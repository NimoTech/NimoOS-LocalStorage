package snapshot

import (
	"context"
	"fmt"

	v2 "github.com/NimoTech/NimoOS-LocalStorage/service/v2"
)

// RAIDUsageProvider adapts v2.RAIDService's cached btrfs usage query (handoff
// §3.3: "reuse service/v2/raid_usage.go's btrfs usage query") into the
// UsageProvider interface the scheduler depends on.
//
// "Volume" is a RAID array today (VolumesFromRAIDArrays' doc comment), but
// GetRAIDUsage is keyed by the array's numeric ID rather than volume_uuid,
// so this adapter re-resolves volume_uuid -> RAID array on every call via
// ListRAIDArrays (itself an in-memory DB read, not an expensive call).
//
// GetRAIDUsage caches its underlying `btrfs filesystem usage` result for 10
// minutes (raid_usage.go's btrfsUsageCacheTTL) and this adapter does not
// attempt to bypass or shorten that cache — the scheduler's shortest
// cadence is hourly (60x the cache TTL), so up to ~10 minutes of staleness
// in the space guard's view of usage is an acceptable trade-off against
// having every volume's scheduler tick shell out to `btrfs filesystem
// usage` once a minute.
type RAIDUsageProvider struct {
	RAID v2.RAIDService
}

// NewRAIDUsageProvider returns a RAIDUsageProvider backed by raid.
func NewRAIDUsageProvider(raid v2.RAIDService) *RAIDUsageProvider {
	return &RAIDUsageProvider{RAID: raid}
}

func (p *RAIDUsageProvider) UsagePercent(_ context.Context, volume VolumeInfo) (float64, error) {
	raids, err := p.RAID.ListRAIDArrays()
	if err != nil {
		return 0, fmt.Errorf("list RAID arrays: %w", err)
	}

	for _, r := range raids {
		if r == nil || r.UUID != volume.UUID {
			continue
		}
		usage, err := p.RAID.GetRAIDUsage(r.ID)
		if err != nil {
			return 0, fmt.Errorf("get RAID usage for volume %s: %w", volume.UUID, err)
		}
		if usage.BtrfsUsage == nil {
			return 0, fmt.Errorf("volume %s: no btrfs usage data (filesystem %q)", volume.UUID, usage.Filesystem)
		}
		return btrfsUsagePercent(btrfsUsage{
			DeviceSizeBytes:    usage.BtrfsUsage.DeviceSizeBytes,
			FreeEstimatedBytes: usage.BtrfsUsage.FreeEstimatedBytes,
		})
	}
	return 0, fmt.Errorf("volume %s: not found among RAID arrays", volume.UUID)
}
