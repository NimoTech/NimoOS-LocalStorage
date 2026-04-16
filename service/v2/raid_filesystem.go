package v2

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"go.uber.org/zap"
)

const defaultRAIDFilesystem = "btrfs"

var (
	supportedRAIDFilesystems = map[string]struct{}{
		"ext4":  {},
		"btrfs": {},
	}
	mdDeviceRegex = regexp.MustCompile(`^/dev/md[0-9]+$`)
)

func configuredDefaultRAIDFilesystem() string {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("RAID_DEFAULT_FILESYSTEM")))
	if raw == "" {
		return defaultRAIDFilesystem
	}
	if _, ok := supportedRAIDFilesystems[raw]; ok {
		return raw
	}
	return defaultRAIDFilesystem
}

func normalizeCreateFilesystem(filesystem string) (string, error) {
	fs := strings.TrimSpace(strings.ToLower(filesystem))
	if fs == "" {
		fs = configuredDefaultRAIDFilesystem()
	}
	if _, ok := supportedRAIDFilesystems[fs]; !ok {
		return "", fmt.Errorf("unsupported filesystem: %s", filesystem)
	}
	return fs, nil
}

func normalizeStoredFilesystem(filesystem string) (string, error) {
	fs := strings.TrimSpace(strings.ToLower(filesystem))
	if fs == "" {
		return "", fmt.Errorf("filesystem metadata is empty")
	}
	if _, ok := supportedRAIDFilesystems[fs]; !ok {
		return "", fmt.Errorf("unsupported filesystem metadata: %s", filesystem)
	}
	return fs, nil
}

func checkBtrfsSupport() error {
	if _, err := exec.LookPath("mkfs.btrfs"); err != nil {
		return fmt.Errorf("mkfs.btrfs not found: please install btrfs-progs")
	}
	if _, err := exec.LookPath("btrfs"); err != nil {
		return fmt.Errorf("btrfs command not found: please install btrfs-progs")
	}
	return nil
}

func wipeDeviceSignatures(device string) error {
	if !mdDeviceRegex.MatchString(device) {
		return fmt.Errorf("wipefs is only allowed on md devices, got: %s", device)
	}
	out, err := exec.Command("wipefs", "-a", device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("wipefs -a %s: %w: %s", device, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func mountRAIDDevice(device, mountPoint, filesystem string) error {
	fs, err := normalizeStoredFilesystem(filesystem)
	if err != nil {
		return err
	}
	switch fs {
	case "btrfs":
		return mountBtrfsDevice(device, mountPoint)
	default:
		out, err := exec.Command("mount", device, mountPoint).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mount %s on %s: %w: %s", device, mountPoint, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

func mountBtrfsDevice(device, mountPoint string) error {
	baseOpts := "space_cache=v2,noatime,compress=zstd:1"
	primaryOpts := baseOpts + ",subvol=@"

	out, err := exec.Command("mount", "-t", "btrfs", "-o", primaryOpts, device, mountPoint).CombinedOutput()
	if err == nil {
		return nil
	}

	lowerOut := strings.ToLower(string(out))
	canFallback := strings.Contains(lowerOut, "no such file") || strings.Contains(lowerOut, "can't find")
	if !canFallback {
		return fmt.Errorf("mount btrfs with subvol=@ failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	fallbackOpts := baseOpts + ",subvolid=5"
	out2, err2 := exec.Command("mount", "-t", "btrfs", "-o", fallbackOpts, device, mountPoint).CombinedOutput()
	if err2 != nil {
		return fmt.Errorf("mount btrfs fallback subvolid=5 failed: %w: %s (primary err: %v, primary out: %s)", err2, strings.TrimSpace(string(out2)), err, strings.TrimSpace(string(out)))
	}

	logger.Info("mounted btrfs with fallback subvolid=5", zap.String("device", device), zap.String("mount_point", mountPoint))
	return nil
}

func btrfsSetupSubvolumes(device string) error {
	tmpMount, err := os.MkdirTemp("", "nimoos-btrfs-init-")
	if err != nil {
		return fmt.Errorf("create temporary mount dir: %w", err)
	}
	defer os.Remove(tmpMount)

	mounted := false
	defer func() {
		if mounted {
			_, _ = exec.Command("umount", tmpMount).CombinedOutput()
		}
	}()

	out, err := exec.Command("mount", "-t", "btrfs", "-o", "subvolid=5", device, tmpMount).CombinedOutput()
	if err != nil {
		return fmt.Errorf("temporary mount btrfs subvolid=5: %w: %s", err, strings.TrimSpace(string(out)))
	}
	mounted = true

	if err := createBtrfsSubvolume(tmpMount + "/@"); err != nil {
		return err
	}
	if err := createBtrfsSubvolume(tmpMount + "/@snapshots"); err != nil {
		return err
	}

	return nil
}

func createBtrfsSubvolume(path string) error {
	out, err := exec.Command("btrfs", "subvolume", "create", path).CombinedOutput()
	if err != nil {
		lowerOut := strings.ToLower(string(out))
		if strings.Contains(lowerOut, "file exists") {
			return nil
		}
		return fmt.Errorf("create btrfs subvolume %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func detectFilesystemByDevice(device string) (string, error) {
	if fs, err := detectFilesystemByLSBLK(device); err == nil && fs != "" {
		return fs, nil
	}
	return detectFilesystemByBLKIDProbe(device)
}

func detectFilesystemByLSBLK(device string) (string, error) {
	type blockDevice struct {
		FSType string `json:"fstype"`
	}
	type lsblkResult struct {
		Blockdevices []blockDevice `json:"blockdevices"`
	}

	out, err := exec.Command("lsblk", "-f", "-J", "-o", "FSTYPE", device).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("lsblk detect filesystem: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var result lsblkResult
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("unmarshal lsblk output: %w", err)
	}
	if len(result.Blockdevices) == 0 {
		return "", nil
	}

	fs := strings.TrimSpace(strings.ToLower(result.Blockdevices[0].FSType))
	if fs == "" {
		return "", nil
	}
	if _, ok := supportedRAIDFilesystems[fs]; ok {
		return fs, nil
	}
	return "", nil
}

func detectFilesystemByBLKIDProbe(device string) (string, error) {
	out, err := exec.Command("blkid", "-p", "-o", "export", device).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("blkid probe detect filesystem: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TYPE=") {
			continue
		}
		fs := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(line, "TYPE=")))
		if _, ok := supportedRAIDFilesystems[fs]; ok {
			return fs, nil
		}
	}
	return "", nil
}

func (s *raidService) normalizeUnknownFilesystems() {
	var raids []model.RAIDArray
	if err := s.db.Where("filesystem = '' OR filesystem IS NULL").Find(&raids).Error; err != nil {
		logger.Error("failed to list RAID rows with empty filesystem", zap.Error(err))
		return
	}

	for _, raid := range raids {
		fs, err := detectFilesystemByDevice(raid.DevicePath)
		if err != nil {
			logger.Info("filesystem detection failed for RAID", zap.Uint("raid_id", raid.ID), zap.String("device", raid.DevicePath), zap.Error(err))
			continue
		}
		if fs == "" {
			continue
		}
		if err := s.db.Model(&model.RAIDArray{}).Where("id = ?", raid.ID).Update("filesystem", fs).Error; err != nil {
			logger.Error("failed to update RAID filesystem metadata", zap.Uint("raid_id", raid.ID), zap.Error(err))
			continue
		}
		logger.Info("updated RAID filesystem metadata", zap.Uint("raid_id", raid.ID), zap.String("filesystem", fs))
	}
}

func (s *raidService) resolveFilesystemForDevice(raid *model.RAIDArray, device string) (string, error) {
	if fs, err := normalizeStoredFilesystem(raid.Filesystem); err == nil {
		return fs, nil
	}

	fs, err := detectFilesystemByDevice(device)
	if err != nil {
		return "", err
	}
	if fs == "" {
		return "", fmt.Errorf("filesystem metadata is empty and probe result is empty for %s", device)
	}

	if err := s.db.Model(&model.RAIDArray{}).Where("id = ?", raid.ID).Update("filesystem", fs).Error; err != nil {
		logger.Error("failed to persist detected filesystem", zap.Uint("raid_id", raid.ID), zap.String("filesystem", fs), zap.Error(err))
	} else {
		raid.Filesystem = fs
	}

	return fs, nil
}

func configuredBtrfsNoCoWDirs() []string {
	raw := strings.TrimSpace(os.Getenv("RAID_BTRFS_NOCOW_DIRS"))
	if raw == "" {
		raw = "docker,appdata"
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		p = strings.TrimPrefix(filepath.Clean(p), "/")
		if p == "." || p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

func applyNoCoWPolicy(mountPoint string) {
	for _, relPath := range configuredBtrfsNoCoWDirs() {
		target := filepath.Join(mountPoint, relPath)
		if err := os.MkdirAll(target, 0o755); err != nil {
			logger.Info("nocow policy: create dir failed", zap.String("path", target), zap.Error(err))
			continue
		}
		empty, err := isDirectoryEmpty(target)
		if err != nil {
			logger.Info("nocow policy: inspect dir failed", zap.String("path", target), zap.Error(err))
			continue
		}
		if !empty {
			logger.Info("nocow policy skipped: directory already has data", zap.String("path", target))
			continue
		}
		out, err := exec.Command("chattr", "+C", target).CombinedOutput()
		if err != nil {
			logger.Info("nocow policy: chattr +C failed", zap.String("path", target), zap.String("output", strings.TrimSpace(string(out))), zap.Error(err))
			continue
		}
		logger.Info("nocow policy applied", zap.String("path", target))
	}
}

func isDirectoryEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
