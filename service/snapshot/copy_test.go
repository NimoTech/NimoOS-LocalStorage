package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestExecCopierCopiesFileContent is a real (non-faked) integration test of
// ExecCopier against whatever filesystem the test's temp directory sits on.
// Most dev/CI machines use ext4/overlay/tmpfs, which don't support
// `--reflink`, so this exercises ExecCopier's real reflink-failed ->
// plain-cp-a fallback path for real (not just via FakeCopier) without
// needing an actual btrfs filesystem. If the machine's temp dir happens to
// be on a reflink-capable filesystem (btrfs/xfs), the reflink attempt
// itself succeeds instead — either way, the observable contract (dest ends
// up with src's content) is what this test verifies.
func TestExecCopierCopiesFileContent(t *testing.T) {
	if _, err := exec.LookPath("cp"); err != nil {
		t.Skip("cp binary not available in this environment")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dest := filepath.Join(dir, "dest.txt")
	want := []byte("restore me")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewExecCopier()
	if err := c.Copy(context.Background(), src, dest); err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got dest content %q, want %q", got, want)
	}
}

// TestExecCopierReturnsErrorWhenBothAttemptsFail uses a source that doesn't
// exist, so both the reflink attempt and the plain-copy fallback fail —
// verifying Copy surfaces a real error rather than swallowing it.
func TestExecCopierReturnsErrorWhenBothAttemptsFail(t *testing.T) {
	if _, err := exec.LookPath("cp"); err != nil {
		t.Skip("cp binary not available in this environment")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "does-not-exist.txt")
	dest := filepath.Join(dir, "dest.txt")

	c := NewExecCopier()
	err := c.Copy(context.Background(), src, dest)
	if err == nil {
		t.Fatal("expected an error when the source doesn't exist")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("dest should not have been created when both copy attempts fail")
	}
}

// TestExecCopierRejectsPreExistingDestinationFile proves the never-overwrite
// guarantee actually holds at the ExecCopier layer against a real `cp`
// binary: a pre-existing destination file is left byte-for-byte untouched,
// and Copy returns ErrRestoreDestinationExists (a retryable error) instead
// of silently reporting success on a skipped copy.
func TestExecCopierRejectsPreExistingDestinationFile(t *testing.T) {
	if _, err := exec.LookPath("cp"); err != nil {
		t.Skip("cp binary not available in this environment")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dest := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("pre-existing content"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewExecCopier()
	err := c.Copy(context.Background(), src, dest)
	if !errors.Is(err, ErrRestoreDestinationExists) {
		t.Fatalf("expected ErrRestoreDestinationExists, got %v", err)
	}

	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("read dest: %v", readErr)
	}
	if string(got) != "pre-existing content" {
		t.Errorf("pre-existing destination was overwritten: got %q", got)
	}
}

// TestExecCopierRejectsPreExistingDestinationDirectory covers the case a
// plain "-n"/"--update=none-fail" cp flag alone does NOT reliably catch:
// when the destination already exists as a directory, GNU cp's default
// behavior is to copy the source INTO it (nesting one level deeper) rather
// than failing, which would silently produce a different layout than the
// caller asked for instead of erroring. ExecCopier's own pre-flight
// existence check (independent of any `cp` flag) must still catch this.
func TestExecCopierRejectsPreExistingDestinationDirectory(t *testing.T) {
	if _, err := exec.LookPath("cp"); err != nil {
		t.Skip("cp binary not available in this environment")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dest := filepath.Join(dir, "destdir")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	c := NewExecCopier()
	err := c.Copy(context.Background(), src, dest)
	if !errors.Is(err, ErrRestoreDestinationExists) {
		t.Fatalf("expected ErrRestoreDestinationExists, got %v", err)
	}

	entries, readErr := os.ReadDir(dest)
	if readErr != nil {
		t.Fatalf("read dest dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("expected pre-existing destination directory to stay empty (no nested copy), got %+v", entries)
	}
}

func TestFakeCopierRecordsReflinkSuccess(t *testing.T) {
	c := NewFakeCopier()
	if err := c.Copy(context.Background(), "/src", "/dest"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Calls) != 1 || !c.Calls[0].Reflink {
		t.Fatalf("expected 1 reflink call, got %+v", c.Calls)
	}
}

func TestFakeCopierFallsBackWhenReflinkErrInjected(t *testing.T) {
	c := NewFakeCopier()
	c.ReflinkErr["/dest"] = errors.New("simulated: invalid cross-device link")

	if err := c.Copy(context.Background(), "/src", "/dest"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Calls) != 1 || c.Calls[0].Reflink {
		t.Fatalf("expected 1 fallback (non-reflink) call, got %+v", c.Calls)
	}
}

func TestFakeCopierReturnsErrorWhenFallbackAlsoFails(t *testing.T) {
	c := NewFakeCopier()
	c.ReflinkErr["/dest"] = errors.New("simulated reflink failure")
	c.FallbackErr["/dest"] = errors.New("simulated fallback failure too")

	if err := c.Copy(context.Background(), "/src", "/dest"); err == nil {
		t.Fatal("expected an error when both reflink and fallback fail")
	}
	if len(c.Calls) != 0 {
		t.Fatalf("expected no recorded successful call, got %+v", c.Calls)
	}
}
