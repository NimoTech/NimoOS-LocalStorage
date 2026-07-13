package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

const (
	// SnapshotsSubvolumeName is the top-level btrfs subvolume that stores
	// read-only snapshots, created alongside "@" for every btrfs volume this
	// system formats (see service/v2/raid_filesystem.go's
	// btrfsSetupSubvolumes) but never mounted or used until this feature.
	SnapshotsSubvolumeName = "@snapshots"

	// SnapshotsMountSubdir is the directory under a volume's mount point
	// where @snapshots is mounted. The leading "." hides it from the file
	// browser by convention, same as ".system_data".
	SnapshotsMountSubdir = ".snapshots"
)

// VolumeInfo is the minimal description of a mounted volume this package
// needs. Callers (the RAID service today; potentially other volume sources
// later) adapt their own models into this shape — keeping this package
// decoupled from any one volume source.
type VolumeInfo struct {
	// UUID identifies the volume (e.g. RAIDArray.UUID) and is the
	// volume_uuid used throughout the o_snapshot* tables.
	UUID string
	// DevicePath is the block device backing the volume (e.g. "/dev/md0").
	DevicePath string
	// MountPoint is where the volume's data subvolume is mounted.
	MountPoint string
	// Filesystem is the volume's filesystem, e.g. "btrfs" or "ext4".
	Filesystem string
}

// MountStatus reports whether a volume can, and does, have its @snapshots
// subvolume mounted.
type MountStatus struct {
	Supported bool
	Mounted   bool
	// Reason explains why Supported is false (empty otherwise), surfaced to
	// the UI by a later task.
	Reason string
}

// SnapshotsDir returns the path where @snapshots is (or would be) mounted
// for volume.
func SnapshotsDir(volume VolumeInfo) string {
	return filepath.Join(volume.MountPoint, SnapshotsMountSubdir)
}

// EnsureSnapshotsMount idempotently makes sure volume's @snapshots subvolume
// exists and is mounted at <mount point>/.snapshots, persisting the mount to
// fstab so it survives reboot (handoff §3.1). Safe to call repeatedly — on
// every service start, and again whenever a caller enables snapshots for a
// volume: if it's already mounted, this is a no-op.
//
// Legacy btrfs volumes without a pre-existing @snapshots subvolume get one
// created on demand. If that's not possible (e.g. an incompatible top-level
// layout), the volume is reported unsupported with a human-readable reason
// instead of returning an error — "this volume doesn't support snapshots" is
// an expected, non-fatal outcome, not a bug.
func EnsureSnapshotsMount(ctx context.Context, runner Runner, persister FstabPersister, volume VolumeInfo) (MountStatus, error) {
	if !strings.EqualFold(volume.Filesystem, "btrfs") {
		return MountStatus{Supported: false, Reason: "volume filesystem is not btrfs"}, nil
	}
	if volume.MountPoint == "" || volume.DevicePath == "" {
		return MountStatus{}, fmt.Errorf("volume mount point and device path are required")
	}

	snapshotsDir := SnapshotsDir(volume)

	mounted, err := runner.IsMounted(volume.DevicePath, snapshotsDir)
	if err != nil {
		return MountStatus{}, fmt.Errorf("check existing @snapshots mount: %w", err)
	}
	if mounted {
		return MountStatus{Supported: true, Mounted: true}, nil
	}

	if err := ensureSnapshotsSubvolumeExists(ctx, runner, volume.DevicePath); err != nil {
		logger.Info("volume does not support snapshots",
			zap.String("volume_uuid", volume.UUID), zap.Error(err))
		return MountStatus{Supported: false, Reason: err.Error()}, nil
	}

	if err := os.MkdirAll(snapshotsDir, 0o755); err != nil {
		return MountStatus{}, fmt.Errorf("create %s: %w", snapshotsDir, err)
	}

	if err := runner.MountSubvolume(ctx, volume.DevicePath, snapshotsDir, SnapshotsSubvolumeName); err != nil {
		return MountStatus{Supported: false, Reason: fmt.Sprintf("mount @snapshots failed: %v", err)}, nil
	}

	if persister != nil {
		// nofail (+ a bounded device-timeout) is required here: the parent
		// RAID volume is never itself in fstab (it's mounted from the DB by
		// RecoverOnBoot at service startup, which runs after
		// local-fs.target), so at boot this mountpoint doesn't exist yet
		// when systemd processes fstab. Without nofail that failure fails
		// local-fs.target and drops the box into emergency mode.
		opts := "subvol=/" + SnapshotsSubvolumeName + ",nofail,x-systemd.device-timeout=10s"
		if err := persister.Persist(snapshotsDir, volume.DevicePath, "btrfs", opts); err != nil {
			// Non-fatal: the mount itself succeeded, only reboot
			// persistence failed. Log and continue, matching how
			// SaveToFStab's own callers already treat this.
			logger.Error("failed to persist @snapshots mount to fstab",
				zap.String("volume_uuid", volume.UUID), zap.Error(err))
		}
	}

	return MountStatus{Supported: true, Mounted: true}, nil
}

// ensureSnapshotsSubvolumeExists makes sure @snapshots exists at the top
// level of device's btrfs filesystem, creating it if this is a legacy
// volume that predates this feature (or was never formatted by NimoOS).
func ensureSnapshotsSubvolumeExists(ctx context.Context, runner Runner, device string) error {
	tmpMount, err := os.MkdirTemp("", "nimoos-snapshot-check-")
	if err != nil {
		return fmt.Errorf("create temporary mount dir: %w", err)
	}
	defer os.Remove(tmpMount)

	if err := runner.MountTopLevel(ctx, device, tmpMount); err != nil {
		return fmt.Errorf("mount top-level subvolume: %w", err)
	}
	defer func() {
		if err := runner.Unmount(context.Background(), tmpMount); err != nil {
			logger.Info("failed to unmount temporary top-level mount",
				zap.String("mount", tmpMount), zap.Error(err))
		}
	}()

	entries, err := runner.ListSubvolumes(ctx, tmpMount)
	if err != nil {
		logger.Info("list subvolumes failed while checking for @snapshots; will attempt to create it anyway",
			zap.String("device", device), zap.Error(err))
	}
	for _, e := range entries {
		if e.Path == SnapshotsSubvolumeName {
			return nil
		}
	}

	if err := runner.CreateSubvolume(ctx, filepath.Join(tmpMount, SnapshotsSubvolumeName)); err != nil {
		return fmt.Errorf("@snapshots subvolume is missing and could not be created (top-level layout may be incompatible): %w", err)
	}
	return nil
}
