package v2

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const btrfsUsageCacheTTL = 10 * time.Minute

type RAIDUsage struct {
	Filesystem string      `json:"filesystem"`
	BtrfsUsage *BtrfsUsage `json:"btrfs_usage,omitempty"`
}

type BtrfsUsage struct {
	DeviceSizeBytes      int64     `json:"device_size_bytes"`
	DeviceAllocatedBytes int64     `json:"device_allocated_bytes"`
	FreeEstimatedBytes   int64     `json:"free_estimated_bytes"`
	CachedAt             time.Time `json:"cached_at"`
}

type cachedBtrfsUsage struct {
	usage     BtrfsUsage
	expiresAt time.Time
}

func (s *raidService) GetRAIDUsage(id uint) (*RAIDUsage, error) {
	raid, err := s.getRAIDByID(id)
	if err != nil {
		return nil, fmt.Errorf("get RAID array: %w", err)
	}

	filesystem, err := s.resolveFilesystemForDevice(raid, raid.DevicePath)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem: %w", err)
	}

	result := &RAIDUsage{Filesystem: filesystem}
	if filesystem != "btrfs" {
		return result, nil
	}

	usage, err := s.getBtrfsUsageCached(raid.ID, raid.MountPoint)
	if err != nil {
		return nil, err
	}
	result.BtrfsUsage = usage
	return result, nil
}

func (s *raidService) getBtrfsUsageCached(raidID uint, mountPoint string) (*BtrfsUsage, error) {
	now := time.Now()

	s.usageMu.Lock()
	if cached, ok := s.usageCache[raidID]; ok && now.Before(cached.expiresAt) {
		value := cached.usage
		s.usageMu.Unlock()
		return &value, nil
	}
	s.usageMu.Unlock()

	usage, err := queryBtrfsUsage(mountPoint)
	if err != nil {
		return nil, err
	}
	usage.CachedAt = now

	s.usageMu.Lock()
	s.usageCache[raidID] = cachedBtrfsUsage{
		usage:     *usage,
		expiresAt: now.Add(btrfsUsageCacheTTL),
	}
	s.usageMu.Unlock()

	return usage, nil
}

func queryBtrfsUsage(mountPoint string) (*BtrfsUsage, error) {
	out, err := exec.Command("btrfs", "filesystem", "usage", "-b", mountPoint).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("btrfs filesystem usage -b %s: %w: %s", mountPoint, err, strings.TrimSpace(string(out)))
	}

	var (
		result                          BtrfsUsage
		foundDeviceSize                 bool
		foundDeviceAllocated            bool
		foundFreeEstimated              bool
	)

	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		lower := strings.ToLower(l)
		switch {
		case strings.HasPrefix(lower, "device size:"):
			v, parseErr := parseMetricInt64(l)
			if parseErr != nil {
				return nil, fmt.Errorf("parse device size from %q: %w", l, parseErr)
			}
			result.DeviceSizeBytes = v
			foundDeviceSize = true
		case strings.HasPrefix(lower, "device allocated:"):
			v, parseErr := parseMetricInt64(l)
			if parseErr != nil {
				return nil, fmt.Errorf("parse device allocated from %q: %w", l, parseErr)
			}
			result.DeviceAllocatedBytes = v
			foundDeviceAllocated = true
		case strings.HasPrefix(lower, "free (estimated):"):
			v, parseErr := parseMetricInt64(l)
			if parseErr != nil {
				return nil, fmt.Errorf("parse free estimated from %q: %w", l, parseErr)
			}
			result.FreeEstimatedBytes = v
			foundFreeEstimated = true
		}
	}

	if !foundDeviceSize || !foundDeviceAllocated || !foundFreeEstimated {
		return nil, fmt.Errorf("missing expected fields in btrfs usage output")
	}

	return &result, nil
}

func parseMetricInt64(line string) (int64, error) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid metric line: %s", line)
	}
	for _, token := range strings.Fields(parts[1]) {
		clean := strings.Trim(token, "(),")
		clean = strings.ReplaceAll(clean, ",", "")
		if clean == "" {
			continue
		}
		if v, err := strconv.ParseInt(clean, 10, 64); err == nil {
			return v, nil
		}
	}
	return 0, fmt.Errorf("no integer metric found in line: %s", line)
}

