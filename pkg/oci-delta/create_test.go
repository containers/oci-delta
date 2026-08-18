package ocidelta

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// --- loadSignatureArtifact tests ---

func TestLoadSignatureArtifact(t *testing.T) {
	want := mustBuildDefaultSignatureArtifact(t)

	reader := newSignatureOCIReader(t, want)

	got, err := loadSignatureArtifact(reader)
	if err != nil {
		t.Fatalf("loadSignatureArtifact() error = %v", err)
	}

	if got.manifestDigest != want.manifestDigest {
		t.Errorf("manifestDigest = %s, want %s", got.manifestDigest, want.manifestDigest)
	}

	if !bytes.Equal(got.manifestData, want.manifestData) {
		t.Errorf("manifestData mismatch")
	}

	if len(got.manifest.Layers) != len(want.manifest.Layers) {
		t.Errorf("len(manifest.Layers) = %d, want %d", len(got.manifest.Layers), len(want.manifest.Layers))
	}

	if got.manifest.Config.Digest != want.manifest.Config.Digest {
		t.Errorf("manifest.Config.Digest = %s, want %s", got.manifest.Config.Digest, want.manifest.Config.Digest)
	}

	if len(got.blobs) != len(want.blobs) {
		t.Errorf("len(blobs) = %d, want %d", len(got.blobs), len(want.blobs))
	}

	for d, data := range want.blobs {
		gotData, ok := got.blobs[d]
		if !ok {
			t.Errorf("missing blob %s", d)
			continue
		}

		if !bytes.Equal(gotData, data) {
			t.Errorf("blob %s content mismatch", d)
		}
	}
}

func TestLoadSignatureArtifactErrors(t *testing.T) {
	t.Run("fails on invalid manifest JSON", func(t *testing.T) {
		artifact := &signatureArtifact{
			manifestData:   []byte(`{"schemaVersion":`),
			manifestDigest: computeDigest([]byte(`{"schemaVersion":`)),
			blobs:          map[digest.Digest][]byte{},
		}

		_, err := loadSignatureArtifact(newSignatureOCIReader(t, artifact))
		if err == nil {
			t.Fatal("expected parse error for invalid signature manifest json")
		}

		if !strings.Contains(err.Error(), "failed to parse signature manifest") {
			t.Errorf("unexpected error %q", err)
		}
	})

	t.Run("fails when reading signature manifest digest", func(t *testing.T) {
		artifact := &signatureArtifact{
			manifestData:   []byte(`{"schemaVersion":`),
			manifestDigest: computeDigest([]byte(`{"schemaVersion":`)),
			blobs:          map[digest.Digest][]byte{},
		}
		reader := &failingManifestDigestReader{
			base:    newSignatureOCIReader(t, artifact),
			failErr: errors.New("manifest digest read failed"),
		}

		_, err := loadSignatureArtifact(reader)
		if err == nil {
			t.Fatal("expected error when reading signature manifest digest")
		}

		if !strings.Contains(err.Error(), "failed to read signature manifest digest") {
			t.Errorf("unexpected error %q", err)
		}
	})

	t.Run("fails when reading signature manifest blob", func(t *testing.T) {
		validArtifact := mustBuildDefaultSignatureArtifact(t)
		flaky := newFailingBlobReader(newSignatureOCIReader(t, validArtifact), validArtifact.manifestDigest, 1, errors.New("manifest read failed"))

		_, err := loadSignatureArtifact(flaky)
		if err == nil {
			t.Fatal("expected error when reading signature manifest blob")
		}

		if !strings.Contains(err.Error(), "failed to read signature manifest") {
			t.Errorf("unexpected error %q", err)
		}
	})
}

// --- CreateDelta tests ---

func TestCreateDeltaTracksReusedAndProcessedLayers(t *testing.T) {
	reusedLayer := newTestLayer(t, "usr/bin/reused", "same-content")
	newLayer := newTestLayer(t, "usr/bin/new", "new-content")

	oldImage := newTestOCIReader(t, []testLayer{reusedLayer})
	newImage := newTestOCIReader(t, []testLayer{reusedLayer, newLayer})

	outputDir := t.TempDir()

	writer, err := newDirOCIWriter(outputDir, "")
	if err != nil {
		t.Fatalf("newDirOCIWriter() error = %v", err)
	}

	stats, err := CreateDelta(oldImage.reader, newImage.reader, writer, CreateOptions{
		TmpDir:      t.TempDir(),
		Parallelism: 1,
	}, discardLogger())
	if err != nil {
		t.Fatalf("CreateDelta() error = %v", err)
	}

	if stats.OldLayers != 1 {
		t.Errorf("OldLayers = %d, want 1", stats.OldLayers)
	}

	if stats.NewLayers != 2 {
		t.Errorf("NewLayers = %d, want 2", stats.NewLayers)
	}

	if stats.ProcessedLayers != 1 {
		t.Errorf("ProcessedLayers = %d, want 1", stats.ProcessedLayers)
	}

	if stats.SkippedLayers != 1 {
		t.Errorf("SkippedLayers = %d, want 1", stats.SkippedLayers)
	}

	if stats.ProcessedLayerBytes <= 0 {
		t.Errorf("ProcessedLayerBytes = %d, want > 0", stats.ProcessedLayerBytes)
	}

	if stats.ProcessedLayerBytes != stats.TarDiffLayerBytes+stats.OriginalLayerBytes {
		t.Errorf("ProcessedLayerBytes (%d) != TarDiffLayerBytes (%d) + OriginalLayerBytes (%d)",
			stats.ProcessedLayerBytes, stats.TarDiffLayerBytes, stats.OriginalLayerBytes)
	}

	deltaManifest := readOutputDeltaManifest(t, outputDir)

	if deltaManifest.Annotations[annotationDeltaTarget] != newImage.manifestDigest.String() {
		t.Errorf("target annotation = %q, want %q", deltaManifest.Annotations[annotationDeltaTarget], newImage.manifestDigest)
	}

	if deltaManifest.Annotations[annotationDeltaSource] != oldImage.manifestDigest.String() {
		t.Errorf("source annotation = %q, want %q", deltaManifest.Annotations[annotationDeltaSource], oldImage.manifestDigest)
	}

	if deltaManifest.Annotations[annotationDeltaSourceConfig] != oldImage.configDigest.String() {
		t.Errorf("source-config annotation = %q, want %q", deltaManifest.Annotations[annotationDeltaSourceConfig], oldImage.configDigest)
	}

	var reusedDigests []string
	if err := json.Unmarshal([]byte(deltaManifest.Annotations[annotationDeltaReused]), &reusedDigests); err != nil {
		t.Fatalf("failed to decode reused digests: %v", err)
	}

	if len(reusedDigests) != 1 || reusedDigests[0] != reusedLayer.digest.String() {
		t.Errorf("reused digests = %v, want [%s]", reusedDigests, reusedLayer.digest)
	}

	var reusedDiffIDs []string
	if err := json.Unmarshal([]byte(deltaManifest.Annotations[annotationDeltaReusedDiffID]), &reusedDiffIDs); err != nil {
		t.Fatalf("failed to decode reused diff_ids: %v", err)
	}

	if len(reusedDiffIDs) != 1 || reusedDiffIDs[0] != reusedLayer.diffID.String() {
		t.Errorf("reused diff_ids = %v, want [%s]", reusedDiffIDs, reusedLayer.diffID)
	}

	imageLayerCount := 0

	for _, layer := range deltaManifest.Layers {
		if layer.Annotations[annotationDeltaContent] != "image-layer" {
			continue
		}
		imageLayerCount++

		if layer.Annotations[annotationDeltaTo] != newLayer.digest.String() {
			t.Errorf("delta.to = %q, want %q", layer.Annotations[annotationDeltaTo], newLayer.digest)
		}
	}

	if imageLayerCount != 1 {
		t.Errorf("image-layer count = %d, want 1", imageLayerCount)
	}
}

func TestCreateDeltaEmbedsSignatureArtifacts(t *testing.T) {
	oldBaseLayer := newTestLayer(t, "usr/bin/old-base", "old-base")
	baseLayer := newTestLayer(t, "usr/bin/base", "base")
	oldImage := newTestOCIReader(t, []testLayer{oldBaseLayer})
	newImage := newTestOCIReader(t, []testLayer{baseLayer})

	sigArtifact := mustBuildDefaultSignatureArtifact(t)

	outputDir := t.TempDir()

	writer, err := newDirOCIWriter(outputDir, "")
	if err != nil {
		t.Fatalf("newDirOCIWriter() error = %v", err)
	}

	_, err = CreateDelta(oldImage.reader, newImage.reader, writer, CreateOptions{
		TmpDir:      t.TempDir(),
		Parallelism: 1,
		Signatures:  []OCIReader{newSignatureOCIReader(t, sigArtifact)},
	}, discardLogger())
	if err != nil {
		t.Fatalf("CreateDelta() error = %v", err)
	}

	deltaManifest := readOutputDeltaManifest(t, outputDir)

	var sigManifestLayers, sigContentLayers int

	for _, layer := range deltaManifest.Layers {
		switch layer.Annotations[annotationDeltaContent] {
		case "cosign-signature":
			sigManifestLayers++
		case "cosign-signature-content":
			sigContentLayers++
		}
	}

	if sigManifestLayers != 1 {
		t.Errorf("cosign-signature layer count = %d, want 1", sigManifestLayers)
	}

	wantContentLayers := 1 + len(sigArtifact.manifest.Layers) // config + signature payload layers
	if sigContentLayers != wantContentLayers {
		t.Errorf("cosign-signature-content layer count = %d, want %d", sigContentLayers, wantContentLayers)
	}

	if _, err := os.ReadFile(filepath.Join(outputDir, blobTarName(sigArtifact.manifestDigest))); err != nil {
		t.Errorf("embedded signature manifest blob missing: %v", err)
	}

	for d := range sigArtifact.blobs {
		if _, err := os.ReadFile(filepath.Join(outputDir, blobTarName(d))); err != nil {
			t.Errorf("embedded signature blob %s missing: %v", d, err)
		}
	}
}

func TestCreateDeltaReadFailures(t *testing.T) {
	oldLayer := newTestLayer(t, "usr/bin/old", "old")
	newLayer := newTestLayer(t, "usr/bin/new", "new")
	oldImage := newTestOCIReader(t, []testLayer{oldLayer})
	newImage := newTestOCIReader(t, []testLayer{newLayer})

	cases := []struct {
		name     string
		old      OCIReader
		new      OCIReader
		wantText string
	}{
		{
			name:     "fails when rereading new manifest blob",
			old:      oldImage.reader,
			new:      newFailingBlobReader(newImage.reader, newImage.manifestDigest, 2, errors.New("manifest reread failed")),
			wantText: "failed to read new image manifest",
		},
		{
			name:     "fails when rereading new config blob",
			old:      oldImage.reader,
			new:      newFailingBlobReader(newImage.reader, newImage.configDigest, 2, errors.New("config reread failed")),
			wantText: "failed to read new image config",
		},
		{
			name:     "fails when parsing new image",
			old:      oldImage.reader,
			new:      &failingManifestDigestReader{base: newImage.reader, failErr: errors.New("new manifest digest failed")},
			wantText: "failed to parse new image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCreateDelta(t, tc.old, tc.new)
			requireErrorContains(t, err, tc.wantText)
		})
	}
}

func TestCreateDeltaWriteFailure(t *testing.T) {
	sentinel := errors.New("write failed")

	t.Run("fails staged writes throughout CreateDelta", func(t *testing.T) {
		tinyOld := newTestLayer(t, "usr/bin/app", "x")
		tinyNew := newTestLayer(t, "usr/bin/app", "y")
		oldImg := newTestOCIReader(t, []testLayer{tinyOld})
		newImg := newTestOCIReader(t, []testLayer{tinyNew})

		// Blob write order for a single new-only layer (no signatures):
		//   blob #1 — image manifest
		//   blob #2 — image config
		//   blob #3 — layer (tar-diff or original)
		//   blob #4 — delta config ({})
		//   blob #5 — delta manifest
		cases := []struct {
			name   string
			writer OCIWriter
		}{
			{name: "oci-layout write", writer: &failingPrefixWriter{failPrefix: "oci-layout", failAt: 1, failErr: sentinel}},
			{name: "manifest blob write", writer: &failingPrefixWriter{failPrefix: "blobs/sha256/", failAt: 1, failErr: sentinel}},
			{name: "delta manifest write", writer: &failingPrefixWriter{failPrefix: "blobs/sha256/", failAt: 5, failErr: sentinel}},
			{name: "index write", writer: &failingPrefixWriter{failPrefix: "index.json", failAt: 1, failErr: sentinel}},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := CreateDelta(oldImg.reader, newImg.reader, tc.writer, CreateOptions{
					TmpDir:      t.TempDir(),
					Parallelism: 1,
				}, discardLogger())
				if !errors.Is(err, sentinel) {
					t.Fatalf("CreateDelta() error = %v, want sentinel write error", err)
				}
			})
		}
	})
}

// --- create test helpers ---
//

func newSignatureOCIReader(t *testing.T, artifact *signatureArtifact) OCIReader {
	t.Helper()
	indexData := []byte(`{
		"schemaVersion": 2,
		"manifests": [{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest": "` + artifact.manifestDigest.String() + `",
			"size": ` + itoa(len(artifact.manifestData)) + `
		}]
	}`)

	files := map[string][]byte{
		"index.json":                         indexData,
		blobTarName(artifact.manifestDigest): artifact.manifestData,
	}
	for d, data := range artifact.blobs {
		files[blobTarName(d)] = data
	}

	return newMemoryReader(files)
}

func runCreateDelta(t *testing.T, old OCIReader, new OCIReader, signatures ...OCIReader) (*CreateStats, error) {
	t.Helper()

	writer, err := newDirOCIWriter(t.TempDir(), "")
	if err != nil {
		t.Fatalf("newDirOCIWriter() error = %v", err)
	}

	return CreateDelta(old, new, writer, CreateOptions{
		TmpDir:      t.TempDir(),
		Parallelism: 1,
		Signatures:  signatures,
	}, discardLogger())
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), want) {
		t.Fatalf("unexpected error %q, want substring %q", err, want)
	}
}

func mustBuildDefaultSignatureArtifact(t *testing.T) *signatureArtifact {
	t.Helper()

	artifact, err := buildSignatureArtifact([]sigstoreJSONRepresentation{{
		MIMEType: "application/vnd.dev.cosign.simplesigning.v1+json",
		Payload:  []byte(`{"critical":{"type":"cosign container image signature"}}`),
		Annotations: map[string]string{
			cosignSignatureAnnotationKey: "dummy-signature",
		},
	}})
	if err != nil {
		t.Fatalf("buildSignatureArtifact() error = %v", err)
	}

	return artifact
}

type testLayer struct {
	digest digest.Digest
	diffID digest.Digest
	data   []byte
}

type testOCIImage struct {
	reader         OCIReader
	manifestDigest digest.Digest
	configDigest   digest.Digest
}

func newTestLayer(t *testing.T, path string, content string) testLayer {
	t.Helper()

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	payload := []byte(content)

	hdr := &tar.Header{
		Name:    path,
		Mode:    0o644,
		Size:    int64(len(payload)),
		ModTime: time.Unix(0, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	diffID := computeDigest(tarBuf.Bytes())

	var gzBuf bytes.Buffer

	gzw := gzip.NewWriter(&gzBuf)
	gzw.Name = ""
	gzw.ModTime = time.Unix(0, 0)
	if _, err := gzw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("gzip Write() error = %v", err)
	}

	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}

	return testLayer{
		digest: computeDigest(gzBuf.Bytes()),
		diffID: diffID,
		data:   gzBuf.Bytes(),
	}
}

func newTestOCIReader(t *testing.T, layers []testLayer) *testOCIImage {
	t.Helper()

	diffIDs := make([]digest.Digest, 0, len(layers))
	manifestLayers := make([]v1.Descriptor, 0, len(layers))
	files := map[string][]byte{}

	for _, layer := range layers {
		diffIDs = append(diffIDs, layer.diffID)
		manifestLayers = append(manifestLayers, v1.Descriptor{
			MediaType: v1.MediaTypeImageLayerGzip,
			Digest:    layer.digest,
			Size:      int64(len(layer.data)),
		})
		files[blobTarName(layer.digest)] = layer.data
	}

	configData, err := json.Marshal(v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: diffIDs,
		},
	})
	if err != nil {
		t.Fatalf("Marshal(config) error = %v", err)
	}
	configDigest := computeDigest(configData)
	files[blobTarName(configDigest)] = configData

	manifestData, err := json.Marshal(v1.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    configDigest,
			Size:      int64(len(configData)),
		},
		Layers: manifestLayers,
	})
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	manifestDigest := computeDigest(manifestData)
	files[blobTarName(manifestDigest)] = manifestData
	files["index.json"] = []byte(`{
		"schemaVersion": 2,
		"manifests": [{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest": "` + manifestDigest.String() + `",
			"size": ` + itoa(len(manifestData)) + `
		}]
	}`)

	return &testOCIImage{
		reader:         newMemoryReader(files),
		manifestDigest: manifestDigest,
		configDigest:   configDigest,
	}
}

func readOutputDeltaManifest(t *testing.T, outputDir string) v1.Manifest {
	t.Helper()

	reader := NewDirOCIReader(outputDir, "")

	manifestDigest, err := reader.GetManifestDigest()
	if err != nil {
		t.Fatalf("GetManifestDigest() error = %v", err)
	}

	manifestData, err := readBlob(reader, manifestDigest)
	if err != nil {
		t.Fatalf("readBlob() error = %v", err)
	}

	var manifest v1.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Unmarshal(delta manifest) error = %v", err)
	}

	return manifest
}

// --- create test doubles ---
// Used to help simulate failures

type failingBlobReader struct {
	base      OCIReader
	failBlob  digest.Digest
	failAt    int
	failErr   error
	readCount map[digest.Digest]int
}

func newFailingBlobReader(base OCIReader, failBlob digest.Digest, failAt int, failErr error) *failingBlobReader {
	return &failingBlobReader{
		base:      base,
		failBlob:  failBlob,
		failAt:    failAt,
		failErr:   failErr,
		readCount: map[digest.Digest]int{},
	}
}

func (r *failingBlobReader) GetManifestDigest() (digest.Digest, error) {
	return r.base.GetManifestDigest()
}

func (r *failingBlobReader) ReadBlob(d digest.Digest) (io.ReadSeekCloser, int64, digest.Digest, error) {
	r.readCount[d]++
	if d == r.failBlob && r.readCount[d] == r.failAt {
		return nil, 0, "", r.failErr
	}

	return r.base.ReadBlob(d)
}

func (r *failingBlobReader) Close() error {
	return r.base.Close()
}

type failingManifestDigestReader struct {
	base    OCIReader
	failErr error
}

func (r *failingManifestDigestReader) GetManifestDigest() (digest.Digest, error) {
	return "", r.failErr
}

func (r *failingManifestDigestReader) ReadBlob(d digest.Digest) (io.ReadSeekCloser, int64, digest.Digest, error) {
	return r.base.ReadBlob(d)
}

func (r *failingManifestDigestReader) Close() error {
	return r.base.Close()
}

type failingPrefixWriter struct {
	failPrefix  string
	failAt      int // 1-based: fail at the Nth write whose name starts with failPrefix
	failErr     error
	prefixCount int
}

func (w *failingPrefixWriter) WriteFile(name string, data []byte) error {
	if strings.HasPrefix(name, w.failPrefix) {
		w.prefixCount++
		if w.prefixCount == w.failAt {
			return w.failErr
		}
	}

	return nil
}

func (w *failingPrefixWriter) WriteFileFromReader(name string, _ int64, r io.Reader) error {
	if strings.HasPrefix(name, w.failPrefix) {
		w.prefixCount++
		if w.prefixCount == w.failAt {
			return w.failErr
		}
	}
	_, err := io.Copy(io.Discard, r)

	return err
}

func (w *failingPrefixWriter) ImageName() string { return "" }

func (w *failingPrefixWriter) Close() error { return nil }
