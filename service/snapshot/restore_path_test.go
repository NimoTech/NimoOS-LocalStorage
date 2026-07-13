package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWithinDirAcceptsPlainNestedPath(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "sub", "dir", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveWithinDir(base, "sub/dir/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(base, "sub", "dir", "file.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWithinDirAcceptsNotYetExistingDestination(t *testing.T) {
	base := t.TempDir()
	// Nothing under base exists yet — this models a restore destination
	// whose parent directories were themselves deleted (the exact "30GB
	// gone" scenario this feature exists for).
	got, err := resolveWithinDir(base, "sub/dir/file.txt.restored-20260713T000000Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(base, "sub", "dir", "file.txt.restored-20260713T000000Z")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWithinDirRejectsEmptyPath(t *testing.T) {
	base := t.TempDir()
	if _, err := resolveWithinDir(base, ""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestResolveWithinDirRejectsAbsolutePath(t *testing.T) {
	base := t.TempDir()
	if _, err := resolveWithinDir(base, "/etc/passwd"); err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestResolveWithinDirRejectsBareDotDot(t *testing.T) {
	base := t.TempDir()
	if _, err := resolveWithinDir(base, "../../../etc/passwd"); err == nil {
		t.Fatal("expected error for bare ../ traversal")
	}
}

func TestResolveWithinDirRejectsNestedDotDotEscape(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWithinDir(base, "sub/../../escaped"); err == nil {
		t.Fatal("expected error for sub/../../escaped")
	}
}

// TestResolveWithinDirRejectsSymlinkEscapeViaExistingComponent is the
// security-critical case filepath.Clean alone cannot catch: an existing
// directory component that is itself a symlink pointing outside base. This
// models a snapshot that (because it's a CoW copy of whatever the live
// volume looked like at snapshot time) contains a symlink planted before
// the snapshot was taken.
func TestResolveWithinDirRejectsSymlinkEscapeViaExistingComponent(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveWithinDir(base, "escape/secret.txt"); err == nil {
		t.Fatal("expected error for symlink escape via an existing component")
	}
}

// TestResolveWithinDirRejectsSymlinkEscapeViaFinalComponent covers the case
// where the final path component itself (not an ancestor directory) is the
// symlink.
func TestResolveWithinDirRejectsSymlinkEscapeViaFinalComponent(t *testing.T) {
	base := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(base, "link.txt")); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveWithinDir(base, "link.txt"); err == nil {
		t.Fatal("expected error for a final-component symlink pointing outside base")
	}
}

func TestResolveWithinDirAcceptsSymlinkStayingInsideBase(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "real", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}

	got, err := resolveWithinDir(base, "alias/file.txt")
	if err != nil {
		t.Fatalf("unexpected error for a symlink that stays inside base: %v", err)
	}
	want := filepath.Join(base, "real", "file.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
