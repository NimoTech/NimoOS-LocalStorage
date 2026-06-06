package hwmon

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

// buildFakeSysfs creates a minimal sysfs tree:
//
//	hwmon0: acpitz (no block device, must be ignored)
//	hwmon1: nvme, 38850 m°C, device/nvme0n1
//	hwmon2: drivetemp, 63000 m°C, device/block/sda
//	hwmon3: drivetemp, malformed temp1_input, device/block/sdx
func buildFakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	hwmon := filepath.Join(root, "class", "hwmon")

	write(filepath.Join(hwmon, "hwmon0", "name"), "acpitz\n")
	write(filepath.Join(hwmon, "hwmon0", "temp1_input"), "27800\n")

	write(filepath.Join(hwmon, "hwmon1", "name"), "nvme\n")
	write(filepath.Join(hwmon, "hwmon1", "temp1_input"), "38850\n")
	assert.NilError(t, os.MkdirAll(filepath.Join(hwmon, "hwmon1", "device", "nvme0n1"), 0o755))

	write(filepath.Join(hwmon, "hwmon2", "name"), "drivetemp\n")
	write(filepath.Join(hwmon, "hwmon2", "temp1_input"), "63000\n")
	assert.NilError(t, os.MkdirAll(filepath.Join(hwmon, "hwmon2", "device", "block", "sda"), 0o755))

	write(filepath.Join(hwmon, "hwmon3", "name"), "drivetemp\n")
	write(filepath.Join(hwmon, "hwmon3", "temp1_input"), "not-a-number\n")
	assert.NilError(t, os.MkdirAll(filepath.Join(hwmon, "hwmon3", "device", "block", "sdx"), 0o755))

	return root
}

func TestDriveTemperatureSATA(t *testing.T) {
	root := buildFakeSysfs(t)

	temp, ok := DriveTemperatureFrom(root, "sda")
	assert.Equal(t, ok, true)
	assert.Equal(t, temp, 63)
}

func TestDriveTemperatureNVMe(t *testing.T) {
	root := buildFakeSysfs(t)

	temp, ok := DriveTemperatureFrom(root, "nvme0n1")
	assert.Equal(t, ok, true)
	assert.Equal(t, temp, 38)
}

func TestDriveTemperatureMissingDevice(t *testing.T) {
	root := buildFakeSysfs(t)

	// sde has no drivetemp hwmon entry (seen in the field when the driver
	// fails to attach) — must report not-found instead of a bogus value
	_, ok := DriveTemperatureFrom(root, "sde")
	assert.Equal(t, ok, false)
}

func TestDriveTemperatureMalformedInput(t *testing.T) {
	root := buildFakeSysfs(t)

	_, ok := DriveTemperatureFrom(root, "sdx")
	assert.Equal(t, ok, false)
}

func TestDriveTemperatureEmptySysfs(t *testing.T) {
	_, ok := DriveTemperatureFrom(t.TempDir(), "sda")
	assert.Equal(t, ok, false)
}
