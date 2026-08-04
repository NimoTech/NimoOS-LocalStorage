package snapshot

import (
	"context"
	"fmt"
	"time"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// ReconcileDiff is the pure result of comparing disk-observed snapshot names
// against the DB records for the same volume.
type ReconcileDiff struct {
	// ToInsert holds records for names present on disk but missing from the
	// DB (handoff §3.2: "on disk but not in DB → backfill as unknown").
	ToInsert []model.Snapshot
	// ToDelete holds DB records whose name is no longer present on disk
	// (handoff §3.2: "in DB but not on disk → delete the record").
	ToDelete []model.Snapshot
}

// ComputeReconcileDiff is a pure function: given the snapshot names observed
// on disk for a volume (see FilterSnapshotNames) and the DB rows currently
// recorded for it, decides which DB rows to add and which to remove. Disk
// is the source of truth. It performs no I/O.
func ComputeReconcileDiff(volumeUUID string, diskNames []string, dbRecords []model.Snapshot) ReconcileDiff {
	onDisk := make(map[string]struct{}, len(diskNames))
	for _, n := range diskNames {
		onDisk[n] = struct{}{}
	}
	inDB := make(map[string]struct{}, len(dbRecords))
	for _, r := range dbRecords {
		inDB[r.Name] = struct{}{}
	}

	var diff ReconcileDiff
	for _, n := range diskNames {
		if _, ok := inDB[n]; ok {
			continue
		}
		diff.ToInsert = append(diff.ToInsert, buildUnknownSnapshot(volumeUUID, n))
	}
	for _, r := range dbRecords {
		if _, ok := onDisk[r.Name]; !ok {
			diff.ToDelete = append(diff.ToDelete, r)
		}
	}
	return diff
}

// buildUnknownSnapshot recovers as much metadata as possible from the
// subvolume name itself: it may well be one of ours whose DB row was lost
// (e.g. the DB was restored from an older backup). Falls back to type
// "unknown" with the current time when the name doesn't match our naming
// convention (handoff §3.2: "backfill as unknown").
func buildUnknownSnapshot(volumeUUID, name string) model.Snapshot {
	rec := model.Snapshot{
		VolumeUUID: volumeUUID,
		Name:       name,
		Type:       TypeUnknown,
		CreatedBy:  "unknown",
		CreatedAt:  time.Now().UTC(),
	}
	if parsed, err := ParseName(name); err == nil {
		rec.Type = parsed.Type
		rec.Label = parsed.Label
		rec.CreatedAt = parsed.Time
	}
	return rec
}

// Store is the minimal DB persistence surface Reconcile needs. GormStore
// (store.go) is the production implementation; tests can supply their own
// in-memory Store to exercise Reconcile's orchestration without a database.
type Store interface {
	ListSnapshots(volumeUUID string) ([]model.Snapshot, error)
	InsertSnapshot(rec model.Snapshot) error
	DeleteSnapshot(id uint) error
}

// Reconcile aligns DB snapshot records for volumeUUID with what's actually
// on disk (handoff §3.2). diskNames should come from FilterSnapshotNames
// applied to a `btrfs subvolume list` result — see ReconcileVolume for the
// full flow starting from a Runner. Returns the diff that was applied.
func Reconcile(store Store, volumeUUID string, diskNames []string) (ReconcileDiff, error) {
	dbRecords, err := store.ListSnapshots(volumeUUID)
	if err != nil {
		return ReconcileDiff{}, fmt.Errorf("list db snapshots for volume %s: %w", volumeUUID, err)
	}

	diff := ComputeReconcileDiff(volumeUUID, diskNames, dbRecords)

	for _, rec := range diff.ToInsert {
		if err := store.InsertSnapshot(rec); err != nil {
			return diff, fmt.Errorf("insert reconciled snapshot %s: %w", rec.Name, err)
		}
	}
	for _, rec := range diff.ToDelete {
		if err := store.DeleteSnapshot(rec.ID); err != nil {
			return diff, fmt.Errorf("delete stale snapshot record %s: %w", rec.Name, err)
		}
	}
	return diff, nil
}

// ReconcileVolume lists what's actually on disk for volume (via runner) and
// reconciles the DB against it. This is the glue most callers should use;
// Reconcile itself stays free of the Runner dependency for easy unit
// testing of the DB-orchestration logic in isolation.
func ReconcileVolume(ctx context.Context, runner Runner, store Store, volume VolumeInfo) (ReconcileDiff, error) {
	entries, err := runner.ListSubvolumes(ctx, volume.MountPoint)
	if err != nil {
		return ReconcileDiff{}, fmt.Errorf("list subvolumes for volume %s: %w", volume.UUID, err)
	}
	diskNames := FilterSnapshotNames(entries)
	return Reconcile(store, volume.UUID, diskNames)
}
