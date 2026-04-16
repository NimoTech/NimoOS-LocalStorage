package v2

import (
	"fmt"
	"os/exec"
	"strings"
)

// EnsureFilesystemResized expands filesystem capacity to match the underlying md device.
// This hook is intended to be called after RAID grow/rebuild workflows.
func (s *raidService) EnsureFilesystemResized(id uint) error {
	raid, err := s.getRAIDByID(id)
	if err != nil {
		return fmt.Errorf("get RAID array: %w", err)
	}

	filesystem, err := s.resolveFilesystemForDevice(raid, raid.DevicePath)
	if err != nil {
		return fmt.Errorf("resolve filesystem: %w", err)
	}

	switch filesystem {
	case "ext4":
		return resizeExt4(raid.DevicePath)
	case "btrfs":
		return resizeBtrfs(raid.MountPoint)
	default:
		return fmt.Errorf("unsupported filesystem for resize: %s", filesystem)
	}
}

func resizeExt4(device string) error {
	out, err := exec.Command("resize2fs", device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize2fs %s: %w: %s", device, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func resizeBtrfs(mountPoint string) error {
	out, err := exec.Command("btrfs", "filesystem", "resize", "max", mountPoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("btrfs filesystem resize max %s: %w: %s", mountPoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

