package snapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

func TestComputeReconcileDiffInsertsDiskOnlyEntries(t *testing.T) {
	diff := ComputeReconcileDiff("vol-1", []string{"20260712T030000Z_auto-hourly"}, nil)

	if len(diff.ToInsert) != 1 {
		t.Fatalf("ToInsert = %v, want 1 entry", diff.ToInsert)
	}
	rec := diff.ToInsert[0]
	if rec.VolumeUUID != "vol-1" || rec.Name != "20260712T030000Z_auto-hourly" {
		t.Errorf("unexpected inserted record: %+v", rec)
	}
	// Name parses as one of our own, so metadata should be recovered rather
	// than falling back to "unknown".
	if rec.Type != TypeAutoHourly {
		t.Errorf("Type = %q, want %q (recovered from name)", rec.Type, TypeAutoHourly)
	}
	if len(diff.ToDelete) != 0 {
		t.Errorf("ToDelete = %v, want none", diff.ToDelete)
	}
}

func TestComputeReconcileDiffFallsBackToUnknownForForeignNames(t *testing.T) {
	diff := ComputeReconcileDiff("vol-1", []string{"my-manual-btrfs-snapshot"}, nil)

	if len(diff.ToInsert) != 1 {
		t.Fatalf("ToInsert = %v, want 1 entry", diff.ToInsert)
	}
	rec := diff.ToInsert[0]
	if rec.Type != TypeUnknown {
		t.Errorf("Type = %q, want %q for an unparseable name", rec.Type, TypeUnknown)
	}
	if rec.CreatedBy != "unknown" {
		t.Errorf("CreatedBy = %q, want %q", rec.CreatedBy, "unknown")
	}
	if rec.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set even for unparseable names")
	}
}

func TestComputeReconcileDiffDeletesDBOnlyEntries(t *testing.T) {
	dbOnly := model.Snapshot{ID: 7, VolumeUUID: "vol-1", Name: "gone-from-disk"}
	diff := ComputeReconcileDiff("vol-1", nil, []model.Snapshot{dbOnly})

	if len(diff.ToInsert) != 0 {
		t.Errorf("ToInsert = %v, want none", diff.ToInsert)
	}
	if len(diff.ToDelete) != 1 || diff.ToDelete[0].ID != 7 {
		t.Errorf("ToDelete = %v, want [id=7]", diff.ToDelete)
	}
}

func TestComputeReconcileDiffOverlapIsNoop(t *testing.T) {
	rec := model.Snapshot{ID: 1, VolumeUUID: "vol-1", Name: "20260712T030000Z_manual"}
	diff := ComputeReconcileDiff("vol-1", []string{"20260712T030000Z_manual"}, []model.Snapshot{rec})

	if len(diff.ToInsert) != 0 {
		t.Errorf("ToInsert = %v, want none", diff.ToInsert)
	}
	if len(diff.ToDelete) != 0 {
		t.Errorf("ToDelete = %v, want none", diff.ToDelete)
	}
}

// fakeStore is a minimal in-memory Store for testing Reconcile's
// orchestration without a real database.
type fakeStore struct {
	records   map[uint]model.Snapshot
	nextID    uint
	insertErr error
	deleteErr error
}

func newFakeStore(seed ...model.Snapshot) *fakeStore {
	s := &fakeStore{records: map[uint]model.Snapshot{}, nextID: 1}
	for _, rec := range seed {
		if rec.ID == 0 {
			rec.ID = s.nextID
			s.nextID++
		}
		s.records[rec.ID] = rec
	}
	return s
}

func (s *fakeStore) ListSnapshots(volumeUUID string) ([]model.Snapshot, error) {
	var out []model.Snapshot
	for _, rec := range s.records {
		if rec.VolumeUUID == volumeUUID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *fakeStore) InsertSnapshot(rec model.Snapshot) error {
	if s.insertErr != nil {
		return s.insertErr
	}
	rec.ID = s.nextID
	s.nextID++
	s.records[rec.ID] = rec
	return nil
}

func (s *fakeStore) DeleteSnapshot(id uint) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.records, id)
	return nil
}

func TestReconcileInsertsAndDeletes(t *testing.T) {
	store := newFakeStore(model.Snapshot{VolumeUUID: "vol-1", Name: "stale-record"})

	diff, err := Reconcile(store, "vol-1", []string{"20260712T030000Z_auto-hourly"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diff.ToInsert) != 1 || len(diff.ToDelete) != 1 {
		t.Fatalf("diff = %+v, want 1 insert and 1 delete", diff)
	}

	remaining, err := store.ListSnapshots("vol-1")
	if err != nil {
		t.Fatalf("ListSnapshots error: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Name != "20260712T030000Z_auto-hourly" {
		t.Errorf("remaining records = %+v, want just the disk-observed one", remaining)
	}
}

func TestReconcilePropagatesStoreErrors(t *testing.T) {
	store := newFakeStore()
	store.insertErr = errors.New("db is locked")

	if _, err := Reconcile(store, "vol-1", []string{"20260712T030000Z_manual"}); err == nil {
		t.Error("expected error to propagate from InsertSnapshot, got nil")
	}
}

func TestReconcileVolumeUsesRunnerListing(t *testing.T) {
	runner := NewFakeRunner()
	vol := VolumeInfo{UUID: "vol-1", DevicePath: "/dev/md0", MountPoint: "/media/RAID_test"}
	runner.ListResult[vol.MountPoint] = []SubvolumeEntry{
		{Path: "@"},
		{Path: "@snapshots"},
		{Path: "@snapshots/20260712T030000Z_auto-hourly"},
	}
	store := newFakeStore()

	diff, err := ReconcileVolume(context.Background(), runner, store, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diff.ToInsert) != 1 || diff.ToInsert[0].Name != "20260712T030000Z_auto-hourly" {
		t.Errorf("diff.ToInsert = %+v, want the single snapshot under @snapshots", diff.ToInsert)
	}
}

func TestBuildUnknownSnapshotSetsCreatedAtFromParsedTime(t *testing.T) {
	rec := buildUnknownSnapshot("vol-1", "20260712T030000Z_preop")
	want := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	if !rec.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", rec.CreatedAt, want)
	}
	if rec.Type != TypePreop {
		t.Errorf("Type = %q, want %q", rec.Type, TypePreop)
	}
}
