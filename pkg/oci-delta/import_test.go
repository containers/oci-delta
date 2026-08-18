package ocidelta

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	diffReadErr                   error
	putLayerCallback              func(parentID string, data io.Reader) (*storage.Layer, error)
	putLayerErr                   error
	layersByUncompressedDigestErr error
	diffErr                       error
	createImageErr                error
	setImageBigDataErr            error
	setImageBigDataErrOnKey       string
	nextImageID                   string
	createdImages                 []*storage.Image
	imageBigData                  map[string]map[string][]byte
}

type failingReader struct {
	data []byte
	err  error
	pos  int
}

func (f *failingReader) Read(p []byte) (n int, err error) {
	if f.pos >= len(f.data)/2 {
		return 0, f.err
	}
	n = copy(p, f.data[f.pos:])
	f.pos += n

	return n, nil
}

func (f *failingReader) Close() error {
	return nil
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

	if m.diffReadErr != nil {
		return &failingReader{data: data, err: m.diffReadErr}, nil
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockStore) PutLayer(id, parent string, names []string, mountLabel string, writeable bool, options *storage.LayerOptions, diff io.Reader) (*storage.Layer, int64, error) {
	if m.putLayerErr != nil {
		return nil, -1, m.putLayerErr
	}

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
	if m.createImageErr != nil {
		return nil, m.createImageErr
	}

	imageID := id
	if imageID == "" {
		if m.nextImageID != "" {
			imageID = m.nextImageID
		} else {
			imageID = "mock-image-id"
		}
	}

	image := &storage.Image{
		ID:       imageID,
		Names:    names,
		TopLayer: layer,
		Metadata: metadata,
	}

	m.createdImages = append(m.createdImages, image)

	return image, nil
}

func (m *mockStore) SetImageBigData(id, key string, data []byte, digestManifest func([]byte) (digest.Digest, error)) error {
	if m.setImageBigDataErr != nil {
		return m.setImageBigDataErr
	}

	// Pattern-match keys to target specific SetImageBigData call sites:
	// - "manifest" → plain manifest key (import.go:127)
	// - "manifest-digest" → manifest digest key containing both "manifest" and "sha256:" (import.go:132)
	// - "sha256:" → config digest key starting with "sha256:" but NOT containing "manifest" (import.go:141)
	if m.setImageBigDataErrOnKey != "" {
		if m.setImageBigDataErrOnKey == "manifest" && key == "manifest" {
			return fmt.Errorf("failed to set big data for key %s", key)
		}

		if m.setImageBigDataErrOnKey == "manifest-digest" && strings.Contains(key, "manifest") && strings.Contains(key, "sha256:") {
			return fmt.Errorf("failed to set big data for key %s", key)
		}

		if m.setImageBigDataErrOnKey == "sha256:" && strings.HasPrefix(key, "sha256:") && !strings.Contains(key, "manifest") {
			return fmt.Errorf("failed to set big data for key %s", key)
		}
	}

	if m.imageBigData == nil {
		m.imageBigData = make(map[string]map[string][]byte)
	}

	if m.imageBigData[id] == nil {
		m.imageBigData[id] = make(map[string][]byte)
	}

	m.imageBigData[id][key] = data

	return nil
}

var (
	testConfigData      = []byte(`{"rootfs":{"diff_ids":[]}}`)
	testLayerDigest     = digest.FromString("layer-content")
	testLayerDescriptor = v1.Descriptor{Digest: digest.FromString("layer-content"), Size: 100}
)

func makeGzippedTarLayer(content string) []byte {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	tw.WriteHeader(&tar.Header{Name: "test.txt", Mode: 0644, Size: int64(len(content))})
	tw.Write([]byte(content))
	tw.Close()

	var gzipBuf bytes.Buffer
	gw := gzip.NewWriter(&gzipBuf)
	gw.Write(tarBuf.Bytes())
	gw.Close()

	return gzipBuf.Bytes()
}

func TestImportDelta(t *testing.T) {
	tests := []struct {
		name                    string
		sourceConfigDigest      string
		configData              []byte
		layerDigests            []digest.Digest
		layerDescriptors        []v1.Descriptor
		deltaLayerByTo          map[digest.Digest]v1.Descriptor
		blobData                map[digest.Digest][]byte
		existingLayers          map[digest.Digest][]storage.Layer
		diffData                map[string][]byte
		tag                     string
		expectedImageID         string
		createImageErr          error
		setImageBigDataErrOnKey string
		readBlobErr             error
		putLayerErr             error
		expectError             bool
		errorContains           string
	}{
		{
			name:               "import delta with one reused layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{
				testLayerDigest: {{
					ID:                 "existing-layer-1",
					Parent:             "",
					UncompressedDigest: testLayerDigest,
				}},
			},
			tag:             "test:latest",
			expectedImageID: "new-image-123",
			expectError:     false,
		},
		{
			name:               "import delta with tar-diff layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("tar-diff-blob"),
					Size:      50,
					MediaType: mediaTypeTarDiff,
				},
			},
			blobData: map[digest.Digest][]byte{
				digest.FromString("tar-diff-blob"): []byte("mock-blob-data"),
			},
			existingLayers:  map[digest.Digest][]storage.Layer{},
			tag:             "test:latest",
			expectedImageID: "new-image-123",
			expectError:     false,
		},
		{
			name:               "error when layer not found in storage",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers:     map[digest.Digest][]storage.Layer{},
			tag:                "test:latest",
			expectError:        true,
			errorContains:      "not found in storage",
		},
		{
			name:               "import delta with original layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("original-layer-blob"),
					Size:      100,
					MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				},
			},
			blobData: map[digest.Digest][]byte{
				digest.FromString("original-layer-blob"): makeGzippedTarLayer("test file content"),
			},
			existingLayers:  map[digest.Digest][]storage.Layer{},
			tag:             "test:latest",
			expectedImageID: "new-image-123",
			expectError:     false,
		},
		{
			name:               "error when CreateImage fails",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{
				testLayerDigest: {{
					ID:                 "existing-layer-1",
					Parent:             "",
					UncompressedDigest: testLayerDigest,
				}},
			},
			tag:            "test:latest",
			createImageErr: fmt.Errorf("disk full"),
			expectError:    true,
			errorContains:  "failed to create image",
		},
		{
			name:               "error when SetImageBigData fails for manifest",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{
				testLayerDigest: {{
					ID:                 "existing-layer-1",
					Parent:             "",
					UncompressedDigest: testLayerDigest,
				}},
			},
			tag:                     "test:latest",
			expectedImageID:         "new-image-123",
			setImageBigDataErrOnKey: "manifest",
			expectError:             true,
			errorContains:           "failed to store manifest",
		},
		{
			name:               "error when SetImageBigData fails for manifest digest key",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{
				testLayerDigest: {{
					ID:                 "existing-layer-1",
					Parent:             "",
					UncompressedDigest: testLayerDigest,
				}},
			},
			tag:                     "test:latest",
			expectedImageID:         "new-image-123",
			setImageBigDataErrOnKey: "manifest-digest",
			expectError:             true,
			errorContains:           "failed to store manifest",
		},
		{
			name:               "error when SetImageBigData fails for config",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{
				testLayerDigest: {{
					ID:                 "existing-layer-1",
					Parent:             "",
					UncompressedDigest: testLayerDigest,
				}},
			},
			tag:                     "test:latest",
			expectedImageID:         "new-image-123",
			setImageBigDataErrOnKey: "sha256:",
			expectError:             true,
			errorContains:           "failed to store config",
		},
		{
			name:               "error when ReadBlob fails for config",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo:     map[digest.Digest]v1.Descriptor{},
			blobData:           map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{
				testLayerDigest: {{
					ID:                 "existing-layer-1",
					Parent:             "",
					UncompressedDigest: testLayerDigest,
				}},
			},
			tag:           "test:latest",
			readBlobErr:   fmt.Errorf("I/O error reading blob"),
			expectError:   true,
			errorContains: "failed to read config",
		},
		{
			name:               "error when PutLayer fails for tar-diff layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("tar-diff-blob"),
					Size:      50,
					MediaType: mediaTypeTarDiff,
				},
			},
			blobData: map[digest.Digest][]byte{
				digest.FromString("tar-diff-blob"): []byte("mock-blob-data"),
			},
			existingLayers: map[digest.Digest][]storage.Layer{},
			tag:            "test:latest",
			putLayerErr:    fmt.Errorf("disk quota exceeded"),
			expectError:    true,
			errorContains:  "failed to store reconstructed layer",
		},
		{
			name:               "error when PutLayer fails for original layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("original-layer-blob"),
					Size:      100,
					MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				},
			},
			blobData: map[digest.Digest][]byte{
				digest.FromString("original-layer-blob"): makeGzippedTarLayer("test file content"),
			},
			existingLayers: map[digest.Digest][]storage.Layer{},
			tag:            "test:latest",
			putLayerErr:    fmt.Errorf("permission denied"),
			expectError:    true,
			errorContains:  "failed to store layer",
		},
		{
			name:               "error when gzip decompression fails",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("corrupted-gzip-blob"),
					Size:      30,
					MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				},
			},
			blobData: map[digest.Digest][]byte{
				digest.FromString("corrupted-gzip-blob"): []byte("this is not valid gzip data!"),
			},
			existingLayers: map[digest.Digest][]storage.Layer{},
			tag:            "test:latest",
			expectError:    true,
			errorContains:  "failed to decompress layer",
		},
		{
			name:               "error when GetBlobReader fails for tar-diff layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("missing-tar-diff-blob"),
					Size:      50,
					MediaType: mediaTypeTarDiff,
				},
			},
			blobData:       map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{},
			tag:            "test:latest",
			expectError:    true,
			errorContains:  "failed to read tar-diff",
		},
		{
			name:               "error when GetBlobReader fails for original layer",
			sourceConfigDigest: "sha256:abc123",
			configData:         testConfigData,
			layerDigests:       []digest.Digest{testLayerDigest},
			layerDescriptors:   []v1.Descriptor{testLayerDescriptor},
			deltaLayerByTo: map[digest.Digest]v1.Descriptor{
				testLayerDigest: {
					Digest:    digest.FromString("missing-original-blob"),
					Size:      100,
					MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				},
			},
			blobData:       map[digest.Digest][]byte{},
			existingLayers: map[digest.Digest][]storage.Layer{},
			tag:            "test:latest",
			expectError:    true,
			errorContains:  "failed to read layer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDigest := digest.FromBytes(tt.configData)

			store := &mockStore{
				nextImageID:             tt.expectedImageID,
				layersByDigest:          tt.existingLayers,
				imageBigData:            make(map[string]map[string][]byte),
				diffData:                tt.diffData,
				createImageErr:          tt.createImageErr,
				setImageBigDataErrOnKey: tt.setImageBigDataErrOnKey,
				putLayerErr:             tt.putLayerErr,
				putLayerCallback: func(parentID string, data io.Reader) (*storage.Layer, error) {
					receivedData, _ := io.ReadAll(data)

					layerID := "new-layer-reconstructed-from-tar-diff"
					if parentID != "" {
						layerID = "new-layer-child-of-" + parentID
					}

					layer := &storage.Layer{
						ID:                 layerID,
						Parent:             parentID,
						UncompressedDigest: tt.layerDigests[0],
					}

					if len(receivedData) > 0 {
						layer.CompressedDigest = digest.FromBytes(receivedData)
						layer.CompressedSize = int64(len(receivedData))
					}

					return layer, nil
				},
			}

			reader := newMockOCIReader()
			reader.addBlob(configDigest, tt.configData)
			reader.readErr = tt.readBlobErr

			for digest, data := range tt.blobData {
				reader.addBlob(digest, data)
			}

			delta := &DeltaArtifact{
				reader:              reader,
				imageConfig:         v1.Image{RootFS: v1.RootFS{DiffIDs: tt.layerDigests}},
				imageManifest:       v1.Manifest{Layers: tt.layerDescriptors},
				imageConfigDigest:   configDigest,
				sourceConfigDigest:  tt.sourceConfigDigest,
				deltaLayerByTo:      tt.deltaLayerByTo,
				imageManifestDigest: digest.FromString("manifest-digest"),
				signatures:          []EmbeddedSignature{},
			}

			dataSource := newMockDataSource()

			imageID, err := ImportDelta(delta, store, dataSource, ImportOptions{
				Tag:    tt.tag,
				TmpDir: t.TempDir(),
			}, discardLogger())

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errorContains)
				}

				return
			}

			if err != nil {
				t.Fatalf("ImportDelta failed: %v", err)
			}

			if imageID != tt.expectedImageID {
				t.Errorf("expected imageID %q, got %q", tt.expectedImageID, imageID)
			}

			if len(store.createdImages) != 1 {
				t.Fatalf("expected 1 created image, got %d", len(store.createdImages))
			}

			createdImage := store.createdImages[0]
			if len(createdImage.Names) != 1 || createdImage.Names[0] != tt.tag {
				t.Errorf("expected image name %q, got %v", tt.tag, createdImage.Names)
			}
		})
	}
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
		tmpDir         string
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
		{
			name:     "error when PutLayer fails during recreation",
			diffID:   layerDigest,
			parentID: "new-parent-id",
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
				putLayerErr: fmt.Errorf("storage quota exceeded"),
			},
			expectError:   true,
			errorContains: "failed to recreate layer",
		},
		{
			name:     "error creating temp file",
			diffID:   layerDigest,
			parentID: "new-parent-id",
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
			},
			tmpDir:        "/nonexistent/invalid/path",
			expectError:   true,
			errorContains: "failed to create temp file",
		},
		{
			name:     "error during io.Copy of layer diff",
			diffID:   layerDigest,
			parentID: "new-parent-id",
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
				diffReadErr: fmt.Errorf("I/O error reading layer"),
			},
			expectError:   true,
			errorContains: "failed to buffer layer diff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useTmpDir := tmpDir
			if tt.tmpDir != "" {
				useTmpDir = tt.tmpDir
			}
			reusedLayer, err := reuseStorageLayer(tt.store, tt.diffID, tt.parentID, useTmpDir, discardLogger())

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
