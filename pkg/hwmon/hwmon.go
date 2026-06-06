// Package hwmon reads drive temperatures from the kernel hwmon sysfs
// interface (drivetemp for SATA/SAS, nvme for NVMe). Unlike smartctl this
// is a plain file read: it is instant, needs no caching and does not wake
// a disk from standby.
package hwmon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const sysRoot = "/sys"

// drive-capable hwmon driver names
var driveHwmonNames = map[string]bool{
	"drivetemp": true,
	"nvme":      true,
}

// DriveTemperature returns the current temperature in °C of a block device
// (e.g. "sda", "nvme0n1") from the real sysfs.
func DriveTemperature(devName string) (int, bool) {
	return DriveTemperatureFrom(sysRoot, devName)
}

// DriveTemperatureFrom is DriveTemperature with a configurable sysfs root,
// used by tests.
func DriveTemperatureFrom(root, devName string) (int, bool) {
	entries, err := os.ReadDir(filepath.Join(root, "class", "hwmon"))
	if err != nil {
		return 0, false
	}

	for _, e := range entries {
		hwmonDir := filepath.Join(root, "class", "hwmon", e.Name())

		name, err := os.ReadFile(filepath.Join(hwmonDir, "name"))
		if err != nil || !driveHwmonNames[strings.TrimSpace(string(name))] {
			continue
		}

		if !hwmonMatchesDevice(hwmonDir, devName) {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(hwmonDir, "temp1_input"))
		if err != nil {
			return 0, false
		}
		milli, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return 0, false
		}
		return milli / 1000, true
	}
	return 0, false
}

// hwmonMatchesDevice reports whether the hwmon entry belongs to devName.
// drivetemp exposes the disk under device/block/<dev>; the nvme controller
// exposes its namespaces directly under device/<dev>.
func hwmonMatchesDevice(hwmonDir, devName string) bool {
	for _, p := range []string{
		filepath.Join(hwmonDir, "device", "block", devName),
		filepath.Join(hwmonDir, "device", devName),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
