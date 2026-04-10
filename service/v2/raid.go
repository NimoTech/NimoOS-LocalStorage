package v2

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/diskid"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/partition"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RAIDService manages RAID array lifecycle.
type RAIDService interface {
	CreateRAIDArray(level int, diskPaths []string, name string, chunkKB int) (*model.RAIDArray, error)
	DeleteRAIDArray(id uint) error
	GetRAIDStatus(id uint) (*RAIDStatus, error)
	ListRAIDArrays() ([]*model.RAIDArray, error)
	ReplaceDisk(arrayID uint, oldDiskPath, newDiskPath string) error
	RecoverOnBoot() error
}

// RAIDStatus extends the DB model with live state from mdadm.
type RAIDStatus struct {
	*model.RAIDArray
	LiveState  string             `json:"live_state"`
	RebuildPct float64            `json:"rebuild_pct"`
	TotalBytes int64              `json:"total_bytes"`  // total capacity in bytes
	UsedBytes  int64              `json:"used_bytes"`   // used capacity in bytes
	FreeBytes  int64              `json:"free_bytes"`   // available capacity in bytes
	Members    []MemberDiskStatus `json:"members"`
}

// MemberDiskStatus represents the live state of a single member disk.
type MemberDiskStatus struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Number int    `json:"number"`
}

var validDevicePath = regexp.MustCompile(`^/dev/(sd[a-z]+[0-9]*|nvme[0-9]+n[0-9]+(p[0-9]+)?|md[0-9]+)$`)

func isValidDevicePath(path string) bool {
	return validDevicePath.MatchString(path)
}

var validRAIDName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type raidService struct {
	db           *gorm.DB
	retryMu      sync.Mutex
	retryCancels map[uint]context.CancelFunc
}

// NewRAIDService creates a new RAIDService backed by the given database.
func NewRAIDService(db *gorm.DB) RAIDService {
	return &raidService{
		db:           db,
		retryCancels: make(map[uint]context.CancelFunc),
	}
}

// ---------------------------------------------------------------------------
// DB helpers
// ---------------------------------------------------------------------------

func (s *raidService) saveRAID(r *model.RAIDArray) error {
	return s.db.Create(r).Error
}

func (s *raidService) getRAIDByID(id uint) (*model.RAIDArray, error) {
	var r model.RAIDArray
	if err := s.db.Preload("MemberDisks").First(&r, id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *raidService) getRAIDByUUID(uuid string) (*model.RAIDArray, error) {
	var r model.RAIDArray
	if err := s.db.Preload("MemberDisks").Where("uuid = ?", uuid).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *raidService) listRAIDs() ([]*model.RAIDArray, error) {
	var raids []*model.RAIDArray
	if err := s.db.Preload("MemberDisks").Find(&raids).Error; err != nil {
		return nil, err
	}
	return raids, nil
}

// deleteRAID deletes member records (hasMany) then the array record.
func (s *raidService) deleteRAID(id uint) error {
	if err := s.db.Where("raid_array_id = ?", id).Delete(&model.RAIDMember{}).Error; err != nil {
		return fmt.Errorf("delete member records: %w", err)
	}
	return s.db.Delete(&model.RAIDArray{}, id).Error
}

func (s *raidService) updateRAIDState(id uint, state string) error {
	return s.db.Model(&model.RAIDArray{}).Where("id = ?", id).Update("state", state).Error
}

// updateMemberCache writes the resolved device path back to DevicePathCache.
func (s *raidService) updateMemberCache(memberID uint, path string) {
	s.db.Model(&model.RAIDMember{}).Where("id = ?", memberID).Update("device_path_cache", path)
}

// ---------------------------------------------------------------------------
// minDisks returns the minimum number of disks required for a RAID level.
// ---------------------------------------------------------------------------

func minDisks(level int) (int, error) {
	switch level {
	case 0:
		return 2, nil
	case 1:
		return 2, nil
	case 5:
		return 3, nil
	case 6:
		return 4, nil
	default:
		return 0, fmt.Errorf("unsupported RAID level: %d", level)
	}
}

// ---------------------------------------------------------------------------
// CreateRAIDArray creates a new software RAID array.
// ---------------------------------------------------------------------------

func (s *raidService) CreateRAIDArray(level int, diskPaths []string, name string, chunkKB int) (*model.RAIDArray, error) {
	// 0. Validate name and device paths.
	if !validRAIDName.MatchString(name) {
		return nil, fmt.Errorf("invalid RAID name: %q (only alphanumeric, hyphens, underscores allowed)", name)
	}
	for _, dp := range diskPaths {
		if !isValidDevicePath(dp) {
			return nil, fmt.Errorf("invalid device path: %q", dp)
		}
	}

	// 1. Validate RAID level and disk count.
	min, err := minDisks(level)
	if err != nil {
		return nil, err
	}
	if len(diskPaths) < min {
		return nil, fmt.Errorf("RAID%d requires at least %d disks, got %d", level, min, len(diskPaths))
	}

	// 2. Default chunk size.
	if chunkKB <= 0 {
		chunkKB = 512
	}

	// 3. Find next available md device.
	device, err := mdadm.NextAvailableDevice()
	if err != nil {
		return nil, fmt.Errorf("find available md device: %w", err)
	}

	// 4. Zero superblocks to clear any stale RAID metadata from previous attempts.
	for _, dp := range diskPaths {
		if err := mdadm.ZeroSuperblock(dp); err != nil {
			logger.Info("zero-superblock failed (disk may be clean)", zap.String("disk", dp), zap.Error(err))
		}
	}

	// 5. Create the array.
	if err := mdadm.Create(device, level, diskPaths, chunkKB); err != nil {
		return nil, fmt.Errorf("create RAID array: %w", err)
	}

	// 6. Wait for device to appear (up to 10s).
	if err := waitForDevice(device, 10*time.Second); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("device %s did not appear: %w", device, err)
	}

	// 7. Format with ext4.
	if err := partition.FormatPartition(device); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("format %s: %w", device, err)
	}

	// 8. Create mount point and mount.
	mountPoint := fmt.Sprintf("/media/RAID_%s", name)
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("create mount point %s: %w", mountPoint, err)
	}

	if out, err := exec.Command("mount", device, mountPoint).CombinedOutput(); err != nil {
		_ = os.Remove(mountPoint)
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("mount %s on %s: %w: %s", device, mountPoint, err, string(out))
	}

	// 9. Get UUID from mdadm detail.
	detail, err := mdadm.Detail(device)
	if err != nil {
		_ = exec.Command("umount", mountPoint).Run()
		_ = os.Remove(mountPoint)
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("get array detail: %w", err)
	}

	// 9. Build member disk references with persistent identifiers.
	members := make([]*model.RAIDMember, 0, len(diskPaths))
	for _, dp := range diskPaths {
		ids := diskid.Identify(dp)
		members = append(members, &model.RAIDMember{
			DiskByID:        ids.ByID,
			DiskSerial:      ids.Serial,
			DevicePathCache: ids.DevicePath,
		})
	}

	// 10. Save to DB.
	raid := &model.RAIDArray{
		Name:        name,
		Level:       level,
		DevicePath:  device,
		MountPoint:  mountPoint,
		UUID:        detail.UUID,
		State:       "active",
		ChunkKB:     chunkKB,
		MemberDisks: members,
	}
	if err := s.saveRAID(raid); err != nil {
		_ = exec.Command("umount", mountPoint).Run()
		_ = os.Remove(mountPoint)
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("save RAID to db: %w", err)
	}

	// 11. Save mdadm config for boot persistence.
	if err := mdadm.SaveConfig(); err != nil {
		logger.Error("failed to save mdadm config after create", zap.Error(err))
	}

	return raid, nil
}

// ---------------------------------------------------------------------------
// DeleteRAIDArray tears down a RAID array and removes it from the database.
// ---------------------------------------------------------------------------

func (s *raidService) DeleteRAIDArray(id uint) error {
	// 1. Get RAID from DB.
	raid, err := s.getRAIDByID(id)
	if err != nil {
		return fmt.Errorf("get RAID array: %w", err)
	}

	// 2. Unmount and remove mount directory.
	// Try by mount point, then by device path, with lazy fallback each time.
	for _, target := range []string{raid.MountPoint, raid.DevicePath} {
		if out, err := exec.Command("umount", target).CombinedOutput(); err != nil {
			logger.Info("umount failed, retrying with -l", zap.String("target", target), zap.String("error", err.Error()), zap.String("output", string(out)))
			out2, err2 := exec.Command("umount", "-l", target).CombinedOutput()
			logger.Info("umount -l result", zap.String("target", target), zap.Bool("ok", err2 == nil), zap.String("output", string(out2)))
		}
	}
	_ = os.Remove(raid.MountPoint)

	// 3. Stop the array.
	// Log current mount state to help diagnose if stop still fails.
	if out, err := exec.Command("grep", raid.DevicePath, "/proc/mounts").CombinedOutput(); err == nil && len(out) > 0 {
		logger.Info("device still in /proc/mounts before stop", zap.String("device", raid.DevicePath), zap.String("mounts", string(out)))
	}
	if err := mdadm.Stop(raid.DevicePath); err != nil {
		return fmt.Errorf("failed to stop RAID array: %w", err)
	}

	// 4. Zero superblock on each member disk.
	// Resolve current path before zeroing; warn if disk is offline (can't zero).
	for _, member := range raid.MemberDisks {
		ids := diskid.DiskIdentifiers{
			ByID:       member.DiskByID,
			Serial:     member.DiskSerial,
			DevicePath: member.DevicePathCache,
		}
		path, ok := diskid.Resolve(ids)
		if !ok {
			logger.Info("cannot zero superblock: disk not resolvable (may be offline)",
				zap.String("cache", member.DevicePathCache),
				zap.String("by_id", member.DiskByID))
			continue
		}
		if err := mdadm.ZeroSuperblock(path); err != nil {
			logger.Info("zero superblock failed", zap.String("disk", path), zap.Error(err))
		}
	}

	// 5. Delete from DB.
	if err := s.deleteRAID(id); err != nil {
		return fmt.Errorf("delete RAID from db: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// GetRAIDStatus returns the current status of a RAID array, combining DB
// state with live data from mdadm.
// ---------------------------------------------------------------------------

func (s *raidService) GetRAIDStatus(id uint) (*RAIDStatus, error) {
	// 1. Get RAID from DB.
	raid, err := s.getRAIDByID(id)
	if err != nil {
		return nil, fmt.Errorf("get RAID array: %w", err)
	}

	status := &RAIDStatus{
		RAIDArray:  raid,
		LiveState:  raid.State,
		RebuildPct: -1,
	}

	// 2. Get live state from mdadm.
	detail, err := mdadm.Detail(raid.DevicePath)
	if err != nil {
		logger.Info("mdadm detail failed, returning DB state", zap.String("device", raid.DevicePath), zap.String("error", err.Error()))
		return status, nil
	}

	// 3. Map detail state to DB state.
	liveState := mapMdadmState(detail.State)
	status.LiveState = detail.State
	status.RebuildPct = detail.RebuildPct

	// 4. Update DB if state changed.
	if liveState != raid.State {
		if err := s.updateRAIDState(id, liveState); err != nil {
			logger.Error("failed to update RAID state in db", zap.Uint("id", id), zap.Error(err))
		}
		raid.State = liveState
	}

	// 5. Build live member list.
	for _, m := range detail.Members {
		status.Members = append(status.Members, MemberDiskStatus{
			Path:   m.Path,
			State:  m.State,
			Number: m.Number,
		})
	}

	// 6. Get capacity information from df.
	if err := getRAIDCapacity(raid.MountPoint, status); err != nil {
		logger.Info("failed to get RAID capacity", zap.String("mount", raid.MountPoint), zap.Error(err))
	}

	return status, nil
}

// ---------------------------------------------------------------------------
// ListRAIDArrays returns all RAID arrays from the database.
// ---------------------------------------------------------------------------

func (s *raidService) ListRAIDArrays() ([]*model.RAIDArray, error) {
	return s.listRAIDs()
}

// ---------------------------------------------------------------------------
// ReplaceDisk replaces a failed disk in a RAID array with a new one.
// ---------------------------------------------------------------------------

func (s *raidService) ReplaceDisk(arrayID uint, oldDiskPath, newDiskPath string) error {
	// 0. Validate device paths.
	if !isValidDevicePath(oldDiskPath) {
		return fmt.Errorf("invalid device path: %q", oldDiskPath)
	}
	if !isValidDevicePath(newDiskPath) {
		return fmt.Errorf("invalid device path: %q", newDiskPath)
	}

	// 1. Get RAID from DB.
	raid, err := s.getRAIDByID(arrayID)
	if err != nil {
		return fmt.Errorf("get RAID array: %w", err)
	}

	// 2. Remove old disk.
	if err := mdadm.RemoveDisk(raid.DevicePath, oldDiskPath); err != nil {
		return fmt.Errorf("remove disk %s: %w", oldDiskPath, err)
	}

	// 3. Add new disk.
	if err := mdadm.AddDisk(raid.DevicePath, newDiskPath); err != nil {
		return fmt.Errorf("add disk %s: %w", newDiskPath, err)
	}

	// 4. Update member disk in DB — identify new disk, replace old entry.
	for _, member := range raid.MemberDisks {
		if member.DevicePathCache == oldDiskPath {
			ids := diskid.Identify(newDiskPath)
			member.DiskByID = ids.ByID
			member.DiskSerial = ids.Serial
			member.DevicePathCache = ids.DevicePath
			if err := s.db.Save(member).Error; err != nil {
				return fmt.Errorf("update member disk in db: %w", err)
			}
			break
		}
	}

	// 5. Update state to rebuilding.
	if err := s.updateRAIDState(arrayID, "rebuilding"); err != nil {
		return fmt.Errorf("update RAID state: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// RecoverOnBoot reassembles all known RAID arrays and remounts them.
// Also auto-discovers RAID arrays present on system but not yet in DB.
// ---------------------------------------------------------------------------

func (s *raidService) RecoverOnBoot() error {
	// 1. Assemble all known arrays.
	if err := mdadm.AssembleScan(); err != nil {
		logger.Error("mdadm assemble scan failed", zap.Error(err))
	}

	// 2. Discover all RAID arrays currently on system.
	mdstatEntries, err := mdadm.ReadMDStat()
	if err != nil {
		logger.Error("read mdstat failed on boot", zap.Error(err))
	}

	// 3. For each system RAID, check if in DB; if not, auto-register.
	for _, entry := range mdstatEntries {
		device := "/dev/" + entry.Device

		// Check if this device is already in DB.
		var existing model.RAIDArray
		result := s.db.Where("device_path = ?", device).First(&existing)
		isFound := result.Error == nil

		if !isFound {
			// Auto-register this array.
			detail, err := mdadm.Detail(device)
			if err != nil {
				logger.Error("mdadm detail failed for system array on boot", zap.String("device", device), zap.Error(err))
				continue
			}

			// Parse RAID level from detail.Level (e.g., "raid1" -> 1)
			raidLevel := parseRAIDLevel(detail.Level)

			// Generate name and mount point.
			arrayName := strings.TrimPrefix(entry.Device, "md")
			if arrayName == "" {
				arrayName = "raid"
			}
			mountPoint := fmt.Sprintf("/media/RAID_%s", arrayName)

			newArray := &model.RAIDArray{
				Name:       fmt.Sprintf("RAID%d_%s", raidLevel, arrayName),
				Level:      raidLevel,
				DevicePath: device,
				MountPoint: mountPoint,
				UUID:       detail.UUID,
				State:      mapMdadmState(detail.State),
				ChunkKB:    512, // default value since ArrayDetail doesn't have ChunkKB
			}

			// Get member disks using Volume model with UUID field storing device path.
			for _, member := range detail.Members {
				vol := &model.Volume{
					UUID: member.Path,
				}
				newArray.MemberDisks = append(newArray.MemberDisks, vol)
			}

			// Save to DB.
			if err := s.saveRAID(newArray); err != nil {
				logger.Error("failed to auto-register RAID on boot", zap.String("device", device), zap.Error(err))
				continue
			}

			logger.Info("auto-registered RAID array on boot", zap.String("device", device), zap.String("name", newArray.Name))
		}
	}

	// 4. List all RAIDs from DB.
	raids, err := s.listRAIDs()
	if err != nil {
		return fmt.Errorf("list RAIDs: %w", err)
	}

	// 5. For each DB array: check device, mount, update state.
	for _, raid := range raids {
		if _, err := os.Stat(raid.DevicePath); err != nil {
			logger.Error("RAID device not found on boot", zap.String("device", raid.DevicePath), zap.Error(err))
			_ = s.updateRAIDState(raid.ID, "failed")
			continue
		}

		// Ensure mount directory exists.
		if err := os.MkdirAll(raid.MountPoint, 0o755); err != nil {
			logger.Error("create mount point failed on boot", zap.String("mount", raid.MountPoint), zap.Error(err))
			continue
		}

		// Mount the device.
		if out, err := exec.Command("mount", raid.DevicePath, raid.MountPoint).CombinedOutput(); err != nil {
			logger.Error("mount failed on boot", zap.String("device", raid.DevicePath), zap.String("mount", raid.MountPoint), zap.Error(err), zap.String("output", string(out)))
			_ = s.updateRAIDState(raid.ID, "failed")
			continue
		}

		// Update state from mdadm detail.
		detail, err := mdadm.Detail(raid.DevicePath)
		if err != nil {
			logger.Error("mdadm detail failed on boot", zap.String("device", raid.DevicePath), zap.Error(err))
			continue
		}

		newState := mapMdadmState(detail.State)
		if newState != raid.State {
			if err := s.updateRAIDState(raid.ID, newState); err != nil {
				logger.Error("update RAID state on boot failed", zap.Uint("id", raid.ID), zap.Error(err))
			}
		}
	}

	return nil
}

// parseRAIDLevel converts string level like "raid1" to integer 1.
func parseRAIDLevel(levelStr string) int {
	switch strings.ToLower(levelStr) {
	case "raid0":
		return 0
	case "raid1":
		return 1
	case "raid5":
		return 5
	case "raid6":
		return 6
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// waitForDevice polls for a device to appear in the filesystem.
func waitForDevice(device string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(device); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", device)
}

// mapMdadmState maps mdadm detail state strings to our internal state.
func mapMdadmState(mdadmState string) string {
	lower := strings.ToLower(mdadmState)
	switch {
	case strings.Contains(lower, "recovering") || strings.Contains(lower, "resyncing"):
		return "rebuilding"
	case strings.Contains(lower, "degraded"):
		return "degraded"
	case strings.Contains(lower, "inactive"):
		return "failed"
	default:
		return "active"
	}
}

// getRAIDCapacity retrieves capacity info from df command for a mounted RAID.
func getRAIDCapacity(mountPoint string, status *RAIDStatus) error {
	// Run: df -B 1 <mountPoint> (output in bytes)
	// Format: Filesystem     1B-blocks    Used Available Use% Mounted on
	out, err := exec.Command("df", "-B", "1", mountPoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("df failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return fmt.Errorf("df output has less than 2 lines")
	}

	// Parse the second line (first is header)
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return fmt.Errorf("df output line has less than 4 fields")
	}

	// fields[1] = total, fields[2] = used, fields[3] = available
	total, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parse total: %w", err)
	}

	used, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return fmt.Errorf("parse used: %w", err)
	}

	available, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return fmt.Errorf("parse available: %w", err)
	}

	status.TotalBytes = total
	status.UsedBytes = used
	status.FreeBytes = available

	return nil
}
