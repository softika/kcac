package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreatePrivateNewFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.csv")

	f, err := createPrivate(path)
	if err != nil {
		t.Fatalf("createPrivate() error = %v", err)
	}
	if _, err := f.WriteString("data"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	assertOwnerOnly(t, path)
}

func TestCreatePrivateTightensAnExistingFile(t *testing.T) {
	// The case a bare O_CREATE mode misses. Re-running a dump over a path an
	// earlier build left at 0644 would otherwise write a realm's complete access
	// map into a still world-readable file, and say nothing about it.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	path := filepath.Join(t.TempDir(), "access.csv")
	if err := os.WriteFile(path, []byte("stale contents from an earlier run"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	assertMode(t, path, 0o644) // precondition

	f, err := createPrivate(path)
	if err != nil {
		t.Fatalf("createPrivate() error = %v", err)
	}
	if _, err := f.WriteString("new"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	assertOwnerOnly(t, path)

	// The old contents must be gone, not merely overwritten at the front.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("file = %q, want %q — it was not truncated", got, "new")
	}
}

func TestCreatePrivateLeavesNonRegularFilesAlone(t *testing.T) {
	// Chmod on a device or a pipe fails, and there is nothing to protect there.
	// Writing to /dev/null must keep working.
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/null on Windows")
	}

	f, err := createPrivate(os.DevNull)
	if err != nil {
		t.Fatalf("createPrivate(%s) error = %v", os.DevNull, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString("data"); err != nil {
		t.Errorf("write to %s: %v", os.DevNull, err)
	}
}

func TestCreatePrivateReportsAnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "access.csv")

	if _, err := createPrivate(path); err == nil {
		t.Fatal("expected an error for a path whose directory does not exist")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should name the path", err)
	}
}

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	assertMode(t, path, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
}
