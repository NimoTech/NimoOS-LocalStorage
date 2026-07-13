package snapshot

import (
	"errors"
	"testing"
)

func TestFindVolumeForPathMatchesExactMountPoint(t *testing.T) {
	volumes := []VolumeInfo{{UUID: "a", MountPoint: "/media/RAID_Design"}}
	vol, rel, err := FindVolumeForPath(volumes, "/media/RAID_Design")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vol.UUID != "a" || rel != "." {
		t.Fatalf("got vol=%q rel=%q, want a/.", vol.UUID, rel)
	}
}

func TestFindVolumeForPathReturnsRelativePath(t *testing.T) {
	volumes := []VolumeInfo{{UUID: "a", MountPoint: "/media/RAID_Design"}}
	_, rel, err := FindVolumeForPath(volumes, "/media/RAID_Design/Photos/2026/img.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != "Photos/2026/img.jpg" {
		t.Errorf("got rel %q, want Photos/2026/img.jpg", rel)
	}
}

// TestFindVolumeForPathPicksLongestPrefix is the case the handoff calls out
// explicitly: a nested volume mounted inside another volume's tree must
// win, not the shorter (outer) mount point.
func TestFindVolumeForPathPicksLongestPrefix(t *testing.T) {
	volumes := []VolumeInfo{
		{UUID: "outer", MountPoint: "/media/RAID"},
		{UUID: "inner", MountPoint: "/media/RAID/nested"},
	}
	vol, rel, err := FindVolumeForPath(volumes, "/media/RAID/nested/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vol.UUID != "inner" {
		t.Fatalf("got volume %q, want inner (longest prefix match)", vol.UUID)
	}
	if rel != "file.txt" {
		t.Errorf("got rel %q, want file.txt", rel)
	}
}

func TestFindVolumeForPathDoesNotMatchSiblingWithSharedPrefixString(t *testing.T) {
	// "/media/RAID_Design2" must not match volume "/media/RAID_Design" just
	// because it shares a string prefix — only a real path-separator
	// boundary counts.
	volumes := []VolumeInfo{{UUID: "a", MountPoint: "/media/RAID_Design"}}
	_, _, err := FindVolumeForPath(volumes, "/media/RAID_Design2/file.txt")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestFindVolumeForPathReturnsNotFoundWhenNoVolumeMatches(t *testing.T) {
	volumes := []VolumeInfo{{UUID: "a", MountPoint: "/media/RAID_Design"}}
	_, _, err := FindVolumeForPath(volumes, "/some/other/path")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("expected ErrVolumeNotFound, got %v", err)
	}
}
