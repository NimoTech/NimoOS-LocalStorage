package v2

import (
	"strings"
	"testing"
)

func TestPathIsUnder(t *testing.T) {
	cases := []struct {
		p, mount string
		want     bool
	}{
		{"/media/RAID/.docker", "/media/RAID", true},
		{"/media/RAID", "/media/RAID", true},
		{"/media/RAID/", "/media/RAID", true},
		{"/media/RAID", "/media/RAID/", true},
		{"/media/RAID2/.docker", "/media/RAID", false}, // sibling, prefix-but-not-under
		{"/DATA/.docker", "/media/RAID", false},
		{"", "/media/RAID", false},
		{"/media/RAID/.docker", "", false},
	}
	for _, c := range cases {
		if got := pathIsUnder(c.p, c.mount); got != c.want {
			t.Errorf("pathIsUnder(%q, %q) = %v, want %v", c.p, c.mount, got, c.want)
		}
	}
}

func TestFormatSystemPathConflictMentionsAllItems(t *testing.T) {
	msg := formatSystemPathConflict("/media/RAID_HDD_md0", []systemPathOnRAID{
		{Kind: "Docker images & containers", Path: "/media/RAID_HDD_md0/.docker"},
		{Kind: "Application data", Path: "/media/RAID_HDD_md0/AppData"},
	})
	for _, want := range []string{
		"/media/RAID_HDD_md0",
		"Docker images & containers",
		"/media/RAID_HDD_md0/.docker",
		"Application data",
		"/media/RAID_HDD_md0/AppData",
		"Storage Paths",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict message missing %q\n--- message ---\n%s", want, msg)
		}
	}
}

func TestFormatBusyErrorWithoutEvidence(t *testing.T) {
	// Use a target that no process can possibly hold, so collectBusyEntries
	// and collectChildMounts return nothing. We just verify the fallback
	// branch produces a coherent message.
	msg := formatBusyError("/nonexistent-target-for-test", "umount: target is busy")
	if !strings.Contains(msg, "/nonexistent-target-for-test") {
		t.Errorf("expected target in message, got %q", msg)
	}
	if !strings.Contains(msg, "Unable to identify") {
		t.Errorf("expected fallback hint, got %q", msg)
	}
}
