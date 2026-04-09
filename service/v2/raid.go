package v2

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
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
	Members    []MemberDiskStatus `json:"members"`
}

// MemberDiskStatus represents the live state of a single member disk.
type MemberDiskStatus struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Number int    `json:"number"`
}

type raidService struct {
	db *gorm.DB
}

// NewRAIDService creates a new RAIDService backed by the given database.
func NewRAIDService(db *gorm.DB) RAIDService {
	return &raidService{db: db}
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

func (s *raidService) listRAIDs() ([]*model.RAIDArray, error) {
	var raids []*model.RAIDArray
	if err := s.db.Preload("MemberDisks").Find(&raids).Error; err != nil {
		return nil, err
	}
	return raids, nil
}

func (s *raidService) deleteRAID(id uint) error {
	r := &model.RAIDArray{ID: id}
	if err := s.db.Model(r).Association("MemberDisks").Clear(); err != nil {
		return fmt.Errorf("clear member disk association: %w", err)
	}
	return s.db.Delete(r).Error
}

func (s *raidService) updateRAIDState(id uint, state string) error {
	return s.db.Model(&model.RAIDArray{}).Where("id = ?", id).Update("state", state).Error
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

	// 4. Create the array.
	if err := mdadm.Create(device, level, diskPaths, chunkKB); err != nil {
		return nil, fmt.Errorf("create RAID array: %w", err)
	}

	// 5. Wait for device to appear (up to 10s).
	if err := waitForDevice(device, 10*time.Second); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("device %s did not appear: %w", device, err)
	}

	// 6. Format with ext4.
	if err := partition.FormatPartition(device); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("format %s: %w", device, err)
	}

	// 7. Create mount point and mount.
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

	// 8. Get UUID from mdadm detail.
	detail, err := mdadm.Detail(device)
	if err != nil {
		_ = exec.Command("umount", mountPoint).Run()
		_ = os.Remove(mountPoint)
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("get array detail: %w", err)
	}

	// 9. Build member disk references.
	members := make([]*model.Volume, 0, len(diskPaths))
	for _, dp := range diskPaths {
		members = append(members, &model.Volume{
			UUID:       dp,
			MountPoint: mountPoint,
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
	if out, err := exec.Command("umount", raid.MountPoint).CombinedOutput(); err != nil {
		logger.Info("umount failed (may already be unmounted)", zap.String("mount", raid.MountPoint), zap.String("error", err.Error()), zap.String("output", string(out)))
	}
	_ = os.Remove(raid.MountPoint)

	// 3. Stop the array.
	if err := mdadm.Stop(raid.DevicePath); err != nil {
		logger.Info("mdadm stop failed", zap.String("device", raid.DevicePath), zap.String("error", err.Error()))
	}

	// 4. Zero superblock on each member disk.
	for _, member := range raid.MemberDisks {
		if err := mdadm.ZeroSuperblock(member.UUID); err != nil {
			logger.Info("zero superblock failed", zap.String("disk", member.UUID), zap.String("error", err.Error()))
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
	status.LiveState = liveState
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

	// 4. Update member disk in DB.
	for _, member := range raid.MemberDisks {
		if member.UUID == oldDiskPath {
			member.UUID = newDiskPath
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
// ---------------------------------------------------------------------------

func (s *raidService) RecoverOnBoot() error {
	// 1. Assemble all known arrays.
	if err := mdadm.AssembleScan(); err != nil {
		logger.Error("mdadm assemble scan failed", zap.Error(err))
	}

	// 2. List all RAIDs from DB.
	raids, err := s.listRAIDs()
	if err != nil {
		return fmt.Errorf("list RAIDs: %w", err)
	}

	// 3. For each array: check device, mount, update state.
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
	case strings.Contains(lower, "degraded"):
		return "degraded"
	case strings.Contains(lower, "recovering"):
		return "rebuilding"
	case strings.Contains(lower, "inactive"):
		return "failed"
	default:
		return "active"
	}
}
