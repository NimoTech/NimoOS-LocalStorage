package v2

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
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
	Unmounts    []string // mount points to unmount (only this disk's own partitions)
	WipeTargets []string // devices to `wipefs -a`, sub-partitions first, whole disk last
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
	if err := checkNoActiveRAIDMembership(disk, disk.Path); err != nil {
		return memberPrepPlan{}, err
	}

	var plan memberPrepPlan
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

// checkNoActiveRAIDMembership recursively walks dev's children looking for
// any device that is itself an active RAID array member (type "raid*") or
// an md device (name prefix "md"). Finding one means diskPath is still a
// live member of some other array, and we must never touch its mounts or
// signatures.
func checkNoActiveRAIDMembership(dev memberBlockDevice, diskPath string) error {
	for _, child := range dev.Children {
		if strings.HasPrefix(child.Type, "raid") || strings.HasPrefix(child.Name, "md") {
			return fmt.Errorf("disk %s is part of an active RAID array (%s), refusing to wipe", diskPath, child.Name)
		}
		if err := checkNoActiveRAIDMembership(child, diskPath); err != nil {
			return err
		}
	}
	return nil
}

// collectMemberPartitions recursively walks dev's children, collecting every
// "part"-typed device into plan.WipeTargets (sub-partitions before parents),
// and every mounted partition's mount point into plan.Unmounts.
func collectMemberPartitions(dev memberBlockDevice, diskPath string, plan *memberPrepPlan) error {
	for _, child := range dev.Children {
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

// lsblkMemberPrepPlan runs lsblk against a single member disk and turns its
// output into a memberPrepPlan.
func lsblkMemberPrepPlan(diskPath string) (memberPrepPlan, error) {
	out, err := exec.Command("lsblk", diskPath, "-O", "-J", "-b").Output()
	if err != nil {
		return memberPrepPlan{}, fmt.Errorf("lsblk %s: %w", diskPath, err)
	}

	var result struct {
		BlockDevices []memberBlockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return memberPrepPlan{}, fmt.Errorf("unmarshal lsblk output for %s: %w", diskPath, err)
	}
	if len(result.BlockDevices) == 0 {
		return memberPrepPlan{}, fmt.Errorf("lsblk returned no block devices for %s", diskPath)
	}

	return planMemberDiskPrep(result.BlockDevices[0])
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
	plan, err := lsblkMemberPrepPlan(diskPath)
	if err != nil {
		logger.Info("lsblk plan failed for member disk, falling back to whole-disk wipe only",
			zap.String("disk", diskPath), zap.Error(err))
		plan = memberPrepPlan{WipeTargets: []string{diskPath}}
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
