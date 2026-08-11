package diskid

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindByIDIn_PreferredPrefix tests that ata-* is preferred over usb-* entries.
func TestFindByIDIn_PreferredPrefix(t *testing.T) {
	dir := t.TempDir()
	// Create a fake target device file
	target := filepath.Join(dir, "sda")
	if err := os.WriteFile(target, nil, 0644); err != nil {
		t.Fatal(err)
	}
	// Create by-id directory with two entries pointing to same target
	byIDDir := filepath.Join(dir, "by-id")
	if err := os.MkdirAll(byIDDir, 0755); err != nil {
		t.Fatal(err)
	}
	// usb entry (should be lower priority)
	if err := os.Symlink(target, filepath.Join(byIDDir, "usb-Generic_Flash_Disk-0:0")); err != nil {
		t.Fatal(err)
	}
	// ata entry (should be preferred)
	if err := os.Symlink(target, filepath.Join(byIDDir, "ata-Samsung_SSD_870_EVO_S1234")); err != nil {
		t.Fatal(err)
	}

	result := findByIDIn(byIDDir, target)
	if result != "ata-Samsung_SSD_870_EVO_S1234" {
		t.Errorf("expected ata- prefix entry, got %q", result)
	}
}

// TestFindByIDIn_SkipsPartitions tests that entries containing "-part" are skipped.
func TestFindByIDIn_SkipsPartitions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sda")
	if err := os.WriteFile(target, nil, 0644); err != nil {
		t.Fatal(err)
	}
	byIDDir := filepath.Join(dir, "by-id")
	if err := os.MkdirAll(byIDDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Only a partition entry — should be skipped
	if err := os.Symlink(target, filepath.Join(byIDDir, "ata-Samsung-part1")); err != nil {
		t.Fatal(err)
	}

	result := findByIDIn(byIDDir, target)
	if result != "" {
		t.Errorf("expected empty result (partition entry skipped), got %q", result)
	}
}

// TestFindByIDIn_NoMatch tests that empty string is returned when no match.
func TestFindByIDIn_NoMatch(t *testing.T) {
	dir := t.TempDir()
	byIDDir := filepath.Join(dir, "by-id")
	if err := os.MkdirAll(byIDDir, 0755); err != nil {
		t.Fatal(err)
	}
	result := findByIDIn(byIDDir, "/dev/nonexistent")
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

// TestFindByIDIn_MissingDir tests graceful handling of missing by-id directory.
func TestFindByIDIn_MissingDir(t *testing.T) {
	result := findByIDIn("/nonexistent/path", "/dev/sda")
	if result != "" {
		t.Errorf("expected empty string for missing dir, got %q", result)
	}
}

// TestParseSerialFromLsblk tests serial extraction from lsblk JSON output.
func TestParseSerialFromLsblk(t *testing.T) {
	jsonOutput := `{
   "blockdevices": [
      {"name": "sda", "serial": "S1234567890"},
      {"name": "sdb", "serial": "ABCDE12345"},
      {"name": "sdc", "serial": null}
   ]
}`
	tests := []struct {
		serial   string
		expected string
	}{
		{"S1234567890", "/dev/sda"},
		{"ABCDE12345", "/dev/sdb"},
		{"NOTFOUND", ""},
		{"", ""},
	}
	for _, tt := range tests {
		result := parseSerialFromJSON([]byte(jsonOutput), tt.serial)
		if result != tt.expected {
			t.Errorf("serial=%q: expected %q, got %q", tt.serial, tt.expected, result)
		}
	}
}

func TestMatchesStoredIdentity(t *testing.T) {
	cases := []struct {
		name            string
		stored, current DiskIdentifiers
		want            bool
	}{
		// The hot-swap collision: cached path now hosts a different disk.
		{"different disk at cached path",
			DiskIdentifiers{ByID: "ata-A_S1", Serial: "S1", DevicePath: "/dev/sdb"},
			DiskIdentifiers{ByID: "ata-B_S9", Serial: "S9", DevicePath: "/dev/sdb"}, false},
		{"same disk by serial",
			DiskIdentifiers{Serial: "S1", DevicePath: "/dev/sdb"},
			DiskIdentifiers{Serial: "S1", DevicePath: "/dev/sdb"}, true},
		{"same disk by by-id when serial unreadable",
			DiskIdentifiers{ByID: "ata-A_S1", Serial: "S1"},
			DiskIdentifiers{ByID: "ata-A_S1", Serial: ""}, true},
		// No stored identity at all: the cached path is all we ever had.
		{"nothing stored to verify against",
			DiskIdentifiers{DevicePath: "/dev/sdb"},
			DiskIdentifiers{ByID: "ata-B_S9", Serial: "S9"}, true},
		{"stored serial, current disk reports none",
			DiskIdentifiers{Serial: "S1"},
			DiskIdentifiers{}, false},
	}
	for _, c := range cases {
		if got := matchesStoredIdentity(c.stored, c.current); got != c.want {
			t.Errorf("%s: matchesStoredIdentity = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseSerialMapJSON(t *testing.T) {
	data := []byte(`{"blockdevices": [
		{"name": "sda", "serial": "S1"},
		{"name": "sdb", "serial": null},
		{"name": "nvme0n1", "serial": "N1"}
	]}`)
	m := parseSerialMapJSON(data)
	if m["/dev/sda"] != "S1" || m["/dev/nvme0n1"] != "N1" {
		t.Errorf("unexpected map: %v", m)
	}
	if v, ok := m["/dev/sdb"]; !ok || v != "" {
		t.Errorf("null serial should map to empty string, got %q ok=%v", v, ok)
	}
	if parseSerialMapJSON([]byte("not json")) != nil {
		t.Error("invalid JSON should return nil")
	}
}
