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
//
// Naming matrix (handoff: restore names must keep the OS/UI-recognizable
// extension so double-click-to-open still works):
//
//	input                    isDir   destination
//	file.txt (regular file)  false   file.restored-<ts>.txt         (extension kept, marker inserted before it)
//	file.txt (1st collision) false   file.restored-<ts>-2.txt       (numbering also stays before the extension)
//	somedir                  true    somedir.restored-<ts>          (directories: append, unchanged)
//	.bashrc                  false   .bashrc.restored-<ts>          (dotfile, no other dot: append, unchanged)
//	README (no dot at all)   false   README.restored-<ts>           (extensionless: append, unchanged)
//	设计图.psd (non-ASCII)    false   设计图.restored-<ts>.psd        (extension kept, marker inserted before it)

func TestComputeRestoreDestinationPicksPlainNameWhenNoCollision(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/file.restored-20260713T000000Z.txt"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationAppendsSuffixOnSingleCollision(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/file.restored-20260713T000000Z.txt", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/file.restored-20260713T000000Z-2.txt"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationSkipsMultipleCollisions(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/file.restored-20260713T000000Z.txt", PathInfo{})
	paths.Seed("/live/file.restored-20260713T000000Z-2.txt", PathInfo{})
	paths.Seed("/live/file.restored-20260713T000000Z-3.txt", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/file.restored-20260713T000000Z-4.txt"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationPropagatesExistsError(t *testing.T) {
	paths := NewFakePathChecker()
	paths.ExistsErr["/live/file.restored-20260713T000000Z.txt"] = errors.New("boom")

	if _, err := computeRestoreDestination(paths, "/live", "file.txt", "20260713T000000Z", false, true); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestComputeRestoreDestinationDirectoryKeepsAppendBehavior(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "Projects", "20260713T000000Z", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/Projects.restored-20260713T000000Z"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestComputeRestoreDestinationDirectoryWithDotInNameIsNotSplit proves a
// directory whose name happens to contain a dot (e.g. "archive.tar", a real
// directory, not an archive file) is never mistaken for "a file with an
// extension" — isDir must win over the name shape.
func TestComputeRestoreDestinationDirectoryWithDotInNameIsNotSplit(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "archive.tar", "20260713T000000Z", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/archive.tar.restored-20260713T000000Z"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationDotfileKeepsAppendBehavior(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", ".bashrc", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/.bashrc.restored-20260713T000000Z"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationExtensionlessKeepsAppendBehavior(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "README", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/README.restored-20260713T000000Z"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestComputeRestoreDestinationNonASCIIStemKeepsExtension proves the
// before-the-extension insertion works correctly for a non-ASCII (CJK) stem,
// not just ASCII names.
func TestComputeRestoreDestinationNonASCIIStemKeepsExtension(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "设计图.psd", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/设计图.restored-20260713T000000Z.psd"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestComputeRestoreDestinationMultiPartExtensionSplitsOnLastDotOnly proves
// the accepted tradeoff for compound extensions like ".tar.gz": only the
// final segment after the LAST dot is treated as "the extension".
func TestComputeRestoreDestinationMultiPartExtensionSplitsOnLastDotOnly(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "archive.tar.gz", "20260713T000000Z", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/archive.tar.restored-20260713T000000Z.gz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- computeRestoreDestination, withMarker=false (naming matrix) ---
//
//	input                    isDir   destination (no collision)   destination (1st collision)
//	report.docx (file)       false   report.docx                  report-2.docx
//	Projects (dir)           true    Projects                     Projects-2
//	.bashrc (dotfile)        false   .bashrc                       .bashrc-2
//	archive.tar.gz (file)    false   archive.tar.gz                archive.tar-2.gz

func TestComputeRestoreDestinationWithoutMarkerPicksOriginalNameWhenNoCollision(t *testing.T) {
	paths := NewFakePathChecker()
	got, err := computeRestoreDestination(paths, "/live", "report.docx", "20260713T000000Z", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/report.docx"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationWithoutMarkerNumbersFileOnCollisionBeforeExtension(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/report.docx", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "report.docx", "20260713T000000Z", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/report-2.docx"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationWithoutMarkerSkipsMultipleCollisions(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/report.docx", PathInfo{})
	paths.Seed("/live/report-2.docx", PathInfo{})
	paths.Seed("/live/report-3.docx", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "report.docx", "20260713T000000Z", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/report-4.docx"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationWithoutMarkerDirectoryNumbersOnCollision(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/Projects", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "Projects", "20260713T000000Z", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/Projects-2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeRestoreDestinationWithoutMarkerDotfileNumbersOnCollision(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/.bashrc", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", ".bashrc", "20260713T000000Z", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/.bashrc-2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestComputeRestoreDestinationWithoutMarkerKeepsCompoundExtensionOnCollision
// proves the extension-preservation rule (splitRestoreExt) applies to the
// bare numeric suffix too, not just the ".restored-<ts>" marker.
func TestComputeRestoreDestinationWithoutMarkerKeepsCompoundExtensionOnCollision(t *testing.T) {
	paths := NewFakePathChecker()
	paths.Seed("/live/archive.tar.gz", PathInfo{})

	got, err := computeRestoreDestination(paths, "/live", "archive.tar.gz", "20260713T000000Z", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/live/archive.tar-2.gz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
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

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z.docx")
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

// TestRestoreDirectoryKeepsAppendNaming proves Restore's Service-level
// integration actually threads the source's real IsDir (from stat'ing
// inside the snapshot) through to computeRestoreDestination — a restored
// directory must keep the append-style name ("Projects.restored-<ts>"),
// never the before-the-extension rewrite regular files with a dot in their
// name get.
func TestRestoreDirectoryKeepsAppendNaming(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.MkdirAll(filepath.Join(f.snapDir, "Projects", "2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.snapDir, "Projects", "2026", "design.psd"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "Projects", RestoreOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "Projects.restored-20260713T120000Z")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
}

// TestRestoreDotfileKeepsAppendNaming proves a dotfile (leading dot, no
// other dot in the name) is restored with the append-style name, not
// split as if ".bashrc" itself were an extension.
func TestRestoreDotfileKeepsAppendNaming(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, ".bashrc"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, ".bashrc", RestoreOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, ".bashrc.restored-20260713T120000Z")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
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

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "Projects/2026/design.psd", RestoreOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDir := filepath.Join(f.volume.MountPoint, "Projects", "2026")
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("expected %s to have been created as a directory: %v", wantDir, err)
	}
	wantPath := filepath.Join(wantDir, "design.restored-20260713T120000Z.psd")
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
	collision1 := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z.docx")
	collision2 := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z-2.docx")
	if err := os.WriteFile(collision1, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collision2, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z-3.docx")
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
	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "/etc/passwd", RestoreOptions{})
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

func TestRestoreRejectsDotDotTraversal(t *testing.T) {
	f := newRestoreFixture(t)
	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "../../../etc/passwd", RestoreOptions{})
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

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "escape/secret.txt", RestoreOptions{})
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

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "nested/secret.txt", RestoreOptions{})
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
	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, ".", RestoreOptions{})
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
	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "missing.txt", RestoreOptions{})
	if !errors.Is(err, ErrRestoreSourceNotFound) {
		t.Fatalf("expected ErrRestoreSourceNotFound, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for a missing source, got %+v", f.copier.Calls)
	}
}

func TestRestoreUnknownSnapshotNameIsRejected(t *testing.T) {
	f := newRestoreFixture(t)
	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, "not-one-of-ours", "report.docx", RestoreOptions{})
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName, got %v", err)
	}
}

func TestRestoreSnapshotNotOnDiskIsRejected(t *testing.T) {
	f := newRestoreFixture(t)
	// A syntactically valid name that this fixture never seeded on disk.
	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, "20260101T000000Z_manual_ghost", "report.docx", RestoreOptions{})
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
	dest := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z.docx")
	f.copier.ReflinkErr[dest] = errors.New("simulated: operation not supported")

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{})
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
	dest := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z.docx")
	f.copier.ReflinkErr[dest] = ErrRestoreDestinationExists
	f.copier.FallbackErr[dest] = ErrRestoreDestinationExists

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{})
	if !errors.Is(err, ErrRestoreDestinationExists) {
		t.Fatalf("expected ErrRestoreDestinationExists, got %v", err)
	}
}

func TestRestorePropagatesCopyFailureWhenBothAttemptsFail(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.volume.MountPoint, "report.restored-20260713T120000Z.docx")
	f.copier.ReflinkErr[dest] = errors.New("simulated reflink failure")
	f.copier.FallbackErr[dest] = errors.New("simulated fallback failure too")

	if _, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{}); err == nil {
		t.Fatal("expected an error when both copy attempts fail")
	}
}

// --- Service.Restore: with_marker=false ---

func boolPtr(b bool) *bool { return &b }

// TestRestoreWithMarkerFalseUsesOriginalName proves with_marker=false lands
// the restore back at exactly relPath's original path (no ".restored-<ts>"
// anywhere) when nothing already occupies it — the common case, since the
// live file/directory being restored was itself removed from that same
// location (that's the whole reason a restore is needed).
func TestRestoreWithMarkerFalseUsesOriginalName(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{WithMarker: boolPtr(false)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "report.docx")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
	if f.copier.Calls[0].Dest != want {
		t.Errorf("got copy dest %q, want %q", f.copier.Calls[0].Dest, want)
	}
}

// TestRestoreWithMarkerFalseNumbersOnCollisionKeepingExtension proves the
// never-overwrite guarantee still holds with with_marker=false: a
// pre-existing "report.docx" at the original location forces a bare "-2"
// suffix inserted before the extension, not a silent overwrite.
func TestRestoreWithMarkerFalseNumbersOnCollisionKeepingExtension(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	preExisting := filepath.Join(f.volume.MountPoint, "report.docx")
	if err := os.WriteFile(preExisting, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{WithMarker: boolPtr(false)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "report-2.docx")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
	content, err := os.ReadFile(preExisting)
	if err != nil {
		t.Fatalf("pre-existing file missing: %v", err)
	}
	if string(content) != "pre-existing" {
		t.Errorf("pre-existing file was overwritten: %q", content)
	}
}

// TestRestoreWithMarkerFalseDirectoryNumbersOnCollision mirrors the file
// case above for a directory, proving the append-style (not
// before-the-extension) placement still applies with the marker off.
func TestRestoreWithMarkerFalseDirectoryNumbersOnCollision(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.MkdirAll(filepath.Join(f.snapDir, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.volume.MountPoint, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "Projects", RestoreOptions{WithMarker: boolPtr(false)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(f.volume.MountPoint, "Projects-2")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
}

// --- Service.Restore: dest_dir ---

func TestRestoreDestDirOverridesDefaultLocation(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(f.volume.MountPoint, "chosen-destination")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx", RestoreOptions{DestDir: destDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(destDir, "report.restored-20260713T120000Z.docx")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
}

// TestRestoreDestDirCombinedWithMarkerFalse proves dest_dir and with_marker
// compose: the file lands under the chosen directory, AND under its
// original (unmarked) name.
func TestRestoreDestDirCombinedWithMarkerFalse(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(f.volume.MountPoint, "chosen-destination")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: destDir, WithMarker: boolPtr(false)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(destDir, "report.docx")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
}

// TestRestoreDestDirCombinedWithMarkerFalseNumbersOnCollision proves the
// never-overwrite guarantee holds in a caller-chosen dest_dir too.
func TestRestoreDestDirCombinedWithMarkerFalseNumbersOnCollision(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(f.volume.MountPoint, "chosen-destination")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	preExisting := filepath.Join(destDir, "report.docx")
	if err := os.WriteFile(preExisting, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: destDir, WithMarker: boolPtr(false)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(destDir, "report-2.docx")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
	content, err := os.ReadFile(preExisting)
	if err != nil {
		t.Fatalf("pre-existing file missing: %v", err)
	}
	if string(content) != "pre-existing" {
		t.Errorf("pre-existing file was overwritten: %q", content)
	}
}

func TestRestoreDestDirRejectsRelativePath(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: "relative/chosen-destination"})
	if !errors.Is(err, ErrInvalidRestoreDestDir) {
		t.Fatalf("expected ErrInvalidRestoreDestDir, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

// TestRestoreDestDirRejectsPathNotUnderAnyVolume proves a dest_dir outside
// every known volume's mount point (e.g. some other real directory on the
// host) is rejected, even though it's an absolute, existing directory.
func TestRestoreDestDirRejectsPathNotUnderAnyVolume(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: outside})
	if !errors.Is(err, ErrInvalidRestoreDestDir) {
		t.Fatalf("expected ErrInvalidRestoreDestDir, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

// TestRestoreDestDirRejectsNonexistentDirectory proves dest_dir is never
// auto-created: a syntactically valid path under a known volume, that
// simply doesn't exist yet, is rejected with a clear error rather than
// silently mkdir-ing it.
func TestRestoreDestDirRejectsNonexistentDirectory(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(f.volume.MountPoint, "does-not-exist-yet")

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: missing})
	if !errors.Is(err, ErrInvalidRestoreDestDir) {
		t.Fatalf("expected ErrInvalidRestoreDestDir, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("expected dest_dir to remain uncreated, got stat error %v", statErr)
	}
}

// TestRestoreDestDirRejectsSymlinkEscape proves dest_dir's own symlink
// defense (resolveRestoreDestDir's use of resolveExistingPrefix) is actually
// reached: a dest_dir that resolves, via a symlink, to somewhere outside its
// claimed volume's real mount point is rejected.
func TestRestoreDestDirRejectsSymlinkEscape(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	escapeLink := filepath.Join(f.volume.MountPoint, "escape-dir")
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: escapeLink})
	if !errors.Is(err, ErrInvalidRestoreDestDir) {
		t.Fatalf("expected ErrInvalidRestoreDestDir, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

// TestRestoreDestDirRejectsUnmountedVolume proves dest_dir must resolve to a
// volume that is CURRENTLY mounted, not merely one this service knows about
// (mirroring ResolveVolume's own mount check on the source-volume side).
func TestRestoreDestDirRejectsUnmountedVolume(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherMount := t.TempDir()
	other := VolumeInfo{UUID: "vol-2", DevicePath: "/dev/md1", MountPoint: otherMount, Filesystem: "btrfs"}
	// Deliberately NOT seeded as mounted on the fake runner.

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume, other}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: otherMount})
	if !errors.Is(err, ErrInvalidRestoreDestDir) {
		t.Fatalf("expected ErrInvalidRestoreDestDir, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

// TestRestoreDestDirRejectsNonBtrfsVolume proves a dest_dir under a known,
// currently mounted volume that isn't btrfs (not "snapshot-supported") is
// still rejected — being mounted alone isn't enough.
func TestRestoreDestDirRejectsNonBtrfsVolume(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherMount := t.TempDir()
	other := VolumeInfo{UUID: "vol-2", DevicePath: "/dev/md1", MountPoint: otherMount, Filesystem: "ext4"}
	runner := f.svc.Runner.(*FakeRunner)
	runner.SeedMounted(other.DevicePath, other.MountPoint)

	_, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume, other}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: otherMount})
	if !errors.Is(err, ErrInvalidRestoreDestDir) {
		t.Fatalf("expected ErrInvalidRestoreDestDir, got %v", err)
	}
	if len(f.copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", f.copier.Calls)
	}
}

// TestRestoreDestDirAllowsCrossVolumeRestore proves a dest_dir on a
// DIFFERENT (also mounted, also btrfs) volume than the restore's source is
// accepted — cross-volume restore is allowed by design, since Copier's
// reflink-then-plain-copy fallback already handles a destination on a
// different filesystem.
func TestRestoreDestDirAllowsCrossVolumeRestore(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherMount := t.TempDir()
	other := VolumeInfo{UUID: "vol-2", DevicePath: "/dev/md1", MountPoint: otherMount, Filesystem: "btrfs"}
	runner := f.svc.Runner.(*FakeRunner)
	runner.SeedMounted(other.DevicePath, other.MountPoint)

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume, other}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: otherMount})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(otherMount, "report.restored-20260713T120000Z.docx")
	if result.RestoredPath != want {
		t.Errorf("got restored path %q, want %q", result.RestoredPath, want)
	}
	if len(f.copier.Calls) != 1 || !f.copier.Calls[0].Reflink {
		t.Fatalf("expected 1 reflink copy call, got %+v", f.copier.Calls)
	}
}

// TestRestoreDestDirCrossVolumeFallsBackOnReflinkFailure proves the
// cross-volume dest_dir path composes with Copier's existing
// reflink-failure fallback (copy.go): a cross-device destination is exactly
// the case reflink is expected to fail for in practice, and Restore must
// still succeed via the plain-copy fallback rather than surfacing the
// reflink error.
func TestRestoreDestDirCrossVolumeFallsBackOnReflinkFailure(t *testing.T) {
	f := newRestoreFixture(t)
	if err := os.WriteFile(filepath.Join(f.snapDir, "report.docx"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherMount := t.TempDir()
	other := VolumeInfo{UUID: "vol-2", DevicePath: "/dev/md1", MountPoint: otherMount, Filesystem: "btrfs"}
	runner := f.svc.Runner.(*FakeRunner)
	runner.SeedMounted(other.DevicePath, other.MountPoint)

	dest := filepath.Join(otherMount, "report.restored-20260713T120000Z.docx")
	f.copier.ReflinkErr[dest] = errors.New("simulated: invalid cross-device link")

	result, err := f.svc.Restore(context.Background(), []VolumeInfo{f.volume, other}, f.volume, f.snapName, "report.docx",
		RestoreOptions{DestDir: otherMount})
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
