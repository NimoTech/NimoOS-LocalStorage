package v2

import (
	"path/filepath"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/fstab"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"go.uber.org/zap"
)

// SnapshotsMountSubdir is the directory under a volume's mount point where its
// @snapshots subvolume is mounted — the one thing a RAID array leaves in
// /etc/fstab (written by service/snapshot.EnsureSnapshotsMount).
//
// It duplicates service/snapshot.SnapshotsMountSubdir deliberately: that
// package imports this one (service/snapshot/usage_raid.go), so importing it
// back would be a cycle. The two are pinned together by
// TestSnapshotsMountSubdirMatchesRAIDCleanupCopy in service/snapshot.
const SnapshotsMountSubdir = ".snapshots"

// Seams so cleanupRAIDPersistence is testable without a real /etc; production
// values go straight to pkg/fstab and pkg/mdadm.
var (
	fstabEntryByMountPoint = func(mountPoint string) (*fstab.Entry, error) {
		return fstab.Get().GetEntryByMountPoint(mountPoint)
	}
	fstabRemoveByMountPoint = func(mountPoint string) error {
		return fstab.Get().RemoveByMountPoint(mountPoint, false)
	}
	mdadmSaveConfig = mdadm.SaveConfig
)

// cleanupRAIDPersistence drops the boot-time persistence a deleted array leaves
// behind. Creating an array writes to two files that deletion never touched, so
// every create→delete cycle left one dead line in each: an /etc/fstab mount for
// a @snapshots subvolume on a device that no longer exists, and an ARRAY line in
// /etc/mdadm/mdadm.conf naming a stale UUID.
//
// Call this only once the array has been stopped and removed from the DB —
// `mdadm --detail --scan` must no longer be able to see it, or SaveConfig writes
// the line straight back.
//
// The two files are cleaned independently, and failures are logged rather than
// returned: by this point the array is gone, so failing the delete would leave
// the caller with a phantom array it can never retry deleting.
func cleanupRAIDPersistence(mountPoint string) {
	// An empty mount point (a partially written DB row) would make
	// filepath.Join yield "/.snapshots" — the root filesystem's, not ours.
	if mountPoint != "" {
		snapshotsDir := filepath.Join(mountPoint, SnapshotsMountSubdir)
		removeSnapshotsFstabEntry(snapshotsDir)
	}

	// mdadm.conf is regenerated wholesale from the live arrays, so simply
	// re-running SaveConfig now that the array is stopped is what drops its line.
	if err := mdadmSaveConfig(); err != nil {
		logger.Error("failed to refresh mdadm config after delete; a stale ARRAY line may remain",
			zap.String("mount_point", mountPoint), zap.Error(err))
	}
}

func removeSnapshotsFstabEntry(snapshotsDir string) {
	// Look before removing: RemoveByMountPoint rewrites /etc/fstab (and
	// refreshes /etc/fstab.nimoos.bak from it) even when nothing matches, and
	// arrays without snapshots — ext4, or btrfs whose @snapshots mount never
	// succeeded — have no entry to remove.
	entry, err := fstabEntryByMountPoint(snapshotsDir)
	if err != nil {
		logger.Error("failed to look up fstab entry after delete; a stale mount line may remain",
			zap.String("mount_point", snapshotsDir), zap.Error(err))
		return
	}
	if entry == nil {
		return
	}

	if err := fstabRemoveByMountPoint(snapshotsDir); err != nil {
		logger.Error("failed to remove fstab entry after delete; a stale mount line may remain",
			zap.String("mount_point", snapshotsDir), zap.Error(err))
		return
	}
	logger.Info("removed @snapshots fstab entry of deleted array",
		zap.String("mount_point", snapshotsDir))
}
