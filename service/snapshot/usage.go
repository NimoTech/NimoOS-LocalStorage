package snapshot

import (
	"context"
	"fmt"
)

// UsageProvider reports how full a volume's filesystem currently is, as a
// percentage (0-100), for the scheduler's space guard (handoff §3.3:
// "创建前查卷使用率"). Production is RAIDUsageProvider (usage_raid.go),
// wrapping service/v2/raid_usage.go's cached `btrfs filesystem usage`
// query; tests inject FakeUsageProvider (usage_fake.go).
type UsageProvider interface {
	UsagePercent(ctx context.Context, volume VolumeInfo) (float64, error)
}

// btrfsUsage is the subset of service/v2.BtrfsUsage this package needs,
// duplicated as a small struct (rather than importing service/v2 into this
// file) so the percentage math itself stays a pure, dependency-free
// function independently testable from the RAID-array-lookup plumbing in
// usage_raid.go.
type btrfsUsage struct {
	DeviceSizeBytes    int64
	FreeEstimatedBytes int64
}

// btrfsUsagePercent computes "how full" a btrfs filesystem is as a
// percentage, from the same three fields `btrfs filesystem usage -b`
// reports (service/v2/raid_usage.go's BtrfsUsage). It uses
// (size - free-estimated) / size, i.e. how much of the device btrfs no
// longer considers free, rather than "device allocated" (which includes
// unwritten free space inside already-allocated chunks and would
// overstate fullness).
func btrfsUsagePercent(u btrfsUsage) (float64, error) {
	if u.DeviceSizeBytes <= 0 {
		return 0, fmt.Errorf("device size is zero or unknown")
	}
	used := u.DeviceSizeBytes - u.FreeEstimatedBytes
	if used < 0 {
		used = 0
	}
	return float64(used) / float64(u.DeviceSizeBytes) * 100, nil
}
