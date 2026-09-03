//go:build !no_ostree

package ocidelta

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func createTestOstreeRepo(t *testing.T) (repoPath, ref string) {
	t.Helper()

	if _, err := exec.LookPath("ostree"); err != nil {
		t.Skip("ostree binary not available")
	}

	dir := t.TempDir()
	repoPath = filepath.Join(dir, "repo")
	ref = "test/branch"

	run := func(args ...string) {
		t.Helper()

		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
	}

	run("ostree", "init", "--repo="+repoPath, "--mode=bare-user")

	content := filepath.Join(dir, "content")
	for _, sub := range []string{"usr/bin", "usr/etc", "usr/lib"} {
		os.MkdirAll(filepath.Join(content, sub), 0o755)
	}
	os.WriteFile(filepath.Join(content, "usr/bin/hello"), []byte("hello ostree"), 0o644)
	os.WriteFile(filepath.Join(content, "usr/etc/conf.cfg"), []byte("key=value"), 0o644)
	os.WriteFile(filepath.Join(content, "usr/lib/libfoo.so"), []byte("fake shared lib content"), 0o644)

	run("ostree", "commit", "--repo="+repoPath, "--branch="+ref, content)

	return repoPath, ref
}

func TestOstreeDataSourceReadFile(t *testing.T) {
	repoPath, ref := createTestOstreeRepo(t)

	ds, err := NewOstreeRepoDataSource(repoPath, ref, discardLogger())
	if err != nil {
		t.Fatalf("NewOstreeRepoDataSource: %v", err)
	}
	defer ds.Close()

	if err := ds.SetCurrentFile("usr/bin/hello"); err != nil {
		t.Fatalf("SetCurrentFile: %v", err)
	}

	data, err := io.ReadAll(ds)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if string(data) != "hello ostree" {
		t.Errorf("got %q, want %q", data, "hello ostree")
	}
}

func TestOstreeDataSourceEtcFallback(t *testing.T) {
	repoPath, ref := createTestOstreeRepo(t)

	ds, err := NewOstreeRepoDataSource(repoPath, ref, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if err := ds.SetCurrentFile("etc/conf.cfg"); err != nil {
		t.Fatalf("SetCurrentFile(etc/...): %v", err)
	}

	data, err := io.ReadAll(ds)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "key=value" {
		t.Errorf("got %q, want %q", data, "key=value")
	}
}

func TestOstreeDataSourceFileNotFound(t *testing.T) {
	repoPath, ref := createTestOstreeRepo(t)

	ds, err := NewOstreeRepoDataSource(repoPath, ref, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if err := ds.SetCurrentFile("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestOstreeDataSourceSeek(t *testing.T) {
	repoPath, ref := createTestOstreeRepo(t)

	ds, err := NewOstreeRepoDataSource(repoPath, ref, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if err := ds.SetCurrentFile("usr/bin/hello"); err != nil {
		t.Fatal(err)
	}

	pos, err := ds.Seek(6, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}

	if pos != 6 {
		t.Errorf("Seek returned %d, want 6", pos)
	}

	data, _ := io.ReadAll(ds)
	if string(data) != "ostree" {
		t.Errorf("after seek got %q, want %q", data, "ostree")
	}
}

func TestOstreeDataSourceSwitchFile(t *testing.T) {
	repoPath, ref := createTestOstreeRepo(t)

	ds, err := NewOstreeRepoDataSource(repoPath, ref, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if err := ds.SetCurrentFile("usr/bin/hello"); err != nil {
		t.Fatal(err)
	}

	if err := ds.SetCurrentFile("usr/lib/libfoo.so"); err != nil {
		t.Fatal(err)
	}

	data, _ := io.ReadAll(ds)
	if string(data) != "fake shared lib content" {
		t.Errorf("got %q, want %q", data, "fake shared lib content")
	}
}

func TestOstreeDataSourceReadNoFile(t *testing.T) {
	repoPath, ref := createTestOstreeRepo(t)

	ds, err := NewOstreeRepoDataSource(repoPath, ref, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 10)
	if _, err := ds.Read(buf); err == nil {
		t.Error("expected error reading with no current file")
	}

	if _, err := ds.Seek(0, io.SeekStart); err == nil {
		t.Error("expected error seeking with no current file")
	}
}

func TestOstreeDataSourceInvalidRepo(t *testing.T) {
	_, err := NewOstreeRepoDataSource("/nonexistent", "ref", discardLogger())
	if err == nil {
		t.Fatal("expected error for nonexistent repo")
	}
}
