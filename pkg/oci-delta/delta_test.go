package ocidelta

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// parseReader is a memReader that also supports GetManifestDigest, so it can
// drive ParseDeltaArtifact.
type parseReader struct {
	memReader
	manifest digest.Digest
}

func (p *parseReader) GetManifestDigest() (digest.Digest, error) {
	return p.manifest, nil
}

// blobCloseTracker is a memReader whose blob readers count Close calls.
type blobCloseTracker struct {
	memReader
	closes int
}

type trackedCloser struct {
	io.ReadSeeker
	closes *int
}

func (c trackedCloser) Close() error { *c.closes++; return nil }

func (b *blobCloseTracker) ReadBlob(d digest.Digest) (io.ReadSeekCloser, int64, digest.Digest, error) {
	data, ok := b.blobs[d]
	if !ok {
		return b.memReader.ReadBlob(d)
	}

	return trackedCloser{bytes.NewReader(data), &b.closes}, int64(len(data)), d, nil
}

// deltaFixture builds an in-memory delta artifact for ParseDeltaArtifact tests.
type deltaFixture struct {
	t             *testing.T
	blobs         map[digest.Digest][]byte
	deltaManifest v1.Manifest
}

// addBlob stores data under its true digest (readBlob verifies integrity).
func (f *deltaFixture) addBlob(data []byte) digest.Digest {
	d := digest.FromBytes(data)
	f.blobs[d] = data

	return d
}

func (f *deltaFixture) addJSON(v any) digest.Digest {
	f.t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		f.t.Fatalf("failed to marshal JSON: %v", err)
	}

	return f.addBlob(data)
}

func contentLayer(dgst digest.Digest, content string, extra map[string]string) v1.Descriptor {
	ann := map[string]string{annotationDeltaContent: content}
	for k, v := range extra {
		ann[k] = v
	}

	return v1.Descriptor{Digest: dgst, Annotations: ann}
}

// newDeltaFixture builds a valid delta artifact: embedded image manifest and
// config, one delta layer, and one signature manifest.
func newDeltaFixture(t *testing.T) *deltaFixture {
	t.Helper()
	f := &deltaFixture{t: t, blobs: map[digest.Digest][]byte{}}

	configDgst := f.addJSON(v1.Image{
		RootFS: v1.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromString("diff-1")}},
	})
	manifestDgst := f.addJSON(v1.Manifest{
		Layers: []v1.Descriptor{{Digest: digest.FromString("image-layer-1")}},
	})
	sigDgst := f.addJSON(v1.Manifest{
		Layers: []v1.Descriptor{{Digest: digest.FromString("sig-payload")}},
	})

	f.deltaManifest = v1.Manifest{
		ArtifactType: mediaTypeDelta,
		Annotations:  map[string]string{annotationDeltaSourceConfig: "sha256:cafe"},
		Layers: []v1.Descriptor{
			contentLayer(manifestDgst, "image-manifest", nil),
			contentLayer(configDgst, "image-config", nil),
			contentLayer(digest.FromString("delta-blob"), "image-layer",
				map[string]string{annotationDeltaTo: digest.FromString("to-layer").String()}),
			contentLayer(sigDgst, "cosign-signature", nil),
		},
	}

	return f
}

// reader serializes the delta manifest and returns a reader for it.
func (f *deltaFixture) reader() *parseReader {
	return &parseReader{
		memReader: memReader{blobs: f.blobs},
		manifest:  f.addJSON(f.deltaManifest),
	}
}

func TestParseDeltaArtifact(t *testing.T) {
	f := newDeltaFixture(t)
	// Layers that must be tolerated and skipped: an image-layer without a
	// "to" annotation, one with an invalid "to", and an unknown content type.
	f.deltaManifest.Layers = append(f.deltaManifest.Layers,
		contentLayer(digest.FromString("no-to"), "image-layer", nil),
		contentLayer(digest.FromString("bad-to"), "image-layer",
			map[string]string{annotationDeltaTo: "not-a-digest"}),
		contentLayer(digest.FromString("other"), "something-else", nil),
	)

	d, err := ParseDeltaArtifact(f.reader(), discardLogger())
	if err != nil {
		t.Fatalf("ParseDeltaArtifact() unexpected error: %v", err)
	}
	defer d.Close()

	if got, want := d.SourceConfigDigest(), "sha256:cafe"; got != want {
		t.Errorf("SourceConfigDigest() = %q, want %q", got, want)
	}

	if len(d.imageManifest.Layers) != 1 || len(d.imageConfig.RootFS.DiffIDs) != 1 {
		t.Errorf("embedded manifest/config not parsed: %d layers, %d diff_ids",
			len(d.imageManifest.Layers), len(d.imageConfig.RootFS.DiffIDs))
	}

	if len(d.Signatures()) != 1 {
		t.Errorf("Signatures() len = %d, want 1", len(d.Signatures()))
	}

	if got, want := d.imageManifestDigest, f.deltaManifest.Layers[0].Digest; got != want {
		t.Errorf("imageManifestDigest = %s, want %s (must come from the image-manifest layer)", got, want)
	}

	if got, want := d.imageConfigDigest, f.deltaManifest.Layers[1].Digest; got != want {
		t.Errorf("imageConfigDigest = %s, want %s (must come from the image-config layer)", got, want)
	}

	if len(d.deltaLayerByTo) != 1 {
		t.Fatalf("deltaLayerByTo len = %d, want 1 (invalid/missing 'to' must be skipped)", len(d.deltaLayerByTo))
	}

	desc, ok := d.deltaLayerByTo[digest.FromString("to-layer")]
	if !ok || desc.Digest != digest.FromString("delta-blob") {
		t.Errorf("deltaLayerByTo mapping wrong: %+v", desc)
	}
}

func TestParseDeltaArtifactSkipsBadSignatures(t *testing.T) {
	f := newDeltaFixture(t)
	// Signature manifests that are missing or invalid JSON are skipped, not fatal.
	f.deltaManifest.Layers = append(f.deltaManifest.Layers,
		contentLayer(digest.FromString("missing-sig"), "cosign-signature", nil),
		contentLayer(f.addBlob([]byte("not json")), "cosign-signature", nil),
	)

	d, err := ParseDeltaArtifact(f.reader(), discardLogger())
	if err != nil {
		t.Fatalf("ParseDeltaArtifact() unexpected error: %v", err)
	}
	defer d.Close()

	if len(d.Signatures()) != 1 {
		t.Errorf("Signatures() len = %d, want 1 (bad signature manifests skipped)", len(d.Signatures()))
	}
}

func TestParseDeltaArtifactErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(f *deltaFixture)
		reader  func(f *deltaFixture) OCIReader // defaults to f.reader()
		wantErr string
	}{
		{
			name:    "manifest digest unavailable",
			reader:  func(f *deltaFixture) OCIReader { return &memReader{blobs: map[digest.Digest][]byte{}} },
			wantErr: "failed to read delta manifest digest",
		},
		{
			name: "delta manifest blob missing",
			reader: func(f *deltaFixture) OCIReader {
				return &parseReader{
					memReader: memReader{blobs: f.blobs},
					manifest:  digest.FromString("nonexistent"),
				}
			},
			wantErr: "failed to read delta manifest blob",
		},
		{
			name: "delta manifest parse failure",
			reader: func(f *deltaFixture) OCIReader {
				return &parseReader{
					memReader: memReader{blobs: f.blobs},
					manifest:  f.addBlob([]byte("not json")),
				}
			},
			wantErr: "failed to parse delta manifest",
		},
		{
			name:    "wrong artifact type",
			setup:   func(f *deltaFixture) { f.deltaManifest.ArtifactType = "application/other" },
			wantErr: "not a delta artifact",
		},
		{
			name:    "missing source config annotation",
			setup:   func(f *deltaFixture) { delete(f.deltaManifest.Annotations, annotationDeltaSourceConfig) },
			wantErr: "delta manifest missing required source config annotation",
		},
		{
			name:    "missing image manifest layer",
			setup:   func(f *deltaFixture) { f.deltaManifest.Layers[0].Annotations[annotationDeltaContent] = "x" },
			wantErr: "no embedded image manifest layer",
		},
		{
			name:    "missing image config layer",
			setup:   func(f *deltaFixture) { f.deltaManifest.Layers[1].Annotations[annotationDeltaContent] = "x" },
			wantErr: "no embedded image config layer",
		},
		{
			name:    "image manifest blob missing",
			setup:   func(f *deltaFixture) { f.deltaManifest.Layers[0].Digest = digest.FromString("gone") },
			wantErr: "failed to read embedded image manifest blob",
		},
		{
			name: "image manifest parse failure",
			setup: func(f *deltaFixture) {
				f.deltaManifest.Layers[0].Digest = f.addBlob([]byte("not json"))
			},
			wantErr: "failed to parse embedded image manifest",
		},
		{
			name:    "image config blob missing",
			setup:   func(f *deltaFixture) { f.deltaManifest.Layers[1].Digest = digest.FromString("gone") },
			wantErr: "failed to read embedded image config blob",
		},
		{
			name: "image config parse failure",
			setup: func(f *deltaFixture) {
				f.deltaManifest.Layers[1].Digest = f.addBlob([]byte("not json"))
			},
			wantErr: "failed to parse embedded image config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDeltaFixture(t)
			if tt.setup != nil {
				tt.setup(f)
			}

			var r OCIReader
			if tt.reader != nil {
				r = tt.reader(f)
			} else {
				r = f.reader()
			}

			_, err := ParseDeltaArtifact(r, discardLogger())
			if err == nil {
				t.Fatal("ParseDeltaArtifact() expected error, got nil")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseDeltaArtifact() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestAccessors(t *testing.T) {
	manifestDgst := digest.FromString("test-manifest")
	sigs := []EmbeddedSignature{{Manifest: v1.Manifest{}}}
	d := &DeltaArtifact{
		sourceConfigDigest:  "sha256:abc123",
		imageManifestDigest: manifestDgst,
		signatures:          sigs,
	}

	if got := d.SourceConfigDigest(); got != "sha256:abc123" {
		t.Errorf("SourceConfigDigest() = %q, want %q", got, "sha256:abc123")
	}

	if got := d.ImageManifestDigest(); got != manifestDgst {
		t.Errorf("ImageManifestDigest() = %s, want %s", got, manifestDgst)
	}

	if got := d.Signatures(); len(got) != 1 {
		t.Errorf("Signatures() len = %d, want 1", len(got))
	}

	empty := &DeltaArtifact{}
	if got := empty.SourceConfigDigest(); got != "" {
		t.Errorf("SourceConfigDigest() = %q, want %q", got, "")
	}

	if got := empty.Signatures(); got != nil {
		t.Errorf("Signatures() = %v, want nil", got)
	}
}

func TestDeltaArtifactReadBlob(t *testing.T) {
	data := []byte("hello blob")
	dgst := digest.FromBytes(data)
	d := &DeltaArtifact{reader: &memReader{blobs: map[digest.Digest][]byte{dgst: data}}}

	got, err := d.ReadBlob(dgst)
	if err != nil {
		t.Fatalf("ReadBlob() unexpected error: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("ReadBlob() = %q, want %q", got, data)
	}

	if _, err := d.ReadBlob(digest.FromString("nonexistent")); err == nil {
		t.Error("ReadBlob() expected error for missing blob")
	}
}

func TestGetBlobReader(t *testing.T) {
	data := []byte("seekable content")
	dgst := digest.FromBytes(data)
	d := &DeltaArtifact{reader: &memReader{blobs: map[digest.Digest][]byte{dgst: data}}}

	r, err := d.GetBlobReader(dgst)
	if err != nil {
		t.Fatalf("GetBlobReader() unexpected error: %v", err)
	}
	defer r.Close()

	buf := make([]byte, 4)
	r.Read(buf)

	if string(buf) != "seek" {
		t.Errorf("GetBlobReader() first read = %q, want \"seek\"", buf)
	}

	r.Seek(0, io.SeekStart)
	buf2 := make([]byte, 4)
	r.Read(buf2)

	if string(buf2) != "seek" {
		t.Errorf("GetBlobReader() after seek = %q, want \"seek\"", buf2)
	}

	r.Seek(0, io.SeekStart)

	if got, _ := io.ReadAll(r); !bytes.Equal(got, data) {
		t.Errorf("GetBlobReader() full content = %q, want %q", got, data)
	}

	if _, err := d.GetBlobReader(digest.FromString("nonexistent")); err == nil {
		t.Error("GetBlobReader() expected error for missing blob")
	}
}

func TestGetBlobSize(t *testing.T) {
	data := []byte("some sized content")
	dgst := digest.FromBytes(data)
	tracker := &blobCloseTracker{memReader: memReader{blobs: map[digest.Digest][]byte{dgst: data}}}
	d := &DeltaArtifact{reader: tracker}

	size, err := d.GetBlobSize(dgst)
	if err != nil {
		t.Fatalf("GetBlobSize() unexpected error: %v", err)
	}

	if size != int64(len(data)) {
		t.Errorf("GetBlobSize() = %d, want %d", size, len(data))
	}

	if tracker.closes != 1 {
		t.Errorf("GetBlobSize() closed blob reader %d times, want 1 (fd leak)", tracker.closes)
	}

	if _, err := d.GetBlobSize(digest.FromString("nonexistent")); err == nil {
		t.Error("GetBlobSize() expected error for missing blob")
	}
}
