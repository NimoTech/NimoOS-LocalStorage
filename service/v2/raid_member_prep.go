package v2

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"go.uber.org/zap"
)

// memberBlockDevice is the minimal subset of `lsblk -J` output this task
// cares about when planning how to sweep a RAID member disk clean.
type memberBlockDevice struct {
	Name       string              `json:"name"`
	Path       string              `json:"path"`
	Type       string              `json:"type"`
	MountPoint string              `json:"mountpoint"`
	Children   []memberBlockDevice `json:"children"`
}

// memberPrepPlan is the sweep plan for a single member disk.
type memberPrepPlan struct {
	Unmounts     []string // mount points to unmount (only this disk's own partitions)
	WipeTargets  []string // devices to `wipefs -a`, sub-partitions first, whole disk last
	HolderArrays []string // md arrays currently claiming this disk (e.g. "md1"), deduped
}

// systemMountPrefixes guards against ever unmounting a location that hosts
// live system state. isValidDevicePath's nvme pattern means a system disk
// could theoretically be selected as a "member disk" for a new array; this
// is the last line of defense against that.
var systemMountPrefixes = []string{
	"/boot",
	"/usr",
	"/etc",
	"/var",
	"/DATA",
	"/media/root-ro",
	"/media/root-rw",
	"/mnt/overlay",
	"/mnt/metadata",
}

// planMemberDiskPrep computes the sweep plan for a single member disk: which
// of its mount points must be unmounted, and which of its devices (its own
// partitions, then itself) must have their partition table / filesystem
// signatures wiped.
//
// It refuses (with an error) to plan a disk that is still part of an active
// RAID array, or whose plan would require unmounting a system mount point.
func planMemberDiskPrep(disk memberBlockDevice) (memberPrepPlan, error) {
	var plan memberPrepPlan
	collectHolderArrays(disk, &plan)

	if err := collectMemberPartitions(disk, disk.Path, &plan); err != nil {
		return memberPrepPlan{}, err
	}

	if disk.MountPoint != "" {
		if err := checkSystemMount(disk.Path, disk.MountPoint); err != nil {
			return memberPrepPlan{}, err
		}
		plan.Unmounts = append(plan.Unmounts, disk.MountPoint)
	}

	plan.WipeTargets = append(plan.WipeTargets, disk.Path)

	return plan, nil
}

// collectHolderArrays recursively walks dev's children collecting every md
// device (type "raid*" or name prefix "md") that currently claims this disk.
// Whether a holder blocks the sweep is decided later against /proc/mdstat:
// an active array is refused, an inactive leftover is stopped to release the
// disk (a dead array's members must stay reusable — the leftover may not
// even be registered in the service DB, so "delete the array first" can be
// a dead end for the user).
func collectHolderArrays(dev memberBlockDevice, plan *memberPrepPlan) {
	for _, child := range dev.Children {
		if strings.HasPrefix(child.Type, "raid") || strings.HasPrefix(child.Name, "md") {
			seen := false
			for _, h := range plan.HolderArrays {
				if h == child.Name {
					seen = true
					break
				}
			}
			if !seen {
				plan.HolderArrays = append(plan.HolderArrays, child.Name)
			}
			continue // never descend into (or wipe) the array device itself
		}
		collectHolderArrays(child, plan)
	}
}

// collectMemberPartitions recursively walks dev's children, collecting every
// "part"-typed device into plan.WipeTargets (sub-partitions before parents),
// and every mounted partition's mount point into plan.Unmounts.
func collectMemberPartitions(dev memberBlockDevice, diskPath string, plan *memberPrepPlan) error {
	for _, child := range dev.Children {
		// Never descend into an md array device: its partitions and mounts
		// belong to that array (handled via HolderArrays), not to this disk.
		if strings.HasPrefix(child.Type, "raid") || strings.HasPrefix(child.Name, "md") {
			continue
		}
		if child.Type == "part" {
			if child.MountPoint != "" {
				if err := checkSystemMount(diskPath, child.MountPoint); err != nil {
					return err
				}
				plan.Unmounts = append(plan.Unmounts, child.MountPoint)
			}
			plan.WipeTargets = append(plan.WipeTargets, child.Path)
		}
		if err := collectMemberPartitions(child, diskPath, plan); err != nil {
			return err
		}
	}
	return nil
}

// checkSystemMount refuses to plan an unmount of a system-critical mount
// point, no matter which device path is trying to claim it.
func checkSystemMount(diskPath, mountPoint string) error {
	if mountPoint == "/" {
		return fmt.Errorf("disk %s hosts system mount %s, refusing", diskPath, mountPoint)
	}
	for _, prefix := range systemMountPrefixes {
		if strings.HasPrefix(mountPoint, prefix) {
			return fmt.Errorf("disk %s hosts system mount %s, refusing", diskPath, mountPoint)
		}
	}
	return nil
}

// lsblkQueryMemberDisk runs lsblk against a single member disk and parses
// the output. It only reports exec/parse failures — those are the only
// errors the caller may degrade on; plan-level refusals come from
// planMemberDiskPrep and must always propagate.
func lsblkQueryMemberDisk(diskPath string) (memberBlockDevice, error) {
	out, err := exec.Command("lsblk", diskPath, "-O", "-J", "-b").Output()
	if err != nil {
		return memberBlockDevice{}, fmt.Errorf("lsblk %s: %w", diskPath, err)
	}

	var result struct {
		BlockDevices []memberBlockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return memberBlockDevice{}, fmt.Errorf("unmarshal lsblk output for %s: %w", diskPath, err)
	}
	if len(result.BlockDevices) == 0 {
		return memberBlockDevice{}, fmt.Errorf("lsblk returned no block devices for %s", diskPath)
	}
	return result.BlockDevices[0], nil
}

// classifyHolder decides, against /proc/mdstat, what to do about an md array
// that claims a member disk: an active array is refused (it holds live
// data), an inactive leftover must be stopped to release the disk, and a
// holder absent from mdstat has already been released.
func classifyHolder(entries []mdadm.MDStatEntry, holder string) (needStop bool, err error) {
	for _, e := range entries {
		if e.Device == holder {
			if e.State == "active" {
				return false, fmt.Errorf("part of active RAID array %s (%s); delete that array first before reusing its disks", holder, e.Level)
			}
			return true, nil
		}
	}
	return false, nil
}

// releaseHolderArrays stops every inactive md array still claiming the disk
// so wipefs/mdadm can take ownership; an active holder aborts the sweep.
func (s *raidService) releaseHolderArrays(diskPath string, holders []string) error {
	if len(holders) == 0 {
		return nil
	}
	entries, err := mdadm.ReadMDStat()
	if err != nil {
		return fmt.Errorf("read /proc/mdstat while releasing holders of %s: %w", diskPath, err)
	}
	for _, holder := range holders {
		needStop, err := classifyHolder(entries, holder)
		if err != nil {
			return fmt.Errorf("disk %s is %w", diskPath, err)
		}
		if !needStop {
			continue
		}
		logger.Info("stopping inactive RAID array to release member disk",
			zap.String("array", holder), zap.String("disk", diskPath))
		if err := mdadm.Stop("/dev/" + holder); err != nil {
			return fmt.Errorf("release member disk %s: %w", diskPath, err)
		}
	}
	return nil
}

// prepareMemberDisks sweeps each member disk clean before creating an array:
// unmount its partitions, drop the stale mount registration, and wipe old
// partition table / filesystem signatures, so that mdadm can cleanly take
// ownership of the whole disk.
func (s *raidService) prepareMemberDisks(diskPaths []string) error {
	for _, dp := range diskPaths {
		if err := s.prepareMemberDisk(dp); err != nil {
			return err
		}
	}

	// Nudge the kernel to re-read partition tables now that they've been
	// wiped; udev will converge on its own even if this fails.
	for _, dp := range diskPaths {
		if out, err := exec.Command("partprobe", dp).CombinedOutput(); err != nil {
			logger.Info("partprobe failed after member disk prep (udev should still converge)",
				zap.String("disk", dp), zap.String("output", strings.TrimSpace(string(out))), zap.Error(err))
		}
	}

	return nil
}

// prepareMemberDisk sweeps a single member disk: plan via lsblk (falling
// back to a whole-disk-only wipe if lsblk fails), unmount+deregister any of
// its mounted partitions, then wipefs every planned target.
func (s *raidService) prepareMemberDisk(diskPath string) error {
	var plan memberPrepPlan
	dev, err := lsblkQueryMemberDisk(diskPath)
	if err != nil {
		// Only exec/parse failures may degrade to a blind whole-disk wipe.
		logger.Info("lsblk query failed for member disk, falling back to whole-disk wipe only",
			zap.String("disk", diskPath), zap.Error(err))
		plan = memberPrepPlan{WipeTargets: []string{diskPath}}
	} else {
		plan, err = planMemberDiskPrep(dev)
		if err != nil {
			// Plan-level refusals (system mounts, ...) must propagate — a
			// degraded wipe here is exactly the damage the plan refused.
			return err
		}
	}

	if err := s.releaseHolderArrays(diskPath, plan.HolderArrays); err != nil {
		return err
	}

	for _, mp := range plan.Unmounts {
		out, err := exec.Command("umount", mp).CombinedOutput()
		if err != nil {
			return fmt.Errorf("unmount %s: %w: %s", mp, err, strings.TrimSpace(string(out)))
		}

		if delErr := s.db.Where("mount_point = ?", mp).Delete(&model.Volume{}).Error; delErr != nil {
			logger.Info("failed to delete stale mount registration after unmount",
				zap.String("mount_point", mp), zap.Error(delErr))
		}
	}

	for _, target := range plan.WipeTargets {
		out, err := exec.Command("wipefs", "-a", target).CombinedOutput()
		if err != nil {
			return fmt.Errorf("wipefs -a %s: %w: %s", target, err, strings.TrimSpace(string(out)))
		}
	}

	return nil
}
