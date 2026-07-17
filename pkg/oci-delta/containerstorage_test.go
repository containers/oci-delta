package ocidelta

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/containers/storage"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type mockContainerStore struct {
	storage.Store

	images      []storage.Image
	imagesErr   error
	imagesByID  map[string]*storage.Image
	imageErr    error
	bigData     map[string]map[string][]byte
	bigDataErr  error
	mountPoints map[string]string
	mountErr    error
	unmountErr  error
}

func (m *mockContainerStore) Images() ([]storage.Image, error) {
	if m.imagesErr != nil {
		return nil, m.imagesErr
	}

	return m.images, nil
}

func (m *mockContainerStore) Image(id string) (*storage.Image, error) {
	if m.imageErr != nil {
		return nil, m.imageErr
	}

	if m.imagesByID != nil {
		if img, ok := m.imagesByID[id]; ok {
			return img, nil
		}
	}

	return nil, fmt.Errorf("image %s not found", id)
}

func (m *mockContainerStore) ImageBigData(id, key string) ([]byte, error) {
	if m.bigDataErr != nil {
		return nil, m.bigDataErr
	}

	if m.bigData != nil {
		if imgData, ok := m.bigData[id]; ok {
			if data, ok := imgData[key]; ok {
				return data, nil
			}
		}
	}

	return nil, fmt.Errorf("big data %s/%s not found", id, key)
}

func (m *mockContainerStore) MountImage(id string, mountOptions []string, mountLabel string) (string, error) {
	if m.mountErr != nil {
		return "", m.mountErr
	}

	if m.mountPoints != nil {
		if mp, ok := m.mountPoints[id]; ok {
			return mp, nil
		}
	}

	return "", fmt.Errorf("mount point for %s not found", id)
}

func (m *mockContainerStore) UnmountImage(id string, force bool) (bool, error) {
	if m.unmountErr != nil {
		return false, m.unmountErr
	}

	return true, nil
}

func assertExpectedError(t *testing.T, err error, errContains string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected error but got none")
	}

	if errContains != "" && !strings.Contains(err.Error(), errContains) {
		t.Errorf("error %q does not contain %q", err.Error(), errContains)
	}
}

const testImageID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func makeSigstoreBlob(mimeType string, payload []byte, annotations map[string]string) []byte {
	rep := sigstoreJSONRepresentation{
		MIMEType:    mimeType,
		Payload:     payload,
		Annotations: annotations,
	}
	jsonData, _ := json.Marshal(rep)

	return append([]byte(sigstoreJSONPrefix), jsonData...)
}

func makeManifestJSON(configDigest string) []byte {
	m := struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}{}
	m.Config.Digest = configDigest
	data, _ := json.Marshal(m)

	return data
}

func TestParseSigstoreBlobs(t *testing.T) {
	validBlob := makeSigstoreBlob("application/vnd.dev.cosign.simplesigning.v1+json", []byte("payload1"), map[string]string{"key": "val"})

	tests := []struct {
		name      string
		blob      []byte
		sizes     []int
		wantCount int
	}{
		{
			name:      "single valid entry",
			blob:      validBlob,
			sizes:     []int{len(validBlob)},
			wantCount: 1,
		},
		{
			name:      "multiple valid entries",
			blob:      append(validBlob, validBlob...),
			sizes:     []int{len(validBlob), len(validBlob)},
			wantCount: 2,
		},
		{
			name:      "entry without sigstore prefix",
			blob:      []byte("not a sigstore entry"),
			sizes:     []int{20},
			wantCount: 0,
		},
		{
			name:      "invalid JSON after prefix",
			blob:      append([]byte(sigstoreJSONPrefix), []byte("{invalid json")...),
			sizes:     []int{len(sigstoreJSONPrefix) + 13},
			wantCount: 0,
		},
		{
			name:      "empty blob and sizes",
			blob:      []byte{},
			sizes:     []int{},
			wantCount: 0,
		},
		{
			name:      "nil blob and sizes",
			blob:      nil,
			sizes:     nil,
			wantCount: 0,
		},
		{
			name:      "size exceeds blob length",
			blob:      validBlob[:10],
			sizes:     []int{len(validBlob)},
			wantCount: 0,
		},
		{
			name:      "mixed valid and invalid entries",
			blob:      append([]byte("not-sigstore-data-here"), validBlob...),
			sizes:     []int{22, len(validBlob)},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseSigstoreBlobs(tt.blob, tt.sizes)
			if len(result) != tt.wantCount {
				t.Errorf("parseSigstoreBlobs() returned %d entries, want %d", len(result), tt.wantCount)
			}
		})
	}
}

func TestParseSigstoreBlobsContent(t *testing.T) {
	payload := []byte("test-payload-data")
	annotations := map[string]string{"annotKey": "annotVal"}
	blob := makeSigstoreBlob("application/vnd.test+json", payload, annotations)

	result := parseSigstoreBlobs(blob, []int{len(blob)})
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}

	sig := result[0]
	if sig.MIMEType != "application/vnd.test+json" {
		t.Errorf("MIMEType = %q, want %q", sig.MIMEType, "application/vnd.test+json")
	}

	if string(sig.Payload) != string(payload) {
		t.Errorf("Payload = %q, want %q", sig.Payload, payload)
	}

	if sig.Annotations["annotKey"] != "annotVal" {
		t.Errorf("Annotations[annotKey] = %q, want %q", sig.Annotations["annotKey"], "annotVal")
	}
}

func TestBuildSignatureArtifact(t *testing.T) {
	tests := []struct {
		name        string
		sigs        []sigstoreJSONRepresentation
		wantErr     bool
		errContains string
		wantLayers  int
	}{
		{
			name: "single signature",
			sigs: []sigstoreJSONRepresentation{
				{
					MIMEType: "application/vnd.dev.cosign.simplesigning.v1+json",
					Payload:  []byte("sig-payload"),
				},
			},
			wantLayers: 1,
		},
		{
			name: "multiple signatures",
			sigs: []sigstoreJSONRepresentation{
				{MIMEType: "type1", Payload: []byte("payload1")},
				{MIMEType: "type2", Payload: []byte("payload2")},
			},
			wantLayers: 2,
		},
		{
			name:        "empty signatures",
			sigs:        []sigstoreJSONRepresentation{},
			wantErr:     true,
			errContains: "no valid sigstore signatures found",
		},
		{
			name:        "nil signatures",
			sigs:        nil,
			wantErr:     true,
			errContains: "no valid sigstore signatures found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artifact, err := buildSignatureArtifact(tt.sigs)
			if tt.wantErr {
				assertExpectedError(t, err, tt.errContains)
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(artifact.manifest.Layers) != tt.wantLayers {
				t.Errorf("got %d layers, want %d", len(artifact.manifest.Layers), tt.wantLayers)
			}
		})
	}
}

func TestBuildSignatureArtifactStructure(t *testing.T) {
	payload := []byte("test-sig-payload")
	annotations := map[string]string{"key1": "val1", "key2": "val2"}
	mimeType := "application/vnd.dev.cosign.simplesigning.v1+json"

	artifact, err := buildSignatureArtifact([]sigstoreJSONRepresentation{
		{MIMEType: mimeType, Payload: payload, Annotations: annotations},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if artifact.manifest.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", artifact.manifest.SchemaVersion)
	}

	if artifact.manifest.Config.MediaType != v1.MediaTypeImageConfig {
		t.Errorf("Config.MediaType = %q, want %q", artifact.manifest.Config.MediaType, v1.MediaTypeImageConfig)
	}

	configData, ok := artifact.blobs[artifact.manifest.Config.Digest]
	if !ok {
		t.Fatal("config blob not found in blobs map")
	}

	if string(configData) != "{}" {
		t.Errorf("config data = %q, want %q", configData, "{}")
	}

	layer := artifact.manifest.Layers[0]
	if layer.MediaType != mimeType {
		t.Errorf("layer MediaType = %q, want %q", layer.MediaType, mimeType)
	}

	if layer.Size != int64(len(payload)) {
		t.Errorf("layer Size = %d, want %d", layer.Size, len(payload))
	}

	payloadBlob, ok := artifact.blobs[layer.Digest]
	if !ok {
		t.Fatal("payload blob not found in blobs map")
	}

	if string(payloadBlob) != string(payload) {
		t.Errorf("payload blob = %q, want %q", payloadBlob, payload)
	}

	if layer.Annotations["key1"] != "val1" || layer.Annotations["key2"] != "val2" {
		t.Errorf("annotations = %v, want %v", layer.Annotations, annotations)
	}

	expectedManifestDigest := digest.FromBytes(artifact.manifestData)
	if artifact.manifestDigest != expectedManifestDigest {
		t.Errorf("manifestDigest = %s, want %s", artifact.manifestDigest, expectedManifestDigest)
	}
}

func TestGetSignatureSizes(t *testing.T) {
	manifestData := []byte(`{"config":{"digest":"sha256:abc123"}}`)
	manifestDigest := digest.FromBytes(manifestData)

	tests := []struct {
		name        string
		store       *mockContainerStore
		imageID     string
		wantSizes   []int
		wantKey     string
		wantErr     bool
		errContains string
	}{
		{
			name: "SignatureSizes in metadata",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {
						ID:       testImageID,
						Metadata: `{"signature-sizes":[100,200]}`,
					},
				},
			},
			imageID:   testImageID,
			wantSizes: []int{100, 200},
			wantKey:   "signatures",
		},
		{
			name: "SignaturesSizes per-digest",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {
						ID:       testImageID,
						Metadata: fmt.Sprintf(`{"signatures-sizes":{"%s":[300,400]}}`, manifestDigest),
					},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": manifestData},
				},
			},
			imageID:   testImageID,
			wantSizes: []int{300, 400},
			wantKey:   "signature-" + manifestDigest.Encoded(),
		},
		{
			name: "no signatures",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {
						ID:       testImageID,
						Metadata: `{}`,
					},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": manifestData},
				},
			},
			imageID:   testImageID,
			wantSizes: nil,
			wantKey:   "",
		},
		{
			name: "empty metadata",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {ID: testImageID, Metadata: ""},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": manifestData},
				},
			},
			imageID:   testImageID,
			wantSizes: nil,
			wantKey:   "",
		},
		{
			name: "store Image error",
			store: &mockContainerStore{
				imageErr: fmt.Errorf("image lookup failed"),
			},
			imageID:     testImageID,
			wantErr:     true,
			errContains: "failed to get image",
		},
		{
			name: "invalid metadata JSON",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {ID: testImageID, Metadata: "{invalid json"},
				},
			},
			imageID:     testImageID,
			wantErr:     true,
			errContains: "failed to parse image metadata",
		},
		{
			name: "ImageBigData error on manifest lookup",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {ID: testImageID, Metadata: `{}`},
				},
				bigDataErr: fmt.Errorf("big data read failed"),
			},
			imageID:   testImageID,
			wantSizes: nil,
			wantKey:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sizes, key, err := getSignatureSizes(tt.store, tt.imageID)
			if tt.wantErr {
				assertExpectedError(t, err, tt.errContains)
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(sizes) != len(tt.wantSizes) {
				t.Fatalf("sizes length = %d, want %d", len(sizes), len(tt.wantSizes))
			}

			for i := range sizes {
				if sizes[i] != tt.wantSizes[i] {
					t.Errorf("sizes[%d] = %d, want %d", i, sizes[i], tt.wantSizes[i])
				}
			}

			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
		})
	}
}

func TestFindImageByConfigDigest(t *testing.T) {
	targetDigest := "sha256:aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233"
	otherDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"

	imageID1 := "1111111111111111aaaaaaaaaaaaaaaa1111111111111111aaaaaaaaaaaaaaaa"
	imageID2 := "2222222222222222bbbbbbbbbbbbbbbb2222222222222222bbbbbbbbbbbbbbbb"

	tests := []struct {
		name        string
		store       *mockContainerStore
		digest      string
		wantID      string
		wantErr     bool
		errContains string
	}{
		{
			name: "matching image found",
			store: &mockContainerStore{
				images: []storage.Image{
					{ID: imageID1},
					{ID: imageID2},
				},
				bigData: map[string]map[string][]byte{
					imageID1: {"manifest": makeManifestJSON(otherDigest)},
					imageID2: {"manifest": makeManifestJSON(targetDigest)},
				},
			},
			digest: targetDigest,
			wantID: imageID2,
		},
		{
			name: "no matching image",
			store: &mockContainerStore{
				images: []storage.Image{
					{ID: imageID1},
				},
				bigData: map[string]map[string][]byte{
					imageID1: {"manifest": makeManifestJSON(otherDigest)},
				},
			},
			digest:      targetDigest,
			wantErr:     true,
			errContains: "no image found with config digest",
		},
		{
			name: "store Images error",
			store: &mockContainerStore{
				imagesErr: fmt.Errorf("storage unavailable"),
			},
			digest:      targetDigest,
			wantErr:     true,
			errContains: "failed to list images",
		},
		{
			name: "ImageBigData error skipped",
			store: &mockContainerStore{
				images: []storage.Image{
					{ID: imageID1},
					{ID: imageID2},
				},
				bigData: map[string]map[string][]byte{
					imageID2: {"manifest": makeManifestJSON(targetDigest)},
				},
			},
			digest: targetDigest,
			wantID: imageID2,
		},
		{
			name: "invalid manifest JSON skipped",
			store: &mockContainerStore{
				images: []storage.Image{
					{ID: imageID1},
					{ID: imageID2},
				},
				bigData: map[string]map[string][]byte{
					imageID1: {"manifest": []byte("{invalid json")},
					imageID2: {"manifest": makeManifestJSON(targetDigest)},
				},
			},
			digest: targetDigest,
			wantID: imageID2,
		},
		{
			name: "empty image list",
			store: &mockContainerStore{
				images: []storage.Image{},
			},
			digest:      targetDigest,
			wantErr:     true,
			errContains: "no image found with config digest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := findImageByConfigDigest(tt.store, tt.digest, SilentLogger{})
			if tt.wantErr {
				assertExpectedError(t, err, tt.errContains)
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if id != tt.wantID {
				t.Errorf("got ID %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestContainerStorageDataSourceCleanup(t *testing.T) {
	t.Run("successful unmount", func(t *testing.T) {
		ds := &containerStorageDataSource{
			store:   &mockContainerStore{},
			imageID: testImageID,
		}

		if err := ds.Cleanup(); err != nil {
			t.Errorf("Cleanup() = %v, want nil", err)
		}
	})

	t.Run("unmount failure", func(t *testing.T) {
		ds := &containerStorageDataSource{
			store:   &mockContainerStore{unmountErr: fmt.Errorf("unmount failed")},
			imageID: testImageID,
		}

		err := ds.Cleanup()
		if err == nil {
			t.Fatal("expected error but got none")
		}

		if !strings.Contains(err.Error(), "unmount failed") {
			t.Errorf("error %q does not contain %q", err.Error(), "unmount failed")
		}
	})
}

func TestResolveContainerStorageDataSource(t *testing.T) {
	targetDigest := "sha256:aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233"

	tests := []struct {
		name        string
		store       *mockContainerStore
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path",
			store: &mockContainerStore{
				images: []storage.Image{
					{ID: testImageID},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": makeManifestJSON(targetDigest)},
				},
				mountPoints: map[string]string{
					testImageID: t.TempDir(),
				},
			},
		},
		{
			name: "image not found",
			store: &mockContainerStore{
				images: []storage.Image{},
			},
			wantErr:     true,
			errContains: "no image found with config digest",
		},
		{
			name: "mount failure",
			store: &mockContainerStore{
				images: []storage.Image{
					{ID: testImageID},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": makeManifestJSON(targetDigest)},
				},
				mountErr: fmt.Errorf("mount permission denied"),
			},
			wantErr:     true,
			errContains: "failed to mount image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds, err := ResolveContainerStorageDataSource(tt.store, targetDigest, SilentLogger{})
			if tt.wantErr {
				assertExpectedError(t, err, tt.errContains)
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if ds == nil {
				t.Fatal("expected non-nil DataSource")
			}

			if err := ds.Cleanup(); err != nil {
				t.Errorf("Cleanup() failed: %v", err)
			}
		})
	}
}

func TestExtractContainerStorageSignatures(t *testing.T) {
	sigPayload := []byte("signature-payload")
	sigBlob := makeSigstoreBlob("application/vnd.dev.cosign.simplesigning.v1+json", sigPayload, nil)

	tests := []struct {
		name        string
		store       *mockContainerStore
		imageID     string
		log         Logger
		wantReaders int
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path with signatures",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {
						ID:       testImageID,
						Metadata: fmt.Sprintf(`{"signature-sizes":[%d]}`, len(sigBlob)),
					},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"signatures": sigBlob},
				},
			},
			imageID:     testImageID,
			log:         SilentLogger{},
			wantReaders: 1,
		},
		{
			name: "no signatures",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {ID: testImageID, Metadata: `{}`},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": []byte(`{}`)},
				},
			},
			imageID:     testImageID,
			log:         SilentLogger{},
			wantReaders: 0,
		},
		{
			name: "getSignatureSizes error",
			store: &mockContainerStore{
				imageErr: fmt.Errorf("image lookup failed"),
			},
			imageID:     testImageID,
			log:         SilentLogger{},
			wantErr:     true,
			errContains: "failed to get image",
		},
		{
			name: "ImageBigData error for signature key",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {
						ID:       testImageID,
						Metadata: `{"signature-sizes":[100]}`,
					},
				},
				bigDataErr: fmt.Errorf("read error"),
			},
			imageID:     testImageID,
			log:         SilentLogger{},
			wantErr:     true,
			errContains: "failed to read signature data",
		},
		{
			name: "no sigstore entries in blob",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {
						ID:       testImageID,
						Metadata: `{"signature-sizes":[20]}`,
					},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"signatures": []byte("not-a-sigstore-entry!")},
				},
			},
			imageID:     testImageID,
			log:         SilentLogger{},
			wantReaders: 0,
		},
		{
			name: "nil logger does not panic",
			store: &mockContainerStore{
				imagesByID: map[string]*storage.Image{
					testImageID: {ID: testImageID, Metadata: `{}`},
				},
				bigData: map[string]map[string][]byte{
					testImageID: {"manifest": []byte(`{}`)},
				},
			},
			imageID:     testImageID,
			log:         nil,
			wantReaders: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readers, err := ExtractContainerStorageSignatures(tt.store, tt.imageID, tt.log)
			if tt.wantErr {
				assertExpectedError(t, err, tt.errContains)
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(readers) != tt.wantReaders {
				t.Errorf("got %d readers, want %d", len(readers), tt.wantReaders)
			}
		})
	}
}

func TestExtractContainerStorageSignaturesContent(t *testing.T) {
	sigPayload := []byte("test-cosign-payload")
	sigBlob := makeSigstoreBlob("application/vnd.dev.cosign.simplesigning.v1+json", sigPayload, map[string]string{"sig": "info"})

	ms := &mockContainerStore{
		imagesByID: map[string]*storage.Image{
			testImageID: {
				ID:       testImageID,
				Metadata: fmt.Sprintf(`{"signature-sizes":[%d]}`, len(sigBlob)),
			},
		},
		bigData: map[string]map[string][]byte{
			testImageID: {"signatures": sigBlob},
		},
	}

	readers, err := ExtractContainerStorageSignatures(ms, testImageID, SilentLogger{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(readers) != 1 {
		t.Fatalf("expected 1 reader, got %d", len(readers))
	}

	reader := readers[0]

	manifestDigest, err := reader.GetManifestDigest()
	if err != nil {
		t.Fatalf("GetManifestDigest() error: %v", err)
	}

	var manifest v1.Manifest

	rawManifest, _, _, err := reader.ReadBlob(manifestDigest)
	if err != nil {
		t.Fatalf("ReadBlob(manifest) error: %v", err)
	}
	defer rawManifest.Close()

	manifestBytes := make([]byte, 4096)
	n, _ := rawManifest.Read(manifestBytes)

	if err := json.Unmarshal(manifestBytes[:n], &manifest); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	if len(manifest.Layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(manifest.Layers))
	}

	layerDesc := manifest.Layers[0]

	payloadReader, size, _, err := reader.ReadBlob(layerDesc.Digest)
	if err != nil {
		t.Fatalf("ReadBlob(payload) error: %v", err)
	}
	defer payloadReader.Close()

	if size != int64(len(sigPayload)) {
		t.Errorf("payload size = %d, want %d", size, len(sigPayload))
	}
}
