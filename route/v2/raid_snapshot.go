package v2

import (
	"context"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	svcmodel "github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"github.com/NimoTech/NimoOS-LocalStorage/service/snapshot"
	"go.uber.org/zap"
)

// enableSnapshotsForNewRAID best-effort enables automatic btrfs snapshot
// protection for a freshly created RAID array (task B5). enable mirrors
// CreateRAIDRequest.EnableSnapshots: nil or true enables it (the default so
// older clients that never send the field keep getting it), an explicit
// false skips the step entirely — no policy row is touched.
//
// Best-effort semantics (task B5 brief): a failure here — the array isn't
// btrfs, or mounting @snapshots fails for any other reason — must never
// fail RAID creation itself. It's logged and swallowed here; the volume
// simply shows up as enabled=false via GET /v2/snapshot/volumes, and the
// user can retry through the snapshot policy API (PUT /v2/snapshot/policy).
func enableSnapshotsForNewRAID(enable *bool, raid *svcmodel.RAIDArray) {
	if enable != nil && !*enable {
		return
	}
	if raid == nil {
		return
	}

	// Snapshots are btrfs-only. Skip unconditionally for any other
	// filesystem (e.g. ext4) — this is the RAID's recorded filesystem
	// choice from creation, not a fresh probe. This is expected, routine
	// behavior for a non-btrfs array, not a failure, so it's logged at
	// info level rather than as a warning/error; it also means
	// SavePolicy/EnsureSnapshotsMount are never invoked for such a volume,
	// so no enabled=true policy row can be persisted for it through this
	// path (belt-and-suspenders: EnsureSnapshotsMount itself already
	// refuses non-btrfs volumes before SavePolicy would persist anything).
	if !strings.EqualFold(raid.Filesystem, "btrfs") {
		logger.Info("skipping snapshot auto-enable: RAID array is not btrfs",
			zap.Uint("raid_id", raid.ID),
			zap.String("volume_uuid", raid.UUID),
			zap.String("filesystem", raid.Filesystem))
		return
	}

	volumes := snapshot.VolumesFromRAIDArrays([]*svcmodel.RAIDArray{raid})
	if len(volumes) == 0 {
		return
	}

	policy := snapshot.DefaultPolicy(raid.UUID)
	policy.Enabled = true

	if err := service.MyService.Snapshot().SavePolicy(context.Background(), volumes[0], policy); err != nil {
		logger.Error("best-effort snapshot enable failed after RAID create",
			zap.Uint("raid_id", raid.ID),
			zap.String("volume_uuid", raid.UUID),
			zap.Error(err))
	}
}
