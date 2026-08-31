package ocidelta

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tardiff "github.com/containers/tar-diff/pkg/tar-diff"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// --- Mock Implementations ---

// mockOCIWriter is an in-memory OCIWriter for testing
type mockOCIWriter struct {
	files     map[string][]byte
	imageName string
	writeErr  error // for simulating write failures
}

func newMockOCIWriter() *mockOCIWriter {
	return &mockOCIWriter{
		files:     make(map[string][]byte),
		imageName: "test-image",
	}
}

func (m *mockOCIWriter) WriteFile(name string, data []byte) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.files[name] = data

	return nil
}

func (m *mockOCIWriter) WriteFileFromReader(name string, size int64, r io.Reader) error {
	if m.writeErr != nil {
		return m.writeErr
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.files[name] = data

	return nil
}

func (m *mockOCIWriter) ImageName() string {
	return m.imageName
}

func (m *mockOCIWriter) Close() error {
	return nil
}

// mockOCIReader is a simple in-memory OCIReader for testing
// (reusing the pattern from common_test.go's memoryReader)
type mockOCIReader struct {
	blobs   map[digest.Digest][]byte
	readErr error // for simulating read failures
}

func newMockOCIReader() *mockOCIReader {
	return &mockOCIReader{
		blobs: make(map[digest.Digest][]byte),
	}
}

func (m *mockOCIReader) addBlob(d digest.Digest, data []byte) {
	m.blobs[d] = data
}

func (m *mockOCIReader) GetManifestDigest() (digest.Digest, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *mockOCIReader) ReadBlob(d digest.Digest) (io.ReadSeekCloser, int64, digest.Digest, error) {
	if m.readErr != nil {
		return nil, 0, "", m.readErr
	}

	data, ok := m.blobs[d]
	if !ok {
		return nil, 0, "", fmt.Errorf("blob not found: %s", d)
	}

	return readSeekNopCloser{bytes.NewReader(data)}, int64(len(data)), d, nil
}

func (m *mockOCIReader) Close() error {
	return nil
}

// mockDataSource implements DataSource for testing
// It implements tarpatch.DataSource (io.ReadSeeker, io.Closer, SetCurrentFile)
type mockDataSource struct {
	files       map[string][]byte
	currentFile string
	currentData []byte
	pos         int64
	err         error // for simulating errors
}

func newMockDataSource() *mockDataSource {
	return &mockDataSource{
		files: make(map[string][]byte),
	}
}

func (m *mockDataSource) addFile(path string, data []byte) {
	m.files[path] = data
}

// SetCurrentFile sets the current file for reading
func (m *mockDataSource) SetCurrentFile(file string) error {
	if m.err != nil {
		return m.err
	}

	data, ok := m.files[file]
	if !ok {
		// File not found is OK for tar-patch - it might be a new file
		m.currentFile = file
		m.currentData = nil
		m.pos = 0

		return nil
	}
	m.currentFile = file
	m.currentData = data
	m.pos = 0

	return nil
}

// Read implements io.Reader
func (m *mockDataSource) Read(p []byte) (n int, err error) {
	if m.currentData == nil || m.pos >= int64(len(m.currentData)) {
		return 0, io.EOF
	}
	n = copy(p, m.currentData[m.pos:])
	m.pos += int64(n)

	return n, nil
}

// Seek implements io.Seeker
func (m *mockDataSource) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = m.pos + offset
	case io.SeekEnd:
		if m.currentData == nil {
			newPos = offset
		} else {
			newPos = int64(len(m.currentData)) + offset
		}
	default:
		return 0, fmt.Errorf("invalid whence")
	}

	if newPos < 0 {
		return 0, fmt.Errorf("negative position")
	}
	m.pos = newPos

	return m.pos, nil
}

// Close implements io.Closer
func (m *mockDataSource) Close() error {
	return nil
}

func (m *mockDataSource) Cleanup() error {
	return nil
}

// --- Test Helpers ---

// createSimpleTar creates a simple tar archive in memory
func createSimpleTar(fileName string, fileContent []byte) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{
		Name: fileName,
		Mode: 0644,
		Size: int64(len(fileContent)),
	})
	tw.Write(fileContent)
	tw.Close()

	return buf.Bytes()
}

// createTestTarDiff creates a minimal valid tar-diff stream for testing
// It creates a tar-diff from an empty old tar to a new tar with one file
func createTestTarDiff(t *testing.T, fileName string, fileContent []byte) []byte {
	t.Helper()

	// Create an empty old tar archive
	var emptyTar bytes.Buffer
	tw := tar.NewWriter(&emptyTar)
	tw.Close()

	// Create a new tar archive with one file
	newTar := createSimpleTar(fileName, fileContent)

	// Create the tar-diff
	var diffBuf bytes.Buffer
	oldReaders := []io.ReadSeeker{bytes.NewReader(emptyTar.Bytes())}
	newReader := bytes.NewReader(newTar)

	opts := tardiff.NewOptions()

	err := tardiff.Diff(oldReaders, newReader, &diffBuf, opts)
	if err != nil {
		t.Fatalf("failed to create tar-diff: %v", err)
	}

	return diffBuf.Bytes()
}

// --- copyBlobAndRename Tests ---

func TestCopyBlobAndRenameSuccess(t *testing.T) {
	data := []byte("test blob content")
	d := digest.FromBytes(data)

	reader := newMockOCIReader()
	reader.addBlob(d, data)

	writer := newMockOCIWriter()

	err := copyBlobAndRename(writer, reader, d, d)
	if err != nil {
		t.Fatalf("copyBlobAndRename failed: %v", err)
	}

	// Verify the blob was written
	written, ok := writer.files[blobTarName(d)]
	if !ok {
		t.Fatal("blob was not written")
	}

	if !bytes.Equal(written, data) {
		t.Errorf("written data = %q, want %q", written, data)
	}
}

func TestCopyBlobAndRenameWithDifferentDigest(t *testing.T) {
	data := []byte("original content")
	sourceDigest := digest.FromBytes(data)
	targetDigest := digest.FromBytes([]byte("different"))

	reader := newMockOCIReader()
	reader.addBlob(sourceDigest, data)

	writer := newMockOCIWriter()

	// This should fail because the actual digest doesn't match targetDigest
	err := copyBlobAndRename(writer, reader, sourceDigest, targetDigest)
	if err == nil {
		t.Fatal("expected error for digest mismatch, got nil")
	}

	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("expected 'digest mismatch' error, got: %v", err)
	}
}

func TestCopyBlobAndRenameReadError(t *testing.T) {
	reader := newMockOCIReader()
	reader.readErr = fmt.Errorf("simulated read error")

	writer := newMockOCIWriter()

	d := digest.FromBytes([]byte("test"))

	err := copyBlobAndRename(writer, reader, d, d)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "simulated read error") {
		t.Errorf("expected read error, got: %v", err)
	}
}

func TestCopyBlobAndRenameWriteError(t *testing.T) {
	data := []byte("test data")
	d := digest.FromBytes(data)

	reader := newMockOCIReader()
	reader.addBlob(d, data)

	writer := newMockOCIWriter()
	writer.writeErr = fmt.Errorf("simulated write error")

	err := copyBlobAndRename(writer, reader, d, d)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "simulated write error") {
		t.Errorf("expected write error, got: %v", err)
	}
}

func TestCopyBlobAndRenameMissingBlob(t *testing.T) {
	reader := newMockOCIReader()
	writer := newMockOCIWriter()

	d := digest.FromBytes([]byte("nonexistent"))

	err := copyBlobAndRename(writer, reader, d, d)
	if err == nil {
		t.Fatal("expected error for missing blob, got nil")
	}

	if !strings.Contains(err.Error(), "blob not found") {
		t.Errorf("expected 'blob not found' error, got: %v", err)
	}
}

// --- copyBlob Tests ---

func TestCopyBlobSuccess(t *testing.T) {
	data := []byte("blob content")
	d := digest.FromBytes(data)

	reader := newMockOCIReader()
	reader.addBlob(d, data)

	writer := newMockOCIWriter()

	err := copyBlob(writer, reader, d)
	if err != nil {
		t.Fatalf("copyBlob failed: %v", err)
	}

	// Verify the blob was written
	written, ok := writer.files[blobTarName(d)]
	if !ok {
		t.Fatal("blob was not written")
	}

	if !bytes.Equal(written, data) {
		t.Errorf("written data = %q, want %q", written, data)
	}
}

func TestCopyBlobError(t *testing.T) {
	reader := newMockOCIReader()
	reader.readErr = fmt.Errorf("read failed")

	writer := newMockOCIWriter()

	d := digest.FromBytes([]byte("test"))

	err := copyBlob(writer, reader, d)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- writeFileFromPath Tests ---

func TestWriteFileFromPathSuccess(t *testing.T) {
	// Create a test file
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := []byte("file content here")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	writer := newMockOCIWriter()

	err := writeFileFromPath(writer, "output.txt", filePath)
	if err != nil {
		t.Fatalf("writeFileFromPath failed: %v", err)
	}

	// Verify the file was written
	written, ok := writer.files["output.txt"]
	if !ok {
		t.Fatal("file was not written")
	}

	if !bytes.Equal(written, content) {
		t.Errorf("written content = %q, want %q", written, content)
	}
}

func TestWriteFileFromPathNotFound(t *testing.T) {
	writer := newMockOCIWriter()

	err := writeFileFromPath(writer, "output.txt", "/nonexistent/file.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestWriteFileFromPathWriteError(t *testing.T) {
	// Create a test file
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	writer := newMockOCIWriter()
	writer.writeErr = fmt.Errorf("write failed")

	err := writeFileFromPath(writer, "output.txt", filePath)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "write failed") {
		t.Errorf("expected write error, got: %v", err)
	}
}

// --- processLayerDiff Tests ---

func TestProcessLayerDiffSuccess(t *testing.T) {
	dir := t.TempDir()

	// Create test tar-diff data
	testFile := "test.txt"
	testContent := []byte("hello world")
	tarDiff := createTestTarDiff(t, testFile, testContent)

	// Create a mock data source (empty since our tar-diff is just adding a new file)
	dataSource := newMockDataSource()

	writer := newMockOCIWriter()
	log := discardLogger()

	// We don't know the expected diffID in advance, so pass empty
	newDigest, newSize, err := processLayerDiff(dir, log, writer, bytes.NewReader(tarDiff), "", dataSource)
	if err != nil {
		t.Fatalf("processLayerDiff failed: %v", err)
	}

	// Verify a digest was returned
	if newDigest == "" {
		t.Error("expected non-empty digest")
	}

	if newSize == 0 {
		t.Error("expected non-zero size")
	}

	// Verify the layer was written
	writtenData, ok := writer.files[blobTarName(newDigest)]
	if !ok {
		t.Fatal("layer was not written")
	}

	// Verify the written data is valid gzip
	gr, err := gzip.NewReader(bytes.NewReader(writtenData))
	if err != nil {
		t.Fatalf("written data is not valid gzip: %v", err)
	}
	defer gr.Close()

	// Verify we can read the tar content
	tr := tar.NewReader(gr)

	header, err := tr.Next()
	if err != nil {
		t.Fatalf("failed to read tar header: %v", err)
	}

	if header.Name != testFile {
		t.Errorf("tar file name = %q, want %q", header.Name, testFile)
	}

	content, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("failed to read tar content: %v", err)
	}

	if !bytes.Equal(content, testContent) {
		t.Errorf("tar content = %q, want %q", content, testContent)
	}
}

func TestProcessLayerDiffWithExpectedDiffID(t *testing.T) {
	dir := t.TempDir()

	testFile := "file.txt"
	testContent := []byte("content")
	tarDiff := createTestTarDiff(t, testFile, testContent)

	dataSource := newMockDataSource()
	writer := newMockOCIWriter()
	log := discardLogger()

	// First, run without expectedDiffID to get the actual diffID
	actualDigest, _, err := processLayerDiff(dir, log, writer, bytes.NewReader(tarDiff), "", dataSource)
	if err != nil {
		t.Fatalf("first processLayerDiff failed: %v", err)
	}

	// Now compute the expected diffID (uncompressed digest)
	// We need to reconstruct the uncompressed tar to get its digest
	var uncompressed bytes.Buffer
	gr, _ := gzip.NewReader(bytes.NewReader(writer.files[blobTarName(actualDigest)]))
	io.Copy(&uncompressed, gr)
	gr.Close()
	expectedDiffID := digest.FromBytes(uncompressed.Bytes())

	// Reset writer
	writer = newMockOCIWriter()

	// Run again with the expected diffID - should succeed
	_, _, err = processLayerDiff(dir, log, writer, bytes.NewReader(tarDiff), expectedDiffID, dataSource)
	if err != nil {
		t.Fatalf("processLayerDiff with correct expectedDiffID failed: %v", err)
	}
}

func TestProcessLayerDiffDiffIDMismatch(t *testing.T) {
	dir := t.TempDir()

	testFile := "file.txt"
	testContent := []byte("actual content")
	tarDiff := createTestTarDiff(t, testFile, testContent)

	dataSource := newMockDataSource()
	writer := newMockOCIWriter()
	log := discardLogger()

	// Use a wrong expectedDiffID
	wrongDiffID := digest.FromBytes([]byte("wrong content"))

	_, _, err := processLayerDiff(dir, log, writer, bytes.NewReader(tarDiff), wrongDiffID, dataSource)
	if err == nil {
		t.Fatal("expected error for diffID mismatch, got nil")
	}

	if !strings.Contains(err.Error(), "diff_id mismatch") {
		t.Errorf("expected 'diff_id mismatch' error, got: %v", err)
	}
}

func TestProcessLayerDiffInvalidTmpDir(t *testing.T) {
	// Use a non-existent directory
	invalidDir := "/nonexistent/tmp/dir"

	tarDiff := createTestTarDiff(t, "file.txt", []byte("content"))
	dataSource := newMockDataSource()
	writer := newMockOCIWriter()
	log := discardLogger()

	_, _, err := processLayerDiff(invalidDir, log, writer, bytes.NewReader(tarDiff), "", dataSource)
	if err == nil {
		t.Fatal("expected error for invalid tmpDir, got nil")
	}

	if !strings.Contains(err.Error(), "failed to create temp file") {
		t.Errorf("expected temp file creation error, got: %v", err)
	}
}

func TestProcessLayerDiffTarPatchError(t *testing.T) {
	dir := t.TempDir()

	// Create invalid tar-diff data (just the magic header, no actual tar data)
	invalidTarDiff := []byte("tardf1\n\x00invalid data")

	dataSource := newMockDataSource()
	writer := newMockOCIWriter()
	log := discardLogger()

	_, _, err := processLayerDiff(dir, log, writer, bytes.NewReader(invalidTarDiff), "", dataSource)
	if err == nil {
		t.Fatal("expected error for invalid tar-diff, got nil")
	}
	// The error should mention tar-patch failure
	if !strings.Contains(err.Error(), "tar-patch failed") {
		t.Errorf("expected 'tar-patch failed' error, got: %v", err)
	}
}

// --- ApplyDelta Tests ---

func TestApplyDeltaSuccess(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal delta artifact
	testFileName := "test.txt"
	testContent := []byte("delta content")
	tarDiffData := createTestTarDiff(t, testFileName, testContent)
	tarDiffDigest := digest.FromBytes(tarDiffData)

	// Create minimal image config (empty diffIDs, will not validate against actual layer)
	// In real usage, the diffIDs would match the actual layer diffIDs
	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{}, // Empty to skip diffID validation
		},
	}
	imageConfigData, _ := json.Marshal(imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	// Create layer descriptor with tar-diff media type
	layerDigest := digest.FromBytes([]byte("original-layer"))
	deltaLayer := v1.Descriptor{
		MediaType: mediaTypeTarDiff,
		Digest:    tarDiffDigest,
		Size:      int64(len(tarDiffData)),
	}

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
				Size:      100,
			},
		},
	}

	// Create mock reader with blobs
	reader := newMockOCIReader()
	reader.addBlob(imageConfigDigest, imageConfigData)
	reader.addBlob(tarDiffDigest, tarDiffData)

	// Create delta artifact
	delta := &DeltaArtifact{
		reader:            reader,
		imageManifest:     imageManifest,
		imageConfig:       imageConfig,
		imageConfigDigest: imageConfigDigest,
		deltaLayerByTo: map[digest.Digest]v1.Descriptor{
			layerDigest: deltaLayer,
		},
	}

	writer := newMockOCIWriter()
	dataSource := newMockDataSource()
	opts := ApplyOptions{TmpDir: tmpDir}
	log := discardLogger()

	err := ApplyDelta(delta, writer, dataSource, opts, log)
	if err != nil {
		t.Fatalf("ApplyDelta failed: %v", err)
	}

	// Verify oci-layout was written
	if _, ok := writer.files["oci-layout"]; !ok {
		t.Error("oci-layout was not written")
	}

	// Verify image config was written
	configWritten := false

	for name := range writer.files {
		if strings.Contains(name, imageConfigDigest.Encoded()) {
			configWritten = true
			break
		}
	}

	if !configWritten {
		t.Error("image config was not written")
	}

	// Verify index.json was written
	if _, ok := writer.files["index.json"]; !ok {
		t.Error("index.json was not written")
	}
}

func TestApplyDeltaUncompressedTarLayerGetsGzipMediaType(t *testing.T) {
	tmpDir := t.TempDir()

	testFileName := "test.txt"
	testContent := []byte("uncompressed target layer")
	tarDiffData := createTestTarDiff(t, testFileName, testContent)
	tarDiffDigest := digest.FromBytes(tarDiffData)

	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{},
		},
	}
	imageConfigData, _ := json.Marshal(imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	originalLayerDigest := digest.FromBytes([]byte("original-uncompressed-tar"))
	imageManifest := v1.Manifest{
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    imageConfigDigest,
			Size:      int64(len(imageConfigData)),
		},
		Layers: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageLayer,
				Digest:    originalLayerDigest,
				Size:      200,
			},
		},
	}

	reader := newMockOCIReader()
	reader.addBlob(imageConfigDigest, imageConfigData)
	reader.addBlob(tarDiffDigest, tarDiffData)

	delta := &DeltaArtifact{
		reader:            reader,
		imageManifest:     imageManifest,
		imageConfig:       imageConfig,
		imageConfigDigest: imageConfigDigest,
		deltaLayerByTo: map[digest.Digest]v1.Descriptor{
			originalLayerDigest: {
				MediaType: mediaTypeTarDiff,
				Digest:    tarDiffDigest,
				Size:      int64(len(tarDiffData)),
			},
		},
	}

	writer := newMockOCIWriter()

	err := ApplyDelta(delta, writer, newMockDataSource(), ApplyOptions{TmpDir: tmpDir}, discardLogger())
	if err != nil {
		t.Fatalf("ApplyDelta failed: %v", err)
	}

	indexData, ok := writer.files["index.json"]
	if !ok {
		t.Fatal("index.json was not written")
	}

	var index v1.Index

	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatalf("failed to parse index.json: %v", err)
	}

	if len(index.Manifests) != 1 {
		t.Fatalf("index.json manifests = %d, want 1", len(index.Manifests))
	}

	manifestData, ok := writer.files[blobTarName(index.Manifests[0].Digest)]
	if !ok {
		t.Fatal("output image manifest was not written")
	}

	var outputManifest v1.Manifest

	if err := json.Unmarshal(manifestData, &outputManifest); err != nil {
		t.Fatalf("failed to parse output manifest: %v", err)
	}

	if len(outputManifest.Layers) != 1 {
		t.Fatalf("output layers = %d, want 1", len(outputManifest.Layers))
	}

	layer := outputManifest.Layers[0]

	if layer.MediaType != v1.MediaTypeImageLayerGzip {
		t.Errorf("reconstructed layer mediaType = %q, want %q", layer.MediaType, v1.MediaTypeImageLayerGzip)
	}

	if layer.Digest == originalLayerDigest {
		t.Error("reconstructed layer digest still matches original uncompressed tar digest")
	}

	blob, ok := writer.files[blobTarName(layer.Digest)]
	if !ok {
		t.Fatal("reconstructed layer blob was not written")
	}

	if layer.Size != int64(len(blob)) {
		t.Errorf("reconstructed layer size = %d, want %d", layer.Size, len(blob))
	}

	if digest.FromBytes(blob) != layer.Digest {
		t.Errorf("reconstructed layer digest = %s, want gzip blob digest %s", layer.Digest, digest.FromBytes(blob))
	}

	if len(blob) < 2 || blob[0] != 0x1f || blob[1] != 0x8b {
		t.Errorf("reconstructed layer blob does not start with gzip magic 1f 8b")
	}
}

func TestApplyDeltaWithReusedLayer(t *testing.T) {
	tmpDir := t.TempDir()

	// Create image config with two layers (empty diffIDs to skip validation)
	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{}, // Empty to skip diffID validation
		},
	}
	imageConfigData, _ := json.Marshal(imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	// Create two layer descriptors: one reused (not in delta), one in delta
	reusedLayerDigest := digest.FromBytes([]byte("reused-layer"))
	deltaLayerDigest := digest.FromBytes([]byte("delta-layer"))
	tarDiffData := createTestTarDiff(t, "file.txt", []byte("content"))
	tarDiffDigest := digest.FromBytes(tarDiffData)

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
				Size:      50,
			},
			{
				MediaType: v1.MediaTypeImageLayerGzip,
				Digest:    deltaLayerDigest,
				Size:      100,
			},
		},
	}

	reader := newMockOCIReader()
	reader.addBlob(imageConfigDigest, imageConfigData)
	reader.addBlob(tarDiffDigest, tarDiffData)

	delta := &DeltaArtifact{
		reader:            reader,
		imageManifest:     imageManifest,
		imageConfig:       imageConfig,
		imageConfigDigest: imageConfigDigest,
		deltaLayerByTo: map[digest.Digest]v1.Descriptor{
			// Only the second layer is in delta
			deltaLayerDigest: {
				MediaType: mediaTypeTarDiff,
				Digest:    tarDiffDigest,
				Size:      int64(len(tarDiffData)),
			},
		},
	}

	writer := newMockOCIWriter()
	dataSource := newMockDataSource()
	opts := ApplyOptions{TmpDir: tmpDir}
	log := discardLogger()

	err := ApplyDelta(delta, writer, dataSource, opts, log)
	if err != nil {
		t.Fatalf("ApplyDelta failed: %v", err)
	}

	// Verify that index.json was written successfully
	if _, ok := writer.files["index.json"]; !ok {
		t.Error("index.json was not written")
	}
}

func TestApplyDeltaReadBlobError(t *testing.T) {
	tmpDir := t.TempDir()

	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{digest.FromBytes([]byte("layer1"))},
		},
	}
	imageConfigData, _ := json.Marshal(imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	layerDigest := digest.FromBytes([]byte("layer"))
	tarDiffDigest := digest.FromBytes([]byte("tar-diff"))

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
				Size:      100,
			},
		},
	}

	// Create reader that will fail on blob read
	reader := newMockOCIReader()
	reader.addBlob(imageConfigDigest, imageConfigData)
	reader.readErr = fmt.Errorf("simulated read error")

	delta := &DeltaArtifact{
		reader:            reader,
		imageManifest:     imageManifest,
		imageConfig:       imageConfig,
		imageConfigDigest: imageConfigDigest,
		deltaLayerByTo: map[digest.Digest]v1.Descriptor{
			layerDigest: {
				MediaType: mediaTypeTarDiff,
				Digest:    tarDiffDigest,
				Size:      100,
			},
		},
	}

	writer := newMockOCIWriter()
	dataSource := newMockDataSource()
	opts := ApplyOptions{TmpDir: tmpDir}
	log := discardLogger()

	err := ApplyDelta(delta, writer, dataSource, opts, log)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "failed to write image config") {
		t.Errorf("expected image config write error, got: %v", err)
	}
}

func TestApplyDeltaWithOriginalLayer(t *testing.T) {
	tmpDir := t.TempDir()

	imageConfig := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{digest.FromBytes([]byte("layer1"))},
		},
	}
	imageConfigData, _ := json.Marshal(imageConfig)
	imageConfigDigest := digest.FromBytes(imageConfigData)

	// Create an original (non-tar-diff) layer
	originalLayerData := createSimpleTar("original.txt", []byte("original content"))
	originalLayerDigest := digest.FromBytes(originalLayerData)

	layerDigest := digest.FromBytes([]byte("layer"))

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
				Size:      int64(len(originalLayerData)),
			},
		},
	}

	reader := newMockOCIReader()
	reader.addBlob(imageConfigDigest, imageConfigData)
	reader.addBlob(originalLayerDigest, originalLayerData)

	delta := &DeltaArtifact{
		reader:            reader,
		imageManifest:     imageManifest,
		imageConfig:       imageConfig,
		imageConfigDigest: imageConfigDigest,
		deltaLayerByTo: map[digest.Digest]v1.Descriptor{
			layerDigest: {
				MediaType: v1.MediaTypeImageLayerGzip, // Not tar-diff
				Digest:    originalLayerDigest,
				Size:      int64(len(originalLayerData)),
			},
		},
	}

	writer := newMockOCIWriter()
	dataSource := newMockDataSource()
	opts := ApplyOptions{TmpDir: tmpDir}
	log := discardLogger()

	err := ApplyDelta(delta, writer, dataSource, opts, log)
	if err != nil {
		t.Fatalf("ApplyDelta with original layer failed: %v", err)
	}

	// Verify the layer was copied
	layerWritten := false

	for name := range writer.files {
		if strings.Contains(name, originalLayerDigest.Encoded()) {
			layerWritten = true
			break
		}
	}

	if !layerWritten {
		t.Error("original layer was not copied")
	}
}
