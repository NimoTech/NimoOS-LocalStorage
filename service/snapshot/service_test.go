package snapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

func newTestService(t *testing.T) (*Service, *FakeRunner, *FakeStore) {
	t.Helper()
	runner := NewFakeRunner()
	store := NewFakeStore()
	svc := &Service{Runner: runner, Store: store, Persister: NewFakeFstabPersister()}
	return svc, runner, store
}

func serviceTestVolume(t *testing.T) VolumeInfo {
	t.Helper()
	return VolumeInfo{
		UUID:       "vol-uuid-1",
		DevicePath: "/dev/md0",
		MountPoint: t.TempDir(),
		Filesystem: "btrfs",
	}
}

func TestResolveVolumeNotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.ResolveVolume(nil, "missing")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestResolveVolumeRejectsNonBtrfs(t *testing.T) {
	svc, _, _ := newTestService(t)
	vol := serviceTestVolume(t)
	vol.Filesystem = "ext4"
	_, err := svc.ResolveVolume([]VolumeInfo{vol}, vol.UUID)
	if !errors.Is(err, ErrVolumeNotBtrfs) {
		t.Fatalf("expected ErrVolumeNotBtrfs, got %v", err)
	}
}

func TestResolveVolumeRejectsUnmounted(t *testing.T) {
	svc, _, _ := newTestService(t)
	vol := serviceTestVolume(t)
	// Not seeded as mounted in the fake runner.
	_, err := svc.ResolveVolume([]VolumeInfo{vol}, vol.UUID)
	if !errors.Is(err, ErrVolumeNotMounted) {
		t.Fatalf("expected ErrVolumeNotMounted, got %v", err)
	}
}

func TestResolveVolumeSucceedsWhenMountedBtrfs(t *testing.T) {
	svc, runner, _ := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)

	got, err := svc.ResolveVolume([]VolumeInfo{vol}, vol.UUID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UUID != vol.UUID {
		t.Errorf("got volume %+v, want %+v", got, vol)
	}
}

// ResolveVolumeIdentity is the lighter counterpart used by operations that
// must work even when a known volume is temporarily offline (RAID member
// unplugged, cold-boot enumeration race) — see B2 Fix Round 1: a pure DB
// read (GET policy) or disabling an already-saved policy (PUT
// enabled:false, matching SavePolicy's own branching) has no business
// requiring the volume to be mounted right now.

func TestResolveVolumeIdentityNotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.ResolveVolumeIdentity(nil, "missing")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestResolveVolumeIdentityRejectsNonBtrfs(t *testing.T) {
	svc, _, _ := newTestService(t)
	vol := serviceTestVolume(t)
	vol.Filesystem = "ext4"
	_, err := svc.ResolveVolumeIdentity([]VolumeInfo{vol}, vol.UUID)
	if !errors.Is(err, ErrVolumeNotBtrfs) {
		t.Fatalf("expected ErrVolumeNotBtrfs, got %v", err)
	}
}

func TestResolveVolumeIdentitySucceedsWhenUnmounted(t *testing.T) {
	svc, _, _ := newTestService(t)
	vol := serviceTestVolume(t)
	// Deliberately not seeded as mounted in the fake runner.
	got, err := svc.ResolveVolumeIdentity([]VolumeInfo{vol}, vol.UUID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UUID != vol.UUID {
		t.Errorf("got volume %+v, want %+v", got, vol)
	}
}

func TestCreateManualRejectsUnsupportedVolume(t *testing.T) {
	svc, _, _ := newTestService(t)
	vol := serviceTestVolume(t)
	vol.Filesystem = "ext4"

	_, err := svc.CreateManual(context.Background(), vol, "", "1")
	if err == nil {
		t.Fatal("expected error for non-btrfs volume")
	}
	if !errors.Is(err, ErrVolumeNotSupported) {
		t.Fatalf("expected ErrVolumeNotSupported, got %v", err)
	}
}

func TestCreateManualCreatesSnapshotAndRecord(t *testing.T) {
	svc, runner, store := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.SeedTopLevelSubvolume(vol.DevicePath, "@snapshots")

	rec, err := svc.CreateManual(context.Background(), vol, "before-move", "42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != TypeManual {
		t.Errorf("got type %q, want %q", rec.Type, TypeManual)
	}
	if rec.Label != "before-move" {
		t.Errorf("got label %q, want %q", rec.Label, "before-move")
	}
	if rec.CreatedBy != "42" {
		t.Errorf("got created_by %q, want %q", rec.CreatedBy, "42")
	}
	if len(runner.CreatedSnapshots) != 1 {
		t.Fatalf("expected 1 CreateReadOnlySnapshot call, got %d", len(runner.CreatedSnapshots))
	}
	if runner.CreatedSnapshots[0].SourcePath != vol.MountPoint {
		t.Errorf("got source path %q, want %q", runner.CreatedSnapshots[0].SourcePath, vol.MountPoint)
	}

	stored, err := store.ListSnapshots(vol.UUID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stored) != 1 || stored[0].Name != rec.Name {
		t.Fatalf("expected the created snapshot to be persisted, got %+v", stored)
	}
}

func TestDeleteByNameRejectsInvalidName(t *testing.T) {
	svc, runner, _ := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)

	err := svc.DeleteByName(context.Background(), vol, "../../../etc/passwd")
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName, got %v", err)
	}
	if len(runner.DeletedPaths) != 0 {
		t.Fatalf("expected no disk delete for an invalid name, got %v", runner.DeletedPaths)
	}
}

func TestDeleteByNameReturnsNotFoundWhenAbsentFromDisk(t *testing.T) {
	svc, runner, _ := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	// No subvolumes seeded under @snapshots on disk.

	err := svc.DeleteByName(context.Background(), vol, "20260712T030000Z_manual_x")
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expected ErrSnapshotNotFound, got %v", err)
	}
}

func TestDeleteByNameDeletesDiskAndDBRecord(t *testing.T) {
	svc, runner, store := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)

	name := "20260712T030000Z_manual_x"
	runner.ListResult[vol.MountPoint] = []SubvolumeEntry{{ID: "300", Path: "@snapshots/" + name}}

	if err := svc.DeleteByName(context.Background(), vol, name); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := SnapshotsDir(vol) + "/" + name
	if len(runner.DeletedPaths) != 1 || runner.DeletedPaths[0] != wantPath {
		t.Fatalf("got deleted paths %v, want [%s]", runner.DeletedPaths, wantPath)
	}

	remaining, _ := store.ListSnapshots(vol.UUID)
	if len(remaining) != 0 {
		t.Fatalf("expected the DB record to be removed too, got %+v", remaining)
	}
}

func TestListSnapshotsReconcilesFromDisk(t *testing.T) {
	svc, runner, store := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.ListResult[vol.MountPoint] = []SubvolumeEntry{
		{ID: "300", Path: "@snapshots/20260712T030000Z_manual_x"},
	}

	got, err := svc.ListSnapshots(context.Background(), vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "20260712T030000Z_manual_x" {
		t.Fatalf("expected the disk snapshot to be reconciled into the list, got %+v", got)
	}
	dbRecs, _ := store.ListSnapshots(vol.UUID)
	if len(dbRecs) != 1 {
		t.Fatalf("expected reconciliation to have inserted a db record, got %+v", dbRecs)
	}
}

func TestListVolumeStatusesReportsUnsupportedForNonBtrfs(t *testing.T) {
	svc, _, _ := newTestService(t)
	vol := serviceTestVolume(t)
	vol.Filesystem = "ext4"

	got, err := svc.ListVolumeStatuses(context.Background(), []VolumeInfo{vol})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Supported {
		t.Fatalf("expected unsupported for ext4 volume, got %+v", got)
	}
}

func TestListVolumeStatusesReportsCountAndLastAt(t *testing.T) {
	svc, runner, _ := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.ListResult[vol.MountPoint] = []SubvolumeEntry{
		{ID: "300", Path: "@snapshots/20260712T030000Z_manual_x"},
		{ID: "301", Path: "@snapshots/20260712T040000Z_manual_y"},
	}

	got, err := svc.ListVolumeStatuses(context.Background(), []VolumeInfo{vol})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 status, got %d", len(got))
	}
	st := got[0]
	if !st.Supported {
		t.Fatal("expected supported=true for a mounted btrfs volume")
	}
	if st.Count != 2 {
		t.Errorf("got count %d, want 2", st.Count)
	}
	if st.LastAt == nil || st.LastAt.Hour() != 4 {
		t.Errorf("got last_at %v, want hour 4", st.LastAt)
	}
}

func TestListVolumeStatusesReflectsEnabledFromPolicy(t *testing.T) {
	svc, runner, store := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	if err := store.SavePolicy(model.SnapshotPolicy{VolumeUUID: vol.UUID, Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := svc.ListVolumeStatuses(context.Background(), []VolumeInfo{vol})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got[0].Enabled {
		t.Error("expected enabled=true to be reflected from the stored policy")
	}
}

func TestListVolumeStatusesReflectsLivePauseState(t *testing.T) {
	svc, runner, _ := newTestService(t)
	svc.Pause = NewPauseState()
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)

	svc.Pause.Set(vol.UUID, "volume usage 95.0% exceeds pause threshold 90%")

	got, err := svc.ListVolumeStatuses(context.Background(), []VolumeInfo{vol})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].PausedReason != "volume usage 95.0% exceeds pause threshold 90%" {
		t.Fatalf("expected PausedReason to reflect live PauseState, got %q", got[0].PausedReason)
	}

	svc.Pause.Clear(vol.UUID)
	got, err = svc.ListVolumeStatuses(context.Background(), []VolumeInfo{vol})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].PausedReason != "" {
		t.Fatalf("expected PausedReason to clear once resolved, got %q", got[0].PausedReason)
	}
}

func TestListVolumeStatusesPausedReasonEmptyWithoutPauseState(t *testing.T) {
	// svc.Pause is left nil (as newTestService leaves it) — must not panic
	// and must report empty, not the stale "B3 takes over" placeholder text.
	svc, runner, _ := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)

	got, err := svc.ListVolumeStatuses(context.Background(), []VolumeInfo{vol})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].PausedReason != "" {
		t.Fatalf("expected empty PausedReason with nil Pause, got %q", got[0].PausedReason)
	}
}

func TestSavePolicyRejectsEnablingUnsupportedVolume(t *testing.T) {
	svc, _, store := newTestService(t)
	vol := serviceTestVolume(t)
	vol.Filesystem = "ext4"

	err := svc.SavePolicy(context.Background(), vol, model.SnapshotPolicy{Enabled: true})
	if !errors.Is(err, ErrVolumeNotSupported) {
		t.Fatalf("expected ErrVolumeNotSupported, got %v", err)
	}
	if _, ok := store.policies[vol.UUID]; ok {
		t.Error("expected policy not to be saved when enabling fails")
	}
}

func TestSavePolicyEnsuresMountWhenEnabling(t *testing.T) {
	svc, runner, store := newTestService(t)
	vol := serviceTestVolume(t)
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.SeedTopLevelSubvolume(vol.DevicePath, "@snapshots")

	err := svc.SavePolicy(context.Background(), vol, model.SnapshotPolicy{
		Enabled: true, HourlyKeep: 24, DailyKeep: 7, WeeklyKeep: 4, PauseThresholdPct: 90,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	saved, _ := store.GetOrCreatePolicy(vol.UUID)
	if !saved.Enabled {
		t.Error("expected policy to be saved as enabled")
	}
	mounted, _ := runner.IsMounted(vol.DevicePath, SnapshotsDir(vol))
	if !mounted {
		t.Error("expected @snapshots to be mounted after enabling")
	}
}

func TestSavePolicyPersistsDisablingWithoutMountCheck(t *testing.T) {
	svc, _, store := newTestService(t)
	vol := serviceTestVolume(t)
	// Volume not seeded as mounted at all — disabling must not require it.
	if err := store.SavePolicy(model.SnapshotPolicy{VolumeUUID: vol.UUID, Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := svc.SavePolicy(context.Background(), vol, model.SnapshotPolicy{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	saved, _ := store.GetOrCreatePolicy(vol.UUID)
	if saved.Enabled {
		t.Error("expected enabled to be persisted as false")
	}
}

func TestGetPolicyReturnsDefaultWhenUnset(t *testing.T) {
	svc, _, _ := newTestService(t)
	policy, err := svc.GetPolicy("unset-volume")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policy.Enabled {
		t.Error("expected default policy to be disabled")
	}
	if policy.HourlyKeep != DefaultHourlyKeep {
		t.Errorf("got hourly_keep %d, want %d", policy.HourlyKeep, DefaultHourlyKeep)
	}
}
