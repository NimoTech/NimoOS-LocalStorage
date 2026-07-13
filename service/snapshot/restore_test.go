package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- computeRestoreDestination (pure numbering logic, FakePathChecker) ---

func TestComputeRestoreDestinationPicksPlainNameWhenNoCollision(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/file.txt.restored-20260713T000000Z"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationAppendsSuffixOnSingleCollision(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/file.txt.restored-20260713T000000Z", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/file.txt.restored-20260713T000000Z-2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationSkipsMultipleCollisions(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/file.txt.restored-20260713T000000Z", PathInfo{})
	paths.Seed("/live/file.txt.restored-20260713T000000Z-2", PathInfo{})
	paths.Seed("/live/file.txt.restored-20260713T000000Z-3", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/file.txt.restored-20260713T000000Z-4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationPropagatesExistsError(t *testing.T) {
	paths := NewFakePathChecker()
	paths.ExistsErr["/live/file.txt.restored-20260713T000000Z"] = errors.New("boom")

	if _, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z"); err == nil {
		t.Fatal("expected error to propagate")
	}
}

// --- Service.Restore (real temp-dir filesystem, FakeRunner/FakeStore/FakeCopier) ---

// restoreFixture wires a Service against a real temp-dir volume (mirroring
// route/snapshot_test.go's btrfsVolume helper) with a snapshot subdirectory
// physically present on disk (so resolveWithinDir's real symlink-aware path
// resolution operates on genuine directories, not a simulated model of
// one), a FakeRunner/FakeStore recording the snapshot as existing, and a
// FakeCopier + FakeClock so the destination path is fully deterministic.
type restoreFixture struct {
	svc      *Service
	volume   VolumeInfo
	snapName string
	snapDir  string
	copier   *FakeCopier
}

func newRestoreFixture(t *testing.T) *restoreFixture {
	t.Helper()
	mountPoint := t.TempDir()
	volume := VolumeInfo{UUID: "vol-1", DevicePath: "/dev/md0", MountPoint: mountPoint, Filesystem: "btrfs"}

	snapName := "20260712T030000Z_manual_before-move"
	snapDir := filepath.Join(mountPoint, SnapshotsMountSubdir, snapName)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := NewFakeRunner()
	runner.SeedMounted(volume.DevicePath, volume.MountPoint)
	runner.ListResult[volume.MountPoint] = []SubvolumeEntry{
		{ID: "300", Path: SnapshotsSubvolumeName + "/" + snapName},
	}
	store := NewFakeStore()
	copier := NewFakeCopier()

	svc := &Service{
		Runner:    runner,
		Store:     store,
		Persister: NewFakeFstabPersister(),
		Pause:     NewPauseState(),
		Paths:     OSPathChecker{},
		Copier:    copier,
		Clock:     NewFakeClock(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)),
	}

	return &restoreFixture{svc: svc, volume: volume, snapName: snapName, snapDir: snapDir, copier: copier}
}

func TestRestoreCopiesFileFromSnapshotToLiveVolume(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "report.docx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
	if len(f.copier.Calls) != 1 {
		t.Fatalf("expected 1 copy call, got %+v", f.copier.Calls)
	}
	if f.copier.Calls[0].Src != filepath.Join(f.snapDir, "report.docx") {
		t.Errorf("got copy src %q, want %q", f.copier.Calls[0].Src, filepath.Join(f.snapDir, "report.docx"))
	}
	if f.copier.Calls[0].Dest != want {
		t.Errorf("got copy dest %q, want %q", f.copier.Calls[0].Dest, want)
	}
}

func TestRestoreCreatesMissingParentDirectoriesInLiveVolume(t *testing.T) {
	f := newRestoreFixture(t)
	nested := filepath.Join(f.snapDir, "Projects", "2026", "design.psd")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The live volume's "Projects/2026" directory does NOT exist — this
	// models the actual incident this feature exists for (source AND
	// destination directories both gone).

	result, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "Projects/2026/design.psd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDir := filepath.Join(f.volume.MountPoint, "Projects", "2026")
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("expected %s to have been created as a directory: %v", wantDir, err)
	}
	wantPath := filepath.Join(wantDir, "design.psd.restored-20260713T120000Z")
	if result.RestoredPath != wantPath {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, wantPath)
	}
}

func TestRestoreNeverOverwritesExistingDestinationAndNumbers(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-create the exact destination Restore would otherwise pick, plus
	// its first numbered successor, so it must skip straight to -3.
	collision1 := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z")
	collision2 := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z-2")
	if err := os.WriteFile(collision1, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collision2, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "report.docx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z-3")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
	// The pre-existing files must be untouched.
	for _, p := range []string{collision1, collision2} {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("pre-existing file %s missing: %v", p, err)
		}
		if string(content) != "pre-existing" {
			t.Errorf("pre-existing file %s was overwritten: %q", p, content)
		}
	}
}

func TestRestoreRejectsAbsolutePath(t *testing.T) {
	f := newRestoreFixture(t)
	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "/etc/passwd")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

func TestRestoreRejectsDotDotTraversal(t *testing.T) {
	f := newRestoreFixture(t)
	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "../../../etc/passwd")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

// TestRestoreRejectsSymlinkEscapeInSnapshot proves resolveWithinDir's real
// symlink-aware defense is actually reached from Restore's real code path,
// not just exercised in isolation (restore_path_test.go).
func TestRestoreRejectsSymlinkEscapeInSnapshot(t *testing.T) {
	f := newRestoreFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.snapDir, "escape")); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "escape/secret.txt")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath for symlink escape, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for a symlink escape, got %+v", f.copier.Calls)
	}
}

// TestRestoreRejectsSymlinkEscapeInLiveVolume is the mirror image of
// TestRestoreRejectsSymlinkEscapeInSnapshot: every other symlink-escape test
// in this package places the malicious link on the snapshot/base side. This
// one places it on the LIVE VOLUME side instead — destParentRel's own path
// resolution (restore.go's second resolveWithinDir call, against liveBase)
// must independently reject it, closing that test gap.
func TestRestoreRejectsSymlinkEscapeInLiveVolume(t *testing.T) {
	f := newRestoreFixture(t)
	// The source exists inside the snapshot, so Restore gets past the
	// source-exists check and actually reaches the live-volume-side path
	// validation this test is targeting.
	if err := os.MkdirAll(filepath.Join(f.snapDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.snapDir, "nested", "secret.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// In the LIVE volume (not the snapshot), "nested" is a symlink escaping
	// outside the mount point.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.volume.MountPoint, "nested")); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "nested/secret.txt")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath for a live-volume symlink escape, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for a live-volume symlink escape, got %+v", f.copier.Calls)
	}
}

// TestRestoreRejectsPathDot proves relPath="." (meaning "restore the entire
// volume root") is rejected outright rather than producing the degenerate
// "..restored-<ts>" destination (filepath.Base(".") == ".") that would copy
// the whole snapshot into a folder inside its own live volume root.
func TestRestoreRejectsPathDot(t *testing.T) {
	f := newRestoreFixture(t)
	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, ".")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath for path \".\", got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for path \".\", got %+v", f.copier.Calls)
	}
}

func TestRestoreSourceNotFoundInsideSnapshot(t *testing.T) {
	f := newRestoreFixture(t)
	// snapDir exists (it's a real directory) but "missing.txt" was never
	// written into it.
	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "missing.txt")
	if !errors.Is(err, ErrRestoreSourceNotFound) {
		t.Fatalf("expected ErrRestoreSourceNotFound, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for a missing source, got %+v", f.copier.Calls)
	}
}

func TestRestoreUnknownSnapshotNameIsRejected(t *testing.T) {
	f := newRestoreFixture(t)
	_, err := f.svc.Restore(context.Background(), f.volume, "not-one-of-ours", "report.docx")
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName, got %v", err)
	}
}

func TestRestoreSnapshotNotOnDiskIsRejected(t *testing.T) {
	f := newRestoreFixture(t)
	// A syntactically valid name that this fixture never seeded on disk.
	_, err := f.svc.Restore(context.Background(), f.volume, "20260101T000000Z_manual_ghost", "report.docx")
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expected ErrSnapshotNotFound, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for a nonexistent snapshot, got %+v", f.copier.Calls)
	}
}

// TestRestoreFallsBackWhenReflinkFails proves Restore's copy step actually
// takes FakeCopier's injected reflink-failure fallback path — i.e. the
// service layer doesn't special-case reflink failures itself, it just
// trusts Copier to do the right thing (handoff §3.4: "reflink 失败...回退
// 普通 cp -a").
func TestRestoreFallsBackWhenReflinkFails(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z")
	f.copier.ReflinkErr[dest] = errors.New("simulated: operation not supported")

	result, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "report.docx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RestoredPath != dest {
		t.Fatalf("got restored path %q, want %q", result.RestoredPath, dest)
	}
	if len(f.copier.Calls) != 1 || f.copier.Calls[0].Reflink {
		t.Fatalf("expected 1 fallback (non-reflink) call, got %+v", f.copier.Calls)
	}
}

// TestRestorePropagatesDestinationExistsErrorFromCopier proves Restore
// surfaces ErrRestoreDestinationExists (rather than reporting success, or an
// opaque error) when the Copier reports the destination collided — the
// retryable outcome of the TOCTOU race between computeRestoreDestination's
// Exists check and Copy's actual write (fixed in copy.go: ExecCopier now
// detects this itself against the real filesystem; here we simulate the
// same signal through FakeCopier to prove the service layer's contract).
func TestRestorePropagatesDestinationExistsErrorFromCopier(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z")
	f.copier.ReflinkErr[dest] = ErrRestoreDestinationExists
	f.copier.FallbackErr[dest] = ErrRestoreDestinationExists

	_, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "report.docx")
	if !errors.Is(err, ErrRestoreDestinationExists) {
		t.Fatalf("expected ErrRestoreDestinationExists, got %v", err)
	}
}

func TestRestorePropagatesCopyFailureWhenBothAttemptsFail(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.volume.MountPoint, "report.docx.restored-20260713T120000Z")
	f.copier.ReflinkErr[dest] = errors.New("simulated reflink failure")
	f.copier.FallbackErr[dest] = errors.New("simulated fallback failure too")

	if _, err := f.svc.Restore(context.Background(), f.volume, f.snapName, "report.docx"); err == nil {
		t.Fatal("expected an error when both copy attempts fail")
	}
}
