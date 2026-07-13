package snapshot

import (
	"errors"
	"testing"
)

func pathTestVolume() VolumeInfo {
	return VolumeInfo{
		UUID:       "vol-1",
		DevicePath: "/dev/md0",
		MountPoint: "/media/RAID_Design",
		Filesystem: "btrfs",
	}
}

func TestResolveSnapshotPathValidName(t *testing.T) {
	name := "20260712T030000Z_manual_before-move"
	got, err := ResolveSnapshotPath(pathTestVolume(), name)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/media/RAID_Design/.snapshots/20260712T030000Z_manual_before-move"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveSnapshotPathRejectsUnrecognizedName(t *testing.T) {
	_, err := ResolveSnapshotPath(pathTestVolume(), "not-one-of-ours")
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName, got %v", err)
	}
}

func TestResolveSnapshotPathRejectsEmptyName(t *testing.T) {
	_, err := ResolveSnapshotPath(pathTestVolume(), "")
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName, got %v", err)
	}
}

func TestResolveSnapshotPathRejectsDotDotInLabel(t *testing.T) {
	// ParseName alone accepts this (label isn't sanitized on parse) — the
	// filepath.Clean + prefix check must be the thing that catches it.
	name := "20260712T030000Z_manual_../../../../etc/passwd"
	_, err := ResolveSnapshotPath(pathTestVolume(), name)
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName for path traversal via label, got %v", err)
	}
}

func TestResolveSnapshotPathRejectsAbsolutePathLabel(t *testing.T) {
	name := "20260712T030000Z_manual_/etc/passwd"
	_, err := ResolveSnapshotPath(pathTestVolume(), name)
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName for absolute path label, got %v", err)
	}
}

func TestResolveSnapshotPathRejectsBareDotDot(t *testing.T) {
	_, err := ResolveSnapshotPath(pathTestVolume(), "../../../etc/passwd")
	if !errors.Is(err, ErrInvalidSnapshotName) {
		t.Fatalf("expected ErrInvalidSnapshotName, got %v", err)
	}
}
