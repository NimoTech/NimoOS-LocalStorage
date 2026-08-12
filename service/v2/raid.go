package v2

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
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
	CreateRAIDArray(level int, diskPaths []string, name string, chunkKB int, filesystem string, wipeResidue bool, onStep func(int)) (*model.RAIDArray, error)
	DeleteRAIDArray(id uint) error
	GetRAIDStatus(id uint) (*RAIDStatus, error)
	GetRAIDUsage(id uint) (*RAIDUsage, error)
	EnsureFilesystemResized(id uint) error
	ListRAIDArrays() ([]*model.RAIDArray, error)
	ReplaceDisk(arrayID uint, oldDiskPath, oldDiskSerial, newDiskPath string, wipeResidue bool) error
	RecoverOnBoot() error
	Recover(id uint) (string, []string, error)
}

// RAIDStatus extends the DB model with live state from mdadm.
type RAIDStatus struct {
	*model.RAIDArray
	LiveState     string             `json:"live_state"`
	RebuildPct    float64            `json:"rebuild_pct"`
	RebuildFinish string             `json:"rebuild_finish"`
	RebuildSpeed  string             `json:"rebuild_speed"`
	// RebuildEtaSeconds estimates the remaining time from the rebuild's
	// *position* advance rate across status polls. The kernel's finish=
	// (RebuildFinish) counts only copied blocks and balloons to weeks during
	// bitmap delta resyncs — clients should prefer this field. -1 = unknown
	// (no rebuild, or not enough samples yet).
	RebuildEtaSeconds int64 `json:"rebuild_eta_seconds"`
	TotalBytes    int64              `json:"total_bytes"` // total capacity in bytes
	UsedBytes     int64              `json:"used_bytes"`  // used capacity in bytes
	FreeBytes     int64              `json:"free_bytes"`  // available capacity in bytes
	Members       []MemberDiskStatus `json:"members"`
	// Reattachable lists this array's own member disks that are present in
	// the system but not attached (pulled from the running array and plugged
	// back). The Recover endpoint reclaims them via mdadm --re-add.
	Reattachable []ReattachMember `json:"reattachable_members,omitempty"`
}

// MemberDiskStatus represents the live state of a single member disk.
type MemberDiskStatus struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	// Serial identifies the disk independently of its device path, which can
	// be reused by a different disk after a hot swap. Empty for placeholder
	// rows (removed disks) or when the serial cannot be read.
	Serial string `json:"serial,omitempty"`
	Number int    `json:"number"`
	// Slot is the array slot this entry occupies, or -1 for none (ejected faulty
	// disk / idle spare). Clients need it to count array slots rather than rows:
	// a degraded 3-disk RAID 5 yields 4 rows (vacated slot + ejected disk).
	Slot int `json:"slot"`
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
	usageMu      sync.Mutex
	usageCache   map[uint]cachedBtrfsUsage
	eta          *etaTracker
}

// NewRAIDService creates a new RAIDService backed by the given database.
func NewRAIDService(db *gorm.DB) RAIDService {
	if err := mdadm.CheckSupport(); err != nil {
		logger.Info("mdadm is unavailable at startup; RAID features will fail", zap.Error(err))
	}

	if err := checkBtrfsSupport(); err != nil {
		logger.Info("btrfs tooling is unavailable at startup; btrfs create/usage features may fail", zap.Error(err))
	}

	return &raidService{
		db:           db,
		retryCancels: make(map[uint]context.CancelFunc),
		usageCache:   make(map[uint]cachedBtrfsUsage),
		eta:          newEtaTracker(),
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
	// Need to check what column GORM uses for the foreign key.
	// Since model.RAIDMember defines `RAIDArrayID`, gorm will map it to `raid_array_id`.
	// But just to be safe, we can use the model itself with GORM's association to delete.
	raid := &model.RAIDArray{ID: id}
	if err := s.db.Model(raid).Association("MemberDisks").Clear(); err != nil {
		logger.Error("failed to clear member disks association", zap.Error(err))
	}

	// Fallback direct delete in case the clear didn't actually delete the rows
	if err := s.db.Where("raid_array_id = ?", id).Delete(&model.RAIDMember{}).Error; err != nil {
		// Ignore "no such column" errors if the schema somehow doesn't match what we expect,
		// as the main goal is to delete the RAID array itself.
		if !strings.Contains(err.Error(), "no such column") {
			return fmt.Errorf("delete member records: %w", err)
		}
		logger.Info("ignored 'no such column' error when deleting members", zap.Error(err))
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
	case 10:
		return 4, nil
	default:
		return 0, fmt.Errorf("unsupported RAID level: %d", level)
	}
}

// ---------------------------------------------------------------------------
// CreateRAIDArray creates a new software RAID array.
// ---------------------------------------------------------------------------

func (s *raidService) CreateRAIDArray(level int, diskPaths []string, name string, chunkKB int, filesystem string, wipeResidue bool, onStep func(int)) (*model.RAIDArray, error) {
	step := func(n int) {
		if onStep != nil {
			onStep(n)
		}
	}
	step(1)
	// 0. Ensure RAID modules are loaded.
	if err := mdadm.EnsureModuleLoaded(level); err != nil {
		logger.Error("failed to ensure RAID modules are loaded (will attempt create anyway)", zap.Error(err))
	}

	// 1. Validate name and device paths.
	if !validRAIDName.MatchString(name) {
		return nil, fmt.Errorf("invalid RAID name: %q (only alphanumeric, hyphens, underscores allowed)", name)
	}
	for _, dp := range diskPaths {
		if !isValidDevicePath(dp) {
			return nil, fmt.Errorf("invalid device path: %q", dp)
		}
	}

	fs, err := normalizeCreateFilesystem(filesystem)
	if err != nil {
		return nil, err
	}
	if fs == "btrfs" {
		if err := checkBtrfsSupport(); err != nil {
			return nil, err
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

	if level == 10 && len(diskPaths)%2 != 0 {
		return nil, fmt.Errorf("RAID 10 requires an even number of disks, got %d", len(diskPaths))
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

	// 3.5. RAID traces on the chosen disks: members of this system's arrays
	// are refused outright; foreign residue needs the explicit wipe flag the
	// UI sets after its confirmation dialog.
	for _, dp := range diskPaths {
		if err := raidTraceGuard(dp, s.findDiskRaidTrace(dp), wipeResidue); err != nil {
			return nil, err
		}
	}

	step(2)
	// 4. Prepare member disks: unmount their partitions and wipe stale
	//    partition tables / filesystem signatures. Without this, a disk with a
	//    mounted partition makes mdadm fail with "Device or resource busy", and
	//    stale GPT/FS signatures survive onto the member disks (verified live:
	//    an old ext4 signature on a member leaked into the new md device).
	if err := s.prepareMemberDisks(diskPaths); err != nil {
		return nil, err
	}

	// 4. Zero superblocks to clear any stale RAID metadata from previous attempts.
	for _, dp := range diskPaths {
		if err := mdadm.ZeroSuperblock(dp); err != nil {
			logger.Info("zero-superblock failed (disk may be clean)", zap.String("disk", dp), zap.Error(err))
		}
	}

	step(3)
	// 5. Create the array.
	if err := mdadm.Create(device, level, diskPaths, chunkKB); err != nil {
		return nil, fmt.Errorf("create RAID array: %w", err)
	}

	// 6. Wait for device to appear (up to 10s).
	if err := waitForDevice(device, 10*time.Second); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("device %s did not appear: %w", device, err)
	}

	step(4)
	// 7. Clear stale signatures then format with requested filesystem.
	if err := wipeDeviceSignatures(device); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("wipefs %s: %w", device, err)
	}

	if err := partition.FormatDevice(device, fs); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("format %s as %s: %w", device, fs, err)
	}

	step(5)
	// 8. Create mount point and mount.
	mountPoint := fmt.Sprintf("/media/RAID_%s", name)
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("create mount point %s: %w", mountPoint, err)
	}

	if fs == "btrfs" {
		if err := btrfsSetupSubvolumes(device); err != nil {
			_ = os.Remove(mountPoint)
			_ = mdadm.Stop(device)
			return nil, fmt.Errorf("setup btrfs subvolumes: %w", err)
		}
	}

	if err := mountRAIDDevice(device, mountPoint, fs); err != nil {
		_ = os.Remove(mountPoint)
		_ = mdadm.Stop(device)
		return nil, fmt.Errorf("mount %s on %s: %w", device, mountPoint, err)
	}
	// disabled: applying no_cow_policy to the btrfs directory
	// if fs == "btrfs" {
	// 	applyNoCoWPolicy(mountPoint)
	// }

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
		Filesystem:  fs,
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

	step(6)
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

	// 2. Refuse if this RAID still hosts a system data location (Docker
	// images, app data, user database). Those are managed by NimoOS path
	// migration and must be moved off the RAID before it can be deleted.
	if items := detectSystemPathsOnRAID(raid.MountPoint); len(items) > 0 {
		return fmt.Errorf("%s", formatSystemPathConflict(raid.MountPoint, items))
	}

	// 3. Unmount and remove mount directory.
	// Submounts (e.g. orphan Docker/containerd overlays left over from an
	// earlier path migration) keep the parent mount busy. We unmount the
	// deepest first so each parent becomes free before we get to it. We DO
	// NOT use lazy unmount (-l) because it hides the busy state from umount
	// but leaves the block device open, causing mdadm.Stop to fail later.
	if children := collectChildMounts(raid.MountPoint); len(children) > 0 {
		// Sort by descending depth so /a/b/c is unmounted before /a/b.
		sort.Slice(children, func(i, j int) bool {
			return strings.Count(children[i], "/") > strings.Count(children[j], "/")
		})
		for _, child := range children {
			if outUmount, errUmount := exec.Command("umount", child).CombinedOutput(); errUmount != nil {
				return fmt.Errorf("%s", formatBusyErrorForSubmount(raid.MountPoint, child, string(outUmount)))
			}
		}
	}

	for _, target := range []string{raid.MountPoint, raid.DevicePath} {
		// Search with space boundaries to avoid matching /dev/md01 when looking for /dev/md0
		grepCmd := fmt.Sprintf("grep ' %s ' /proc/mounts || grep '^%s ' /proc/mounts", target, target)
		out, err := exec.Command("sh", "-c", grepCmd).CombinedOutput()
		if err != nil || len(out) == 0 {
			continue // Not currently mounted
		}

		// Attempt to unmount normally.
		if outUmount, errUmount := exec.Command("umount", target).CombinedOutput(); errUmount != nil {
			return fmt.Errorf("%s", formatBusyError(target, string(outUmount)))
		}
	}
	_ = os.Remove(raid.MountPoint)

	// 4. Deactivate any LVM volume group that uses this device as a PV.
	// pvs exits 5 when the device isn't a PV, so we check stdout instead.
	if pvOut, _ := exec.Command("pvs", "--noheadings", "-o", "vg_name", raid.DevicePath).CombinedOutput(); len(strings.TrimSpace(string(pvOut))) > 0 {
		vgName := strings.TrimSpace(string(pvOut))
		logger.Info("deactivating LVM VG before stopping RAID", zap.String("vg", vgName), zap.String("device", raid.DevicePath))
		if out, err := exec.Command("vgchange", "-an", vgName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to deactivate LVM VG %s on %s: %s", vgName, raid.DevicePath, strings.TrimSpace(string(out)))
		}
	}

	// 5. Stop the array.
	// Log current mount state to help diagnose if stop still fails.
	if out, err := exec.Command("grep", raid.DevicePath, "/proc/mounts").CombinedOutput(); err == nil && len(out) > 0 {
		logger.Info("device still in /proc/mounts before stop", zap.String("device", raid.DevicePath), zap.String("mounts", string(out)))
	}
	if err := mdadm.Stop(raid.DevicePath); err != nil {
		// If mdadm says the device doesn't exist or isn't an md device, it's already gone/stopped.
		// We can safely ignore this error and proceed to delete from DB.
		errStr := err.Error()
		if !strings.Contains(errStr, "No such file or directory") &&
			!strings.Contains(errStr, "does not appear to be an md device") &&
			!strings.Contains(errStr, "not found") {
			return fmt.Errorf("failed to stop RAID array: %w", err)
		}
		logger.Info("device already missing or not an md device, continuing with deletion", zap.String("device", raid.DevicePath))
	}

	// 6. Zero superblock on each member disk.
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

	// 7. Delete from DB.
	if err := s.deleteRAID(id); err != nil {
		return fmt.Errorf("delete RAID from db: %w", err)
	}

	// 8. Drop the boot-time persistence created alongside the array: the
	// @snapshots line in /etc/fstab and the ARRAY line in /etc/mdadm/mdadm.conf.
	// Runs last on purpose — mdadm.conf is regenerated from the live arrays, so
	// the array has to be stopped (step 5) before it can be dropped from it.
	cleanupRAIDPersistence(raid.MountPoint)

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
		RAIDArray:         raid,
		LiveState:         raid.State,
		RebuildPct:        -1,
		RebuildEtaSeconds: -1,
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

	// Fetch mdstat for finish time and speed
	mdstatEntries, mdstatErr := mdadm.ReadMDStat()
	if mdstatErr == nil {
		mdName := strings.TrimPrefix(raid.DevicePath, "/dev/")
		for _, e := range mdstatEntries {
			if e.Device == mdName {
				status.RebuildFinish = e.RebuildFinish
				status.RebuildSpeed = e.RebuildSpeed
				if e.RebuildPct >= 0 {
					status.RebuildPct = e.RebuildPct
				}
				if e.RebuildPos > 0 && e.RebuildTotal > 0 {
					status.RebuildEtaSeconds = s.eta.Observe(mdName, e.RebuildPos, e.RebuildTotal)
				} else {
					s.eta.Forget(mdName)
				}
				break
			}
		}
	}

	// 4. Update DB if state changed.
	if liveState != raid.State {
		if err := s.updateRAIDState(id, liveState); err != nil {
			logger.Error("failed to update RAID state in db", zap.Uint("id", id), zap.Error(err))
		}
		raid.State = liveState
	}

	// 5. Build live member list. One lsblk call covers every member; the
	// per-device Identify fallback only runs if that call failed outright.
	serials := diskid.SerialMap()
	for _, m := range detail.Members {
		serial := ""
		if m.Path != "" {
			if serials != nil {
				serial = serials[m.Path]
			} else {
				serial = diskid.Identify(m.Path).Serial
			}
		}
		status.Members = append(status.Members, MemberDiskStatus{
			Path:   m.Path,
			State:  m.State,
			Serial: serial,
			Number: m.Number,
			Slot:   m.Slot,
		})
	}

	// 6. Get capacity information from df.
	if err := getRAIDCapacity(raid.MountPoint, status); err != nil {
		logger.Info("failed to get RAID capacity", zap.String("mount", raid.MountPoint), zap.Error(err))
	}

	// 7. Degraded array: check whether its own missing members are actually
	// sitting in the machine, kicked but plugged back — the UI offers a
	// one-click reclaim (Recover) for those.
	if len(attachedMembers(detail.Members)) < detail.TotalDisks {
		status.Reattachable = s.detachedMembers(raid.UUID, detail)
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

func (s *raidService) ReplaceDisk(arrayID uint, oldDiskPath, oldDiskSerial, newDiskPath string, wipeResidue bool) error {
	// 0. Validate input. The old disk may be identified by serial alone —
	// after a hot swap its stored device path may already belong to the
	// replacement disk (device letters get reused), so the path is optional
	// and never trusted on its own when a serial is available.
	if oldDiskPath != "" && !isValidDevicePath(oldDiskPath) {
		return fmt.Errorf("invalid device path: %q", oldDiskPath)
	}
	if !isValidDevicePath(newDiskPath) {
		return fmt.Errorf("invalid device path: %q", newDiskPath)
	}
	if strings.HasPrefix(newDiskPath, "/dev/md") {
		return fmt.Errorf("new disk must be a physical disk, not an md device: %q", newDiskPath)
	}
	if oldDiskPath == "" && oldDiskSerial == "" {
		return fmt.Errorf("old disk path or serial is required")
	}

	// 0.5. RAID traces on the new disk: another array's member is refused,
	// foreign residue needs the explicit wipe confirmation flag.
	if err := raidTraceGuard(newDiskPath, s.findDiskRaidTrace(newDiskPath), wipeResidue); err != nil {
		return err
	}

	// 1. Get RAID from DB.
	raid, err := s.getRAIDByID(arrayID)
	if err != nil {
		return fmt.Errorf("get RAID array: %w", err)
	}

	// 2. Establish what is actually attached to the array right now.
	detail, err := mdadm.Detail(raid.DevicePath)
	if err != nil {
		return fmt.Errorf("mdadm detail %s: %w", raid.DevicePath, err)
	}
	attached := attachedMembers(detail.Members)
	serialByPath := make(map[string]string, len(attached))
	for _, m := range attached {
		serialByPath[m.Path] = diskid.Identify(m.Path).Serial
	}

	// 3. Refuse to touch a disk that is already an array member as "new".
	newIDs := diskid.Identify(newDiskPath)
	for _, m := range attached {
		if m.Path == newDiskPath || (newIDs.Serial != "" && serialByPath[m.Path] == newIDs.Serial) {
			return fmt.Errorf("disk %s is already a member of the array", newDiskPath)
		}
	}

	// 4. Sweep the new disk exactly like array creation does: unmount its
	// partitions and wipe stale partition-table / filesystem / md signatures,
	// which otherwise survive onto the member (verified live during create).
	// Runs before any change to the array so a refused sweep leaves it intact.
	if err := s.prepareMemberDisk(newDiskPath); err != nil {
		return fmt.Errorf("prepare new disk %s: %w", newDiskPath, err)
	}
	if err := mdadm.ZeroSuperblock(newDiskPath); err != nil {
		logger.Info("zero-superblock on new disk failed (disk may be clean)",
			zap.String("disk", newDiskPath), zap.Error(err))
	}

	// 5. Remove the old disk only if it is still attached (present but
	// faulty). A pulled disk is already gone from mdadm's point of view.
	matches := findOldDiskMatches(attached, serialByPath, oldDiskSerial, oldDiskPath)
	switch {
	case len(matches) > 1:
		return fmt.Errorf("%d attached disks share serial %q; refusing to pick one (duplicate serials)",
			len(matches), oldDiskSerial)
	case len(matches) == 1:
		old := matches[0]
		// A serial-only match on a healthy active member almost certainly
		// means the pulled disk's serial is duplicated by a twin. Yanking an
		// active member needs an explicit path confirmation.
		if strings.HasPrefix(old.State, "active") && old.Path != oldDiskPath {
			return fmt.Errorf("disk %s matches serial %q but is an active member; refusing to remove it (duplicate serial?)",
				old.Path, oldDiskSerial)
		}
		if err := mdadm.RemoveDisk(raid.DevicePath, old.Path); err != nil {
			return fmt.Errorf("remove disk %s: %w", old.Path, err)
		}
	default:
		logger.Info("old disk no longer attached, skipping --fail --remove",
			zap.String("path", oldDiskPath), zap.String("serial", oldDiskSerial))
	}

	// 6. Add new disk.
	if err := mdadm.AddDisk(raid.DevicePath, newDiskPath); err != nil {
		return fmt.Errorf("add disk %s: %w", newDiskPath, err)
	}

	// 7. Update member disk in DB — identify new disk, replace old entry.
	if member := memberRowToReplace(raid.MemberDisks, oldDiskSerial, oldDiskPath); member != nil {
		member.DiskByID = newIDs.ByID
		member.DiskSerial = newIDs.Serial
		member.DevicePathCache = newIDs.DevicePath
		if err := s.db.Save(member).Error; err != nil {
			return fmt.Errorf("update member disk in db: %w", err)
		}
	}

	// 8. Update state to rebuilding.
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
	s.normalizeUnknownFilesystems()

	// 1. Assemble all known arrays from mdadm.conf superblocks.
	mdadm.AssembleScan()

	// 2. Build UUID → device path map from currently active md devices.
	uuidToDevice, err := buildUUIDMap()
	if err != nil {
		logger.Error("failed to build UUID map on boot", zap.Error(err))
		uuidToDevice = map[string]string{}
	}

	// 3. Auto-register arrays found on system but not in DB.
	for uuid, device := range uuidToDevice {
		var existing model.RAIDArray
		found := s.db.Preload("MemberDisks").Where("uuid = ?", uuid).First(&existing).Error == nil
		if found {
			// Update stale device path cache (md number may have changed).
			if existing.DevicePath != device {
				s.db.Model(&existing).Update("device_path", device)
			}
			// Refresh member caches asynchronously.
			existing.DevicePath = device
			go s.refreshMemberCaches(&existing)
			continue
		}

		// New array: collect metadata and register.
		detail, err := mdadm.Detail(device)
		if err != nil {
			logger.Error("mdadm detail failed for new array on boot", zap.String("device", device), zap.Error(err))
			continue
		}

		// Register only arrays that actually run here. udev assembles any
		// hot-plugged disk with a superblock into an inactive md (e.g. one
		// leftover disk of a foreign 4-disk array) — registering that
		// creates a phantom array that can never start, and marks the disk
		// a protected member, blocking the residue-wipe flow that would let
		// the user reuse it. A complete array moved from another machine
		// assembles *active* (possibly degraded) and still registers.
		if strings.Contains(strings.ToLower(detail.State), "inactive") {
			logger.Info("skipping auto-registration of inactive array (foreign leftover?)",
				zap.String("device", device), zap.String("name", detail.Name), zap.String("state", detail.State))
			continue
		}

		arrayName := parseMdadmName(detail.Name, uuid)
		mountPoint := fmt.Sprintf("/media/RAID_%s", arrayName)
		filesystem, fsErr := detectFilesystemByDevice(device)
		if fsErr != nil {
			logger.Info("failed to detect filesystem for auto-registered RAID", zap.String("device", device), zap.Error(fsErr))
		}
		newArray := &model.RAIDArray{
			Name:       arrayName,
			Level:      parseRAIDLevel(detail.Level),
			Filesystem: filesystem,
			DevicePath: device,
			MountPoint: mountPoint,
			UUID:       uuid,
			State:      mapMdadmState(detail.State),
			ChunkKB:    512,
		}
		for _, m := range detail.Members {
			// A degraded array's vacated slot is a pathless placeholder row —
			// registering it would create a member with no identity at all,
			// which nothing (UI missing-disk detection, replace, delete) can
			// ever match again.
			if m.Path == "" {
				continue
			}
			ids := diskid.Identify(m.Path)
			newArray.MemberDisks = append(newArray.MemberDisks, &model.RAIDMember{
				DiskByID:        ids.ByID,
				DiskSerial:      ids.Serial,
				DevicePathCache: ids.DevicePath,
			})
		}
		if err := s.saveRAID(newArray); err != nil {
			logger.Error("failed to auto-register RAID on boot", zap.String("device", device), zap.Error(err))
		} else {
			logger.Info("auto-registered RAID array on boot", zap.String("device", device), zap.String("name", arrayName))
		}
	}

	// 4. Mount all DB arrays.
	raids, err := s.listRAIDs()
	if err != nil {
		return fmt.Errorf("list RAIDs: %w", err)
	}

	for _, raid := range raids {
		device, assembled := uuidToDevice[raid.UUID]
		if !assembled {
			// md device not present — disks may be missing or not yet connected.
			logger.Info("RAID not assembled on boot, scheduling retry", zap.String("uuid", raid.UUID))
			_ = s.updateRAIDState(raid.ID, "retrying")
			s.startRetryWorker(raid.ID, raid.UUID)
			continue
		}

		// Update device path cache if md number changed.
		if device != raid.DevicePath {
			s.db.Model(raid).Update("device_path", device)
			raid.DevicePath = device
		}

		if err := os.MkdirAll(raid.MountPoint, 0o755); err != nil {
			logger.Error("create mount point failed on boot", zap.String("mount", raid.MountPoint), zap.Error(err))
			_ = s.updateRAIDState(raid.ID, "retrying")
			s.startRetryWorker(raid.ID, raid.UUID)
			continue
		}

		filesystem, err := s.resolveFilesystemForDevice(raid, device)
		if err != nil {
			logger.Error("resolve filesystem failed on boot", zap.String("device", device), zap.Uint("raid_id", raid.ID), zap.Error(err))
			_ = s.updateRAIDState(raid.ID, "retrying")
			s.startRetryWorker(raid.ID, raid.UUID)
			continue
		}

		// Mount without requiring all members to resolve —
		// degraded arrays are already handled by mdadm internally.
		// Treat "already mounted" as success (idempotent).
		if err := mountRAIDDevice(device, raid.MountPoint, filesystem); err != nil {
			if !strings.Contains(err.Error(), "already mounted") {
				logger.Error("mount failed on boot", zap.String("device", device), zap.Error(err))
				_ = s.updateRAIDState(raid.ID, "retrying")
				s.startRetryWorker(raid.ID, raid.UUID)
				continue
			}
		}

		// Make the mount root writable by all NimoOS users.
		// The filesystem root is owned by root after a fresh mkfs; chmod 0777 so that
		// the logged-in NimoOS user (e.g. admin) can create directories and migrate data.
		if err := os.Chmod(raid.MountPoint, 0o777); err != nil {
			logger.Error("chmod mount point failed", zap.String("mount", raid.MountPoint), zap.Error(err))
		}

		// Sync live state from mdadm.
		if detail, err := mdadm.Detail(device); err == nil {
			_ = s.updateRAIDState(raid.ID, mapMdadmState(detail.State))
		}
		go s.refreshMemberCaches(raid)
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
	case "raid10":
		return 10
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
	// FAILED (too many members lost, e.g. "clean, FAILED") and broken (raid0
	// with a missing member, mdadm >= 4.1) mean the array can no longer serve
	// data — check before the degraded/recovering substrings so a dead array
	// is never reported as merely degraded or, worse, active.
	case strings.Contains(lower, "failed") || strings.Contains(lower, "broken"):
		return "failed"
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

// buildUUIDMap reads /proc/mdstat and returns a map of mdadm UUID → device path.
// e.g. {"3e316422:d065eb05:f8547081:cb43b63d": "/dev/md0"}
func buildUUIDMap() (map[string]string, error) {
	entries, err := mdadm.ReadMDStat()
	if err != nil {
		return nil, fmt.Errorf("read mdstat: %w", err)
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		device := "/dev/" + entry.Device
		detail, err := mdadm.Detail(device)
		if err != nil {
			logger.Info("mdadm detail failed during UUID map build", zap.String("device", device), zap.Error(err))
			continue
		}
		if detail.UUID != "" {
			result[detail.UUID] = device
		}
	}
	return result, nil
}

// parseMdadmName extracts the array name from mdadm detail Name field.
// Format is "hostname:arrayname"; returns "arrayname".
// Falls back to the first 8 characters of uuid to guarantee uniqueness
// when two or more arrays have no valid name (prevents mount point conflicts).
func parseMdadmName(name string, uuid string) string {
	// Strip mdadm's trailing parenthetical comment, e.g. "(local to host hostname)"
	if idx := strings.Index(name, "("); idx >= 0 {
		name = name[:idx]
	}
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)
	// Sanitize: keep only alphanumeric, hyphens, underscores
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if result := b.String(); result != "" {
		return result
	}
	// Fallback: use UUID prefix to guarantee uniqueness
	if len(uuid) >= 8 {
		return uuid[:8]
	}
	return "raid"
}

// refreshMemberCaches updates DevicePathCache for all members of a RAID array.
// Called asynchronously after a successful mount.
func (s *raidService) refreshMemberCaches(raid *model.RAIDArray) {
	for _, member := range raid.MemberDisks {
		ids := diskid.DiskIdentifiers{
			ByID:       member.DiskByID,
			Serial:     member.DiskSerial,
			DevicePath: member.DevicePathCache,
		}
		if path, ok := diskid.Resolve(ids); ok && path != member.DevicePathCache {
			s.updateMemberCache(member.ID, path)
		}
	}
}

const (
	retryInterval = 30 * time.Second
	// 10 × 30s: slow-spinning USB enclosures regularly need more than the
	// 2.5 minutes the previous 5 attempts allowed before their disks appear.
	maxRetries = 10
)

// startRetryWorker launches a background goroutine that periodically tries to
// assemble and mount a RAID array. Each array gets its own goroutine + context
// so they can be independently cancelled (e.g. on manual recover).
func (s *raidService) startRetryWorker(arrayID uint, arrayUUID string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.retryMu.Lock()
	if old, exists := s.retryCancels[arrayID]; exists {
		old() // cancel any existing worker for this array
	}
	s.retryCancels[arrayID] = cancel
	s.retryMu.Unlock()

	go func() {
		defer func() {
			s.retryMu.Lock()
			delete(s.retryCancels, arrayID)
			s.retryMu.Unlock()
		}()

		for attempt := 1; attempt <= maxRetries; attempt++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
			}

			// Actively try to assemble — required for hot-plugged disks.
			mdadm.AssembleScan()

			uuidToDevice, err := buildUUIDMap()
			if err != nil {
				continue
			}
			device, assembled := uuidToDevice[arrayUUID]
			if !assembled {
				logger.Info("retry: array not yet assembled", zap.Int("attempt", attempt), zap.String("uuid", arrayUUID))
				continue
			}

			raid, err := s.getRAIDByUUID(arrayUUID)
			if err != nil {
				continue
			}
			if device != raid.DevicePath {
				s.db.Model(raid).Update("device_path", device)
				raid.DevicePath = device
			}

			_ = os.MkdirAll(raid.MountPoint, 0o755)

			filesystem, err := s.resolveFilesystemForDevice(raid, device)
			if err != nil {
				logger.Info("retry resolve filesystem failed", zap.Int("attempt", attempt), zap.String("uuid", arrayUUID), zap.Error(err))
				continue
			}

			if err := mountRAIDDevice(device, raid.MountPoint, filesystem); err != nil {
				if !strings.Contains(err.Error(), "already mounted") {
					logger.Info("retry mount failed", zap.Int("attempt", attempt), zap.String("error", err.Error()))
					continue
				}
			}

			// Success.
			if detail, err := mdadm.Detail(device); err == nil {
				_ = s.updateRAIDState(raid.ID, mapMdadmState(detail.State))
			} else {
				_ = s.updateRAIDState(raid.ID, "active")
			}
			go s.refreshMemberCaches(raid)
			logger.Info("retry mount succeeded", zap.Int("attempt", attempt), zap.String("uuid", arrayUUID))
			return
		}

		// All retries exhausted.
		if raid, err := s.getRAIDByUUID(arrayUUID); err == nil {
			_ = s.updateRAIDState(raid.ID, "failed")
			logger.Info("retry exhausted, marking failed", zap.String("uuid", arrayUUID))
		}
	}()
}

// Recover manually triggers reassembly and mount for a single RAID array.
// Cancels any running retry goroutine, attempts immediately, then re-schedules
// retry if the attempt fails.
func (s *raidService) Recover(id uint) (string, []string, error) {
	raid, err := s.getRAIDByID(id)
	if err != nil {
		return "", nil, fmt.Errorf("get RAID: %w", err)
	}

	// Cancel existing retry goroutine for this array.
	s.retryMu.Lock()
	if cancel, exists := s.retryCancels[id]; exists {
		cancel()
		delete(s.retryCancels, id)
	}
	s.retryMu.Unlock()

	// Try to assemble.
	mdadm.AssembleScan()

	uuidToDevice, err := buildUUIDMap()
	if err != nil || uuidToDevice[raid.UUID] == "" {
		_ = s.updateRAIDState(id, "retrying")
		s.startRetryWorker(id, raid.UUID)
		return "retrying", nil, nil
	}

	device := uuidToDevice[raid.UUID]
	if device != raid.DevicePath {
		s.db.Model(raid).Update("device_path", device)
		raid.DevicePath = device
	}

	_ = os.MkdirAll(raid.MountPoint, 0o755)

	filesystem, err := s.resolveFilesystemForDevice(raid, device)
	if err != nil {
		logger.Info("manual recover resolve filesystem failed", zap.String("device", device), zap.Error(err))
		_ = s.updateRAIDState(id, "retrying")
		s.startRetryWorker(id, raid.UUID)
		return "retrying", nil, nil
	}

	if err := mountRAIDDevice(device, raid.MountPoint, filesystem); err != nil {
		if !strings.Contains(err.Error(), "already mounted") {
			logger.Info("manual recover mount failed", zap.String("error", err.Error()))
			_ = s.updateRAIDState(id, "retrying")
			s.startRetryWorker(id, raid.UUID)
			return "retrying", nil, nil
		}
	}

	// Reclaim kicked members that are plugged back in: a member pulled from
	// a *running* array falls behind on events and udev won't hot re-add it,
	// so the array stays degraded even though its own disk is present.
	// --re-add rides the write-intent bitmap (delta resync).
	readded := s.reattachDetachedMembers(device, raid.UUID)

	var state string
	if detail, err := mdadm.Detail(device); err == nil {
		state = mapMdadmState(detail.State)
	} else {
		state = "active"
	}
	_ = s.updateRAIDState(id, state)
	go s.refreshMemberCaches(raid)
	return state, readded, nil
}
