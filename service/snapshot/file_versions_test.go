package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// fileVersionsFixture wires a Service against a real temp-dir volume with N
// real snapshot subdirectories on disk (mirroring restoreFixture), each
// optionally containing "report.docx" with distinguishable content/mtime,
// so FileVersions' size/mtime reporting and its "some snapshots don't have
// this file" skip logic exercise genuine os.Stat results.
type fileVersionsFixture struct {
	svc    *Service
	volume VolumeInfo
}

func newFileVersionsFixture(t *testing.T) *fileVersionsFixture {
	t.Helper()
	mountPoint := t.TempDir()
	volume := VolumeInfo{UUID: "vol-1", DevicePath: "/dev/md0", MountPoint: mountPoint, Filesystem: "btrfs"}

	runner := NewFakeRunner()
	runner.SeedMounted(volume.DevicePath, volume.MountPoint)
	store := NewFakeStore()

	svc := &Service{
		Runner:    runner,
		Store:     store,
		Persister: NewFakeFstabPersister(),
		Pause:     NewPauseState(),
		Paths:     OSPathChecker{},
		Copier:    NewFakeCopier(),
		Clock:     NewFakeClock(time.Now()),
	}
	return &fileVersionsFixture{svc: svc, volume: volume}
}

// addSnapshot creates snapshot name on disk (as a real directory under
// .snapshots), registers it with the fake runner's ListSubvolumes result
// (so reconciliation picks it up) and inserts its DB record with the given
// createdAt. If withFile, "report.docx" is written inside it with content
// (used to derive a distinct size).
func (f *fileVersionsFixture) addSnapshot(t *testing.T, name string, createdAt time.Time, withFile bool, content string) {
	t.Helper()
	dir := filepath.Join(f.volume.MountPoint, SnapshotsMountSubdir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if withFile {
		if err := os.WriteFile(filepath.Join(dir, "report.docx"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runner := f.svc.Runner.(*FakeRunner)
	runner.ListResult[f.volume.MountPoint] = append(
		runner.ListResult[f.volume.MountPoint],
		SubvolumeEntry{Path: SnapshotsSubvolumeName + "/" + name},
	)

	if err := f.svc.Store.InsertSnapshot(model.Snapshot{
		VolumeUUID: f.volume.UUID,
		Name:       name,
		Type:       TypeManual,
		CreatedAt:  createdAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFileVersionsSkipsSnapshotsWithoutThePath(t *testing.T) {
	f := newFileVersionsFixture(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f.addSnapshot(t, "20260701T000000Z_manual_a", base, true, "aaaa")
	f.addSnapshot(t, "20260702T000000Z_manual_b", base.AddDate(0, 0, 1), false, "")
	f.addSnapshot(t, "20260703T000000Z_manual_c", base.AddDate(0, 0, 2), true, "cc")

	versions, err := f.svc.FileVersions(context.Background(), f.volume, "report.docx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions (snapshot without the file omitted), got %+v", versions)
	}
	// Newest first.
	if versions[0].Snapshot != "20260703T000000Z_manual_c" || versions[1].Snapshot != "20260701T000000Z_manual_a" {
		t.Fatalf("unexpected order: %+v", versions)
	}
	if versions[0].Size != 2 {
		t.Errorf("got size %d for newest version, want 2", versions[0].Size)
	}
	if versions[1].Size != 4 {
		t.Errorf("got size %d for oldest version, want 4", versions[1].Size)
	}
}

// TestFileVersionsCapsAtMostRecentSixty is the explicit handoff §3.4
// requirement: "cap the traversal count (most recent 60 snapshots)". This seeds 65 snapshots (all
// containing the file) and verifies only the 60 most recent are returned —
// i.e. the 5 oldest are never even stat'd, let alone included.
func TestFileVersionsCapsAtMostRecentSixty(t *testing.T) {
	f := newFileVersionsFixture(t)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const total = 65
	for i := 0; i < total; i++ {
		name := time.Date(2020, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(nameTimeLayout) + "_manual_v"
		f.addSnapshot(t, name, base.AddDate(0, 0, i), true, "x")
	}

	versions, err := f.svc.FileVersions(context.Background(), f.volume, "report.docx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != MaxFileVersionsSnapshots {
		t.Fatalf("got %d versions, want %d (capped)", len(versions), MaxFileVersionsSnapshots)
	}

	// The newest entry must be the very last snapshot created (index 64);
	// the oldest returned entry must be exactly 60 snapshots back from it
	// (index 5) — i.e. the cap keeps the most recent, not an arbitrary
	// slice.
	wantNewest := time.Date(2020, 1, 1+total-1, 0, 0, 0, 0, time.UTC).Format(nameTimeLayout) + "_manual_v"
	wantOldestKept := time.Date(2020, 1, 1+total-MaxFileVersionsSnapshots, 0, 0, 0, 0, time.UTC).Format(nameTimeLayout) + "_manual_v"
	if versions[0].Snapshot != wantNewest {
		t.Errorf("got newest %q, want %q", versions[0].Snapshot, wantNewest)
	}
	if versions[len(versions)-1].Snapshot != wantOldestKept {
		t.Errorf("got oldest kept %q, want %q", versions[len(versions)-1].Snapshot, wantOldestKept)
	}
}

func TestFileVersionsRejectsPathTraversal(t *testing.T) {
	f := newFileVersionsFixture(t)
	f.addSnapshot(t, "20260701T000000Z_manual_a", time.Now(), true, "x")

	_, err := f.svc.FileVersions(context.Background(), f.volume, "../../../etc/passwd")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath, got %v", err)
	}
}

func TestFileVersionsRejectsSymlinkEscape(t *testing.T) {
	f := newFileVersionsFixture(t)
	f.addSnapshot(t, "20260701T000000Z_manual_a", time.Now(), false, "")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapDir := filepath.Join(f.volume.MountPoint, SnapshotsMountSubdir, "20260701T000000Z_manual_a")
	if err := os.Symlink(outside, filepath.Join(snapDir, "escape")); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.FileVersions(context.Background(), f.volume, "escape/secret.txt")
	if !errors.Is(err, ErrInvalidRestorePath) {
		t.Fatalf("expected ErrInvalidRestorePath for symlink escape, got %v", err)
	}
}

func TestFileVersionsReturnsEmptyWhenNoSnapshotsHaveThePath(t *testing.T) {
	f := newFileVersionsFixture(t)
	f.addSnapshot(t, "20260701T000000Z_manual_a", time.Now(), false, "")

	versions, err := f.svc.FileVersions(context.Background(), f.volume, "report.docx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected 0 versions, got %+v", versions)
	}
}
