package diskid

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DiskIdentifiers holds persistent identifiers for a single disk.
// Fields are tried in order: ByID → Serial → DevicePath (cache only).
type DiskIdentifiers struct {
	ByID       string // entry name under /dev/disk/by-id/ (not the full path)
	Serial     string // disk serial number
	DevicePath string // last known /dev/sdX — cache, not authoritative
}

// Identify collects persistent identifiers for the disk at devicePath.
// Call this when creating or registering a RAID array.
func Identify(devicePath string) DiskIdentifiers {
	ids := DiskIdentifiers{DevicePath: devicePath}
	ids.ByID = findByIDIn("/dev/disk/by-id", devicePath)
	ids.Serial = getSerial(devicePath)
	return ids
}

// Resolve finds the current device path for a disk using its stored identifiers.
// Returns ("", false) if the disk is not currently available.
// IMPORTANT: Resolve is used only to update DevicePathCache.
// A failed Resolve must NOT prevent a RAID array from being mounted —
// degraded arrays are already handled by mdadm internally.
func Resolve(ids DiskIdentifiers) (string, bool) {
	// 1. Try /dev/disk/by-id/
	if ids.ByID != "" {
		linkPath := filepath.Join("/dev/disk/by-id", ids.ByID)
		if resolved, err := filepath.EvalSymlinks(linkPath); err == nil {
			if _, err := os.Stat(resolved); err == nil {
				return resolved, true
			}
		}
	}

	// 2. Try serial number scan
	if ids.Serial != "" {
		out, err := exec.Command("lsblk", "-o", "NAME,SERIAL", "-n", "-J").Output()
		if err == nil {
			if path := parseSerialFromJSON(out, ids.Serial); path != "" {
				return path, true
			}
		}
	}

	// 3. Try cached device path
	if ids.DevicePath != "" {
		if _, err := os.Stat(ids.DevicePath); err == nil {
			return ids.DevicePath, true
		}
	}

	return "", false
}

// findByIDIn scans dir for a symlink pointing to devicePath.
// Preferred prefixes: ata-, nvme-, wwn- (skips partition entries and usb- as fallback).
// Exported as internal helper; tests pass a temp dir.
func findByIDIn(dir, devicePath string) string {
	// Resolve devicePath to its canonical form for comparison.
	realTarget, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		realTarget = devicePath
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	preferred := []string{"ata-", "nvme-", "wwn-"}
	var fallback string

	for _, entry := range entries {
		name := entry.Name()
		// Skip partition entries (e.g. ata-Samsung-part1)
		if strings.Contains(name, "-part") {
			continue
		}

		linkPath := filepath.Join(dir, name)
		resolved, err := filepath.EvalSymlinks(linkPath)
		if err != nil || resolved != realTarget {
			continue
		}

		for _, prefix := range preferred {
			if strings.HasPrefix(name, prefix) {
				return name // Best match — return immediately.
			}
		}
		if fallback == "" {
			fallback = name
		}
	}

	return fallback
}

// getSerial reads the serial number of devicePath via lsblk.
func getSerial(devicePath string) string {
	out, err := exec.Command("lsblk", "-o", "SERIAL", "-n", devicePath).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseSerialFromJSON finds the device path for a serial number in lsblk -J output.
// Extracted as a pure function so it can be unit-tested without real hardware.
func parseSerialFromJSON(data []byte, serial string) string {
	if serial == "" {
		return ""
	}
	var output struct {
		Blockdevices []struct {
			Name   string  `json:"name"`
			Serial *string `json:"serial"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return ""
	}
	for _, dev := range output.Blockdevices {
		if dev.Serial != nil && *dev.Serial == serial {
			return "/dev/" + dev.Name
		}
	}
	return ""
}
