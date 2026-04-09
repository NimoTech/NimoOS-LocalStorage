package mdadm

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Create creates a new RAID array.
// mdadm --create <device> --level=<level> --raid-devices=<n> [--chunk=<kb>] --run <members...>
func Create(device string, level int, members []string, chunkKB int) error {
	args := []string{
		"--create", device,
		fmt.Sprintf("--level=%d", level),
		fmt.Sprintf("--raid-devices=%d", len(members)),
	}
	if chunkKB > 0 {
		args = append(args, fmt.Sprintf("--chunk=%d", chunkKB))
	}
	args = append(args, "--run")
	args = append(args, members...)

	logger.Info("mdadm create", zap.String("device", device), zap.Int("level", level), zap.Strings("members", members))
	out, err := exec.Command("mdadm", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mdadm create %s: %w: %s", device, err, string(out))
	}
	return nil
}

// Stop stops a RAID array: mdadm --stop <device>
func Stop(device string) error {
	logger.Info("mdadm stop", zap.String("device", device))
	out, err := exec.Command("mdadm", "--stop", device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mdadm stop %s: %w: %s", device, err, string(out))
	}
	return nil
}

// Detail runs `mdadm --detail <device>` and parses output using ParseDetail from parse.go.
func Detail(device string) (*ArrayDetail, error) {
	out, err := exec.Command("mdadm", "--detail", device).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("mdadm detail %s: %w: %s", device, err, string(out))
	}
	return ParseDetail(string(out))
}

// ReadMDStat reads /proc/mdstat and parses using ParseMDStat from parse.go.
func ReadMDStat() ([]MDStatEntry, error) {
	data, err := os.ReadFile("/proc/mdstat")
	if err != nil {
		return nil, fmt.Errorf("read /proc/mdstat: %w", err)
	}
	return ParseMDStat(string(data))
}

// AddDisk adds a disk to array: mdadm <arrayDevice> --add <diskDevice>
func AddDisk(arrayDevice, diskDevice string) error {
	logger.Info("mdadm add disk", zap.String("array", arrayDevice), zap.String("disk", diskDevice))
	out, err := exec.Command("mdadm", arrayDevice, "--add", diskDevice).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mdadm add disk %s to %s: %w: %s", diskDevice, arrayDevice, err, string(out))
	}
	return nil
}

// RemoveDisk marks faulty + removes: mdadm <arrayDevice> --fail <diskDevice> --remove <diskDevice>
func RemoveDisk(arrayDevice, diskDevice string) error {
	logger.Info("mdadm remove disk", zap.String("array", arrayDevice), zap.String("disk", diskDevice))
	out, err := exec.Command("mdadm", arrayDevice, "--fail", diskDevice, "--remove", diskDevice).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mdadm remove disk %s from %s: %w: %s", diskDevice, arrayDevice, err, string(out))
	}
	return nil
}

// AssembleScan reassembles all known arrays: mdadm --assemble --scan
// NOTE: may return non-zero if arrays already active — log but don't fail
func AssembleScan() error {
	logger.Info("mdadm assemble scan")
	out, err := exec.Command("mdadm", "--assemble", "--scan").CombinedOutput()
	if err != nil {
		logger.Info("mdadm assemble scan returned non-zero (arrays may already be active)", zap.String("output", string(out)))
	}
	return nil
}

// SaveConfig writes config: bash -c "mdadm --detail --scan >> /etc/mdadm/mdadm.conf"
func SaveConfig() error {
	logger.Info("mdadm save config")
	out, err := exec.Command("bash", "-c", "mdadm --detail --scan >> /etc/mdadm/mdadm.conf").CombinedOutput()
	if err != nil {
		return fmt.Errorf("mdadm save config: %w: %s", err, string(out))
	}
	return nil
}

// ZeroSuperblock clears superblock: mdadm --zero-superblock <device>
func ZeroSuperblock(device string) error {
	logger.Info("mdadm zero superblock", zap.String("device", device))
	out, err := exec.Command("mdadm", "--zero-superblock", device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mdadm zero-superblock %s: %w: %s", device, err, string(out))
	}
	return nil
}

// NextAvailableDevice returns next free /dev/mdN (checks /proc/mdstat, tries 0-127).
func NextAvailableDevice() (string, error) {
	entries, err := ReadMDStat()
	if err != nil {
		return "", fmt.Errorf("next available device: %w", err)
	}

	used := make(map[string]bool)
	for _, e := range entries {
		used[e.Device] = true
	}

	for i := 0; i < 128; i++ {
		name := fmt.Sprintf("md%d", i)
		if !used[name] {
			return "/dev/" + name, nil
		}
	}
	return "", fmt.Errorf("no available /dev/mdN device in range 0-127")
}

// LevelName converts 5 → "raid5".
func LevelName(level int) string {
	return fmt.Sprintf("raid%d", level)
}

// ParseLevelNumber converts "raid5" → 5.
func ParseLevelNumber(level string) (int, error) {
	lower := strings.ToLower(strings.TrimSpace(level))
	if !strings.HasPrefix(lower, "raid") {
		return 0, fmt.Errorf("invalid raid level name %q: must start with \"raid\"", level)
	}
	n, err := strconv.Atoi(lower[4:])
	if err != nil {
		return 0, fmt.Errorf("invalid raid level name %q: %w", level, err)
	}
	return n, nil
}
