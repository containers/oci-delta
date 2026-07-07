package ocidelta

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/containers/storage"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type mockStore struct {
	storage.Store

	layersByDigest                map[digest.Digest][]storage.Layer
	diffData                      map[string][]byte
	putLayerCallback              func(parentID string, data io.Reader) (*storage.Layer, error)
	layersByUncompressedDigestErr error
	diffErr                       error
	createdImage                  *storage.Image
	imageBigData                  map[string]map[string][]byte // imageID -> key -> data
	sourceImages                  []storage.Image              // Pre-existing images for data source
	mountPoint                    string
}

func (m *mockStore) LayersByUncompressedDigest(d digest.Digest) ([]storage.Layer, error) {
	if m.layersByUncompressedDigestErr != nil {
		return nil, m.layersByUncompressedDigestErr
	}
	layers, ok := m.layersByDigest[d]
	if !ok {
		return nil, nil
	}
	return layers, nil
}

func (m *mockStore) Diff(from, to string, options *storage.DiffOptions) (io.ReadCloser, error) {
	if m.diffErr != nil {
		return nil, m.diffErr
	}
	data, ok := m.diffData[to]
	if !ok {
		return nil, fmt.Errorf("layer %s not found", to)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockStore) PutLayer(id, parent string, names []string, mountLabel string, writeable bool, options *storage.LayerOptions, diff io.Reader) (*storage.Layer, int64, error) {
	if m.putLayerCallback != nil {
		layer, err := m.putLayerCallback(parent, diff)
		if err != nil {
			return nil, -1, err
		}
		return layer, 11, nil
	}
	return nil, -1, fmt.Errorf("putLayerCallback not set")
}

func (m *mockStore) CreateImage(id string, names []string, layer, metadata string, options *storage.ImageOptions) (*storage.Image, error) {
	img := &storage.Image{
		ID:    "test-image-id",
		Names: names,
	}
	m.createdImage = img

	if m.imageBigData == nil {
		m.imageBigData = make(map[string]map[string][]byte)
	}
	m.imageBigData[img.ID] = make(map[string][]byte)

	return img, nil
}

func (m *mockStore) SetImageBigData(id, key string, data []byte, digestFunc func([]byte) (digest.Digest, error)) error {
	if m.imageBigData == nil {
		m.imageBigData = make(map[string]map[string][]byte)
	}
	if m.imageBigData[id] == nil {
		m.imageBigData[id] = make(map[string][]byte)
	}
	m.imageBigData[id][key] = data
	return nil
}

func (m *mockStore) Images() ([]storage.Image, error) {
	var images []storage.Image
	images = append(images, m.sourceImages...)
	if m.createdImage != nil {
		images = append(images, *m.createdImage)
	}
	return images, nil
}

func (m *mockStore) ImageBigData(id, key string) ([]byte, error) {
	if m.imageBigData == nil || m.imageBigData[id] == nil {
		return nil, fmt.Errorf("not found")
	}

	data, ok := m.imageBigData[id][key]

	if !ok {
		return nil, fmt.Errorf("not found")
	}

	return data, nil
}

func (m *mockStore) MountImage(id string, options []string, mountLabel string) (string, error) {
	if m.mountPoint == "" {
		m.mountPoint = "/tmp/mock-mount"
	}

	return m.mountPoint, nil
}

func (m *mockStore) UnmountImage(id string, force bool) (bool, error) {
	return true, nil
}

func TestReuseStorageLayer(t *testing.T) {
	tmpDir := t.TempDir()

	layerContent := []byte("mock-layer-content")
	layerDigest := digest.FromBytes(layerContent)

	baseLayerID := "base-layer-id"
	existingLayerID := "existing-layer-id"

	tests := []struct {
		name           string
		diffID         digest.Digest
		parentID       string
		store          *mockStore
		expectError    bool
		errorContains  string
		verifyParent   bool
		expectedParent string
	}{
		{
			name:     "reuse existing layer with same parent",
			diffID:   layerDigest,
			parentID: baseLayerID,
			store: &mockStore{
				layersByDigest: map[digest.Digest][]storage.Layer{
					layerDigest: {{
						ID:                 existingLayerID,
						Parent:             baseLayerID,
						UncompressedDigest: layerDigest,
					}},
				},
			},
			expectError:    false,
			verifyParent:   true,
			expectedParent: baseLayerID,
		},
		{
			name:     "recreate layer with different parent",
			diffID:   layerDigest,
			parentID: "",
			store: &mockStore{
				layersByDigest: map[digest.Digest][]storage.Layer{
					layerDigest: {{
						ID:                 existingLayerID,
						Parent:             baseLayerID,
						UncompressedDigest: layerDigest,
					}},
				},
				diffData: map[string][]byte{
					existingLayerID: layerContent,
				},
				putLayerCallback: func(parentID string, data io.Reader) (*storage.Layer, error) {
					return &storage.Layer{
						ID:                 "new-layer-id",
						Parent:             parentID,
						UncompressedDigest: layerDigest,
					}, nil
				},
			},
			expectError:    false,
			verifyParent:   true,
			expectedParent: "",
		},
		{
			name:     "layer not found",
			diffID:   digest.FromString("nonexistent"),
			parentID: "",
			store: &mockStore{
				layersByDigest: map[digest.Digest][]storage.Layer{},
			},
			expectError:   true,
			errorContains: "not found in storage",
		},
		{
			name:     "error extracting layer diff",
			diffID:   layerDigest,
			parentID: "",
			store: &mockStore{
				layersByDigest: map[digest.Digest][]storage.Layer{
					layerDigest: {{
						ID:                 existingLayerID,
						Parent:             baseLayerID,
						UncompressedDigest: layerDigest,
					}},
				},
				diffErr: fmt.Errorf("simulated diff error"),
			},
			expectError:   true,
			errorContains: "failed to extract layer diff",
		},
		{
			name:     "multiple layers exist but none match parent",
			diffID:   layerDigest,
			parentID: "desired-parent-id",
			store: &mockStore{
				layersByDigest: map[digest.Digest][]storage.Layer{
					layerDigest: {
						{
							ID:                 "layer1",
							Parent:             "parent1",
							UncompressedDigest: layerDigest,
						},
						{
							ID:                 "layer2",
							Parent:             "parent2",
							UncompressedDigest: layerDigest,
						},
					},
				},
				diffData: map[string][]byte{
					"layer1": layerContent,
				},
				putLayerCallback: func(parentID string, data io.Reader) (*storage.Layer, error) {
					return &storage.Layer{
						ID:                 "new-layer",
						Parent:             parentID,
						UncompressedDigest: layerDigest,
					}, nil
				},
			},
			expectError:    false,
			verifyParent:   true,
			expectedParent: "desired-parent-id",
		},
		{
			name:     "error from LayersByUncompressedDigest",
			diffID:   layerDigest,
			parentID: "",
			store: &mockStore{
				layersByUncompressedDigestErr: fmt.Errorf("storage lookup failed"),
			},
			expectError:   true,
			errorContains: "failed to look up layer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reusedLayer, err := reuseStorageLayer(tt.store, tt.diffID, tt.parentID, tmpDir, SilentLogger{})

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("error message %q does not contain %q", err.Error(), tt.errorContains)
				}
			} else {
				if err != nil {
					t.Fatalf("reuseStorageLayer failed: %v", err)
				}

				if reusedLayer.UncompressedDigest != tt.diffID {
					t.Errorf("digest mismatch: got %s, want %s", reusedLayer.UncompressedDigest, tt.diffID)
				}

				if tt.verifyParent && reusedLayer.Parent != tt.expectedParent {
					t.Errorf("parent mismatch: got %s, want %s", reusedLayer.Parent, tt.expectedParent)
				}
			}
		})
	}
}

// --- ImportDelta Tests ---

func TestImportDeltaWithOriginalLayer(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test layer data
	testContent := []byte("test file content")
	layerTar := createSimpleTarArchive(t, "test.txt", testContent)
	layerTarGz := gzipData(t, layerTar)
	layerDigest := digest.FromBytes(layerTarGz)
	layerDiffID := digest.FromBytes(layerTar)

	// Create image config
	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{layerDiffID},
		},
	}
	imageConfigData, _ := json.Marshal(&imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	// Create image manifest
	imageManifest := v1.Manifest{
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    imageConfigDigest,
			Size:      int64(len(imageConfigData)),
		},
		Layers: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageLayerGzip,
				Digest:    layerDigest,
				Size:      int64(len(layerTarGz)),
			},
		},
	}

	// Create mock reader
	reader := newMockOCIReaderForImport()
	reader.addBlob(imageConfigDigest, imageConfigData)
	reader.addBlob(layerDigest, layerTarGz)

	// Create delta artifact with original layer (non-tar-diff)
	delta := &DeltaArtifact{
		reader:             reader,
		imageManifest:      imageManifest,
		imageConfig:        imageConfig,
		imageConfigDigest:  imageConfigDigest,
		sourceConfigDigest: imageConfigDigest.String(),
		deltaLayerByTo: map[digest.Digest]v1.Descriptor{
			layerDigest: {
				MediaType: v1.MediaTypeImageLayerGzip,
				Digest:    layerDigest,
				Size:      int64(len(layerTarGz)),
			},
		},
	}

	// Create mock store with source image
	layerID := "layer-id-1234567890abcdef1234567890"
	sourceImageID := "source-image-id-1234567890abcdef"

	// Create manifest data for the source image
	manifestData, _ := json.Marshal(imageManifest)

	store := &mockStore{
		putLayerCallback: func(parentID string, data io.Reader) (*storage.Layer, error) {
			return &storage.Layer{
				ID:                 layerID,
				Parent:             parentID,
				UncompressedDigest: layerDiffID,
				CompressedDigest:   layerDigest,
				CompressedSize:     int64(len(layerTarGz)),
			}, nil
		},
		sourceImages: []storage.Image{
			{
				ID: sourceImageID,
			},
		},
		imageBigData: map[string]map[string][]byte{
			sourceImageID: {
				imageConfigDigest.String(): imageConfigData,
				"manifest":                 manifestData,
			},
		},
		mountPoint: t.TempDir(),
	}

	opts := ImportOptions{
		Tag:    "test:latest",
		TmpDir: tmpDir,
	}
	log := SilentLogger{}

	imageID, err := ImportDelta(delta, store, opts, log)
	if err != nil {
		t.Fatalf("ImportDelta failed: %v", err)
	}

	// Verify image was created
	if imageID == "" {
		t.Fatal("expected non-empty image ID")
	}
	if store.createdImage == nil {
		t.Fatal("expected image to be created in store")
	}
	if len(store.createdImage.Names) != 1 || store.createdImage.Names[0] != "test:latest" {
		t.Errorf("expected image name 'test:latest', got %v", store.createdImage.Names)
	}

	// Verify image big data was set
	if store.imageBigData[imageID] == nil {
		t.Fatal("expected image big data to be set")
	}
	if _, ok := store.imageBigData[imageID]["manifest"]; !ok {
		t.Error("expected manifest to be stored")
	}
	if _, ok := store.imageBigData[imageID][imageConfigDigest.String()]; !ok {
		t.Error("expected config to be stored")
	}
}

func TestImportDeltaWithReusedLayer(t *testing.T) {
	tmpDir := t.TempDir()

	// Layer that will be reused
	reusedDiffID := digest.FromBytes([]byte("reused-layer-content"))
	reusedLayerID := "existing-layer-id"

	// Create image config with one layer
	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{reusedDiffID},
		},
	}
	imageConfigData, _ := json.Marshal(&imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	// Create image manifest
	reusedLayerDigest := digest.FromBytes([]byte("reused-layer"))
	imageManifest := v1.Manifest{
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    imageConfigDigest,
			Size:      int64(len(imageConfigData)),
		},
		Layers: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageLayerGzip,
				Digest:    reusedLayerDigest,
				Size:      100,
			},
		},
	}

	// Create mock reader
	reader := newMockOCIReaderForImport()
	reader.addBlob(imageConfigDigest, imageConfigData)

	// Create delta artifact with no layers in delta (reused layer)
	delta := &DeltaArtifact{
		reader:             reader,
		imageManifest:      imageManifest,
		imageConfig:        imageConfig,
		imageConfigDigest:  imageConfigDigest,
		sourceConfigDigest: imageConfigDigest.String(),
		deltaLayerByTo:     map[digest.Digest]v1.Descriptor{}, // Empty - layer is reused
	}

	// Create mock store with existing layer and source image
	sourceImageID := "source-image-id-1234567890abcdef"

	// Create manifest data for the source image
	manifestData, _ := json.Marshal(imageManifest)

	existingLayer := storage.Layer{
		ID:                 reusedLayerID,
		Parent:             "",
		UncompressedDigest: reusedDiffID,
	}
	store := &mockStore{
		layersByDigest: map[digest.Digest][]storage.Layer{
			reusedDiffID: {existingLayer},
		},
		diffData: map[string][]byte{
			reusedLayerID: []byte("layer-diff-data"),
		},
		sourceImages: []storage.Image{
			{
				ID: sourceImageID,
			},
		},
		imageBigData: map[string]map[string][]byte{
			sourceImageID: {
				imageConfigDigest.String(): imageConfigData,
				"manifest":                 manifestData,
			},
		},
		mountPoint: t.TempDir(),
	}

	opts := ImportOptions{
		Tag:    "",
		TmpDir: tmpDir,
	}
	log := SilentLogger{}

	imageID, err := ImportDelta(delta, store, opts, log)
	if err != nil {
		t.Fatalf("ImportDelta with reused layer failed: %v", err)
	}

	if imageID == "" {
		t.Fatal("expected non-empty image ID")
	}
}

// --- Helper functions for ImportDelta tests ---

func createSimpleTarArchive(t *testing.T, fileName string, fileContent []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{
		Name: fileName,
		Mode: 0644,
		Size: int64(len(fileContent)),
	})
	_, _ = tw.Write(fileContent)
	tw.Close()
	return buf.Bytes()
}

func gzipData(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write(data)
	gw.Close()
	return buf.Bytes()
}

type mockOCIReaderForImport struct {
	blobs map[digest.Digest][]byte
}

func newMockOCIReaderForImport() *mockOCIReaderForImport {
	return &mockOCIReaderForImport{
		blobs: make(map[digest.Digest][]byte),
	}
}

func (m *mockOCIReaderForImport) addBlob(d digest.Digest, data []byte) {
	m.blobs[d] = data
}

func (m *mockOCIReaderForImport) GetManifestDigest() (digest.Digest, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *mockOCIReaderForImport) ReadBlob(d digest.Digest) (io.ReadSeekCloser, int64, digest.Digest, error) {
	data, ok := m.blobs[d]

	if !ok {
		return nil, 0, "", fmt.Errorf("blob not found: %s", d)
	}

	return readSeekNopCloser{bytes.NewReader(data)}, int64(len(data)), d, nil
}

func (m *mockOCIReaderForImport) Close() error {
	return nil
}
