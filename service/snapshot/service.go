package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Service is the orchestration layer route/snapshot.go calls into: it wires
// together the B1 primitives (Runner, FullStore, FstabPersister,
// EnsureSnapshotsMount, Reconcile*, naming) into the operations the API
// needs, so the route layer stays a thin HTTP binding (handoff §3.4 /
// task-B2 brief).
type Service struct {
	Runner    Runner
	Store     FullStore
	Persister FstabPersister
	// Pause is the scheduler's live space-guard state (scheduler.go /
	// pause.go), shared with whatever *Scheduler is constructed on top of
	// this same Service so ListVolumeStatuses's PausedReason (below)
	// reflects the scheduler's most recent evaluation. nil-safe: a Service
	// built without wiring a Scheduler (e.g. most of this package's own
	// tests) simply reports every volume as never paused.
	Pause *PauseState
	// Paths is the plain-filesystem seam (existence/stat/mkdir) Restore and
	// FileVersions need beyond btrfs commands — see fs.go.
	Paths PathChecker
	// Copier performs Restore's actual data copy (reflink, falling back to
	// a plain deep copy) — see copy.go.
	Copier Copier
	// Clock supplies Restore's ".restored-<ts>" timestamp. nil-safe: a
	// Service constructed with a zero-value Clock (e.g. most of this
	// package's own pre-B4 tests, which never call Restore) simply isn't
	// exercised on that path; NewService always wires RealClock{}, and
	// tests that do call Restore inject a FakeClock for deterministic
	// destination names.
	Clock Clock
	// TempSuffix generates the random-looking suffix Restore's
	// on_conflict=overwrite path (restoreOverwrite, restore.go) appends to
	// build its "<name>.nimoos-restoring-<suffix>" temporary file name.
	// nil-safe: a nil TempSuffix (the default for every Service that never
	// sets it, including NewService's production Service) falls back to
	// randomHexSuffix, a crypto/rand-backed generator. Tests that need a
	// predictable temporary path (to key a FakeCopier failure injection by
	// exact dest string, or to assert the exact Rename call made) inject a
	// fixed or sequenced func here instead.
	TempSuffix func() string
}

func (s *Service) clock() Clock {
	if s.Clock == nil {
		return RealClock{}
	}
	return s.Clock
}

// tempSuffix resolves TempSuffix's nil-means-randomHexSuffix default.
func (s *Service) tempSuffix() string {
	if s.TempSuffix != nil {
		return s.TempSuffix()
	}
	return randomHexSuffix()
}

// NewService returns the production Service, backed by the real btrfs/mount
// tooling (ExecRunner), GORM-backed storage, and real fstab persistence.
func NewService(db *gorm.DB) *Service {
	return &Service{
		Runner:    NewExecRunner(),
		Store:     NewGormStore(db),
		Persister: RealFstabPersister{},
		Pause:     NewPauseState(),
		Paths:     OSPathChecker{},
		Copier:    NewExecCopier(),
		Clock:     RealClock{},
	}
}

// VolumeStatus is one row of GET /v2/snapshot/volumes: a volume's snapshot
// capability and current state (handoff §3.4).
type VolumeStatus struct {
	VolumeUUID string `json:"volume_uuid"`
	Mount      string `json:"mount"`
	Filesystem string `json:"fs"`
	// Supported is true when the volume is btrfs and currently mounted.
	// Deeper layout-compatibility checks (does @snapshots exist / can it be
	// created) only run when the volume is actually enabled or a snapshot
	// is created (EnsureSnapshotsMount) — this list endpoint is read-only
	// and never mounts or creates anything as a side effect of a GET.
	Supported bool       `json:"supported"`
	Enabled   bool       `json:"enabled"`
	Count     int        `json:"count"`
	LastAt    *time.Time `json:"last_at"`
	// PausedReason reflects the scheduler's live space-guard state
	// (Service.Pause) for this volume: non-empty while automatic snapshots
	// are currently paused due to low free space, empty otherwise —
	// including automatically once usage recovers (handoff §3.3: "恢复后
	// 自动续").
	PausedReason string `json:"paused_reason"`
}

// ResolveVolumeIdentity finds volumeUUID among volumes (the caller's
// current enumeration of known volumes, e.g.
// VolumesFromRAIDArrays(raidList)) and verifies only its identity: that
// it's a known volume and that its filesystem is btrfs. It deliberately
// does NOT require the volume to be currently mounted.
//
// Fix Round 1 (review defect): the original ResolveVolume unconditionally
// required a mounted volume, and that gate was applied uniformly to every
// volume_uuid-bearing endpoint including GET policy (a pure DB read) and
// PUT policy's disable path — even though Service.SavePolicy itself only
// calls EnsureSnapshotsMount when Enabled==true (see
// TestSavePolicyPersistsDisablingWithoutMountCheck). That made it
// impossible to read or disable a saved policy for a volume that's
// temporarily offline (RAID member unplugged, cold-boot enumeration race),
// even though nothing about those operations touches the filesystem. Use
// this lighter variant for operations that don't need the volume mounted;
// use ResolveVolume (below) for operations that do.
func (s *Service) ResolveVolumeIdentity(volumes []VolumeInfo, volumeUUID string) (VolumeInfo, error) {
	for _, v := range volumes {
		if v.UUID != volumeUUID {
			continue
		}
		if !strings.EqualFold(v.Filesystem, "btrfs") {
			return VolumeInfo{}, ErrVolumeNotBtrfs
		}
		return v, nil
	}
	return VolumeInfo{}, ErrVolumeNotFound
}

// ResolveVolume finds volumeUUID among volumes and verifies it's usable as
// a snapshot volume right now: btrfs, and actually mounted (handoff/B2
// brief: "volume_uuid 必须对应真实已挂载 btrfs 卷" — for operations that
// actually touch the filesystem: list/create/delete snapshots, and
// enabling a policy). See ResolveVolumeIdentity for the mount-agnostic
// variant used by reads and disabling a policy.
func (s *Service) ResolveVolume(volumes []VolumeInfo, volumeUUID string) (VolumeInfo, error) {
	v, err := s.ResolveVolumeIdentity(volumes, volumeUUID)
	if err != nil {
		return VolumeInfo{}, err
	}
	mounted, err := s.Runner.IsMounted(v.DevicePath, v.MountPoint)
	if err != nil {
		return VolumeInfo{}, fmt.Errorf("check volume mount state: %w", err)
	}
	if !mounted {
		return VolumeInfo{}, ErrVolumeNotMounted
	}
	return v, nil
}

// ListVolumeStatuses builds the GET /v2/snapshot/volumes response for every
// volume the caller passes in.
func (s *Service) ListVolumeStatuses(ctx context.Context, volumes []VolumeInfo) ([]VolumeStatus, error) {
	statuses := make([]VolumeStatus, 0, len(volumes))
	for _, v := range volumes {
		st := VolumeStatus{VolumeUUID: v.UUID, Mount: v.MountPoint, Filesystem: v.Filesystem}

		st.Supported = strings.EqualFold(v.Filesystem, "btrfs")
		if st.Supported {
			mounted, err := s.Runner.IsMounted(v.DevicePath, v.MountPoint)
			if err != nil {
				return nil, fmt.Errorf("check mount state for volume %s: %w", v.UUID, err)
			}
			st.Supported = mounted
		}

		policy, err := s.Store.GetOrCreatePolicy(v.UUID)
		if err != nil {
			return nil, fmt.Errorf("load policy for volume %s: %w", v.UUID, err)
		}
		st.Enabled = policy.Enabled

		if st.Supported {
			if _, err := ReconcileVolume(ctx, s.Runner, s.Store, v); err != nil {
				// Non-fatal for the list as a whole: report zero
				// count/last_at for this volume rather than failing every
				// other volume's status too.
				logger.Info("snapshot: reconcile failed while listing volume status",
					zap.String("volume_uuid", v.UUID), zap.Error(err))
			} else if recs, err := s.Store.ListSnapshots(v.UUID); err == nil {
				st.Count = len(recs)
				st.LastAt = latestCreatedAt(recs)
			}
		}

		st.PausedReason = s.Pause.Reason(v.UUID)

		statuses = append(statuses, st)
	}
	return statuses, nil
}

func latestCreatedAt(recs []model.Snapshot) *time.Time {
	if len(recs) == 0 {
		return nil
	}
	last := recs[0].CreatedAt
	for _, r := range recs[1:] {
		if r.CreatedAt.After(last) {
			last = r.CreatedAt
		}
	}
	return &last
}

// ListSnapshots reconciles volume's DB records against disk, then returns
// them (GET /v2/snapshot?volume_uuid=).
func (s *Service) ListSnapshots(ctx context.Context, volume VolumeInfo) ([]model.Snapshot, error) {
	if _, err := ReconcileVolume(ctx, s.Runner, s.Store, volume); err != nil {
		return nil, fmt.Errorf("reconcile volume %s: %w", volume.UUID, err)
	}
	return s.Store.ListSnapshots(volume.UUID)
}

// CreateManual creates a type=manual snapshot for volume (POST /v2/snapshot).
// It ensures @snapshots is mounted first (idempotent — a no-op if it
// already is), since a manual snapshot must be creatable even for a volume
// whose automatic policy has never been enabled.
func (s *Service) CreateManual(ctx context.Context, volume VolumeInfo, label, createdBy string) (model.Snapshot, error) {
	status, err := EnsureSnapshotsMount(ctx, s.Runner, s.Persister, volume)
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("ensure @snapshots mount for volume %s: %w", volume.UUID, err)
	}
	if !status.Supported {
		return model.Snapshot{}, fmt.Errorf("%w: %s", ErrVolumeNotSupported, status.Reason)
	}

	now := time.Now().UTC()
	name, err := FormatName(now, TypeManual, label)
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidSnapshotName, err)
	}

	destPath := filepath.Join(SnapshotsDir(volume), name)
	if err := s.Runner.CreateReadOnlySnapshot(ctx, volume.MountPoint, destPath); err != nil {
		return model.Snapshot{}, fmt.Errorf("create snapshot: %w", err)
	}

	rec := model.Snapshot{
		VolumeUUID: volume.UUID,
		Name:       name,
		Type:       TypeManual,
		Label:      SanitizeLabel(label),
		CreatedAt:  now,
		CreatedBy:  createdBy,
	}
	if err := s.Store.InsertSnapshot(rec); err != nil {
		return model.Snapshot{}, fmt.Errorf("record snapshot in db: %w", err)
	}
	return rec, nil
}

// DeleteByName validates name, reconciles volume against disk, and — if the
// snapshot currently exists — deletes it from disk and (if present) its DB
// record (DELETE /v2/snapshot/:name?volume_uuid=).
func (s *Service) DeleteByName(ctx context.Context, volume VolumeInfo, name string) error {
	path, err := ResolveSnapshotPath(volume, name)
	if err != nil {
		return err
	}

	if _, err := ReconcileVolume(ctx, s.Runner, s.Store, volume); err != nil {
		return fmt.Errorf("reconcile volume %s: %w", volume.UUID, err)
	}

	recs, err := s.Store.ListSnapshots(volume.UUID)
	if err != nil {
		return fmt.Errorf("list snapshots for volume %s: %w", volume.UUID, err)
	}
	var found *model.Snapshot
	for i := range recs {
		if recs[i].Name == name {
			found = &recs[i]
			break
		}
	}
	if found == nil {
		return ErrSnapshotNotFound
	}

	if err := s.Runner.DeleteSubvolume(ctx, path); err != nil {
		return fmt.Errorf("delete snapshot subvolume: %w", err)
	}
	if err := s.Store.DeleteSnapshot(found.ID); err != nil {
		return fmt.Errorf("delete snapshot record: %w", err)
	}
	return nil
}

// GetPolicy returns volumeUUID's policy, creating the recommended default
// (disabled) if none is stored yet (GET /v2/snapshot/policy?volume_uuid=).
func (s *Service) GetPolicy(volumeUUID string) (*model.SnapshotPolicy, error) {
	return s.Store.GetOrCreatePolicy(volumeUUID)
}

// SavePolicy persists policy for volume (PUT /v2/snapshot/policy). If the
// caller is enabling automatic snapshots (policy.Enabled), @snapshots is
// ensured mounted first — the policy is not saved as enabled if the volume
// turns out not to support snapshots.
func (s *Service) SavePolicy(ctx context.Context, volume VolumeInfo, policy model.SnapshotPolicy) error {
	policy.VolumeUUID = volume.UUID
	if policy.Enabled {
		status, err := EnsureSnapshotsMount(ctx, s.Runner, s.Persister, volume)
		if err != nil {
			return fmt.Errorf("ensure @snapshots mount for volume %s: %w", volume.UUID, err)
		}
		if !status.Supported {
			return fmt.Errorf("%w: %s", ErrVolumeNotSupported, status.Reason)
		}
	}
	return s.Store.SavePolicy(policy)
}
