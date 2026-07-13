package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExecRunnerIntegration exercises the real ExecRunner against an actual
// loopback-backed btrfs filesystem: creates a subvolume layout, snapshots
// it, lists it, and deletes it. It requires root plus btrfs-progs and
// losetup; environments without those skip with a clear reason instead of
// failing, per the task brief ("无 btrfs 环境 t.Skip 并打印原因").
func TestExecRunnerIntegration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping btrfs integration test: requires root (mount/losetup)")
	}
	if _, err := exec.LookPath("mkfs.btrfs"); err != nil {
		t.Skip("skipping btrfs integration test: mkfs.btrfs not found (install btrfs-progs)")
	}
	if _, err := exec.LookPath("btrfs"); err != nil {
		t.Skip("skipping btrfs integration test: btrfs command not found (install btrfs-progs)")
	}
	if _, err := exec.LookPath("losetup"); err != nil {
		t.Skip("skipping btrfs integration test: losetup not found (install util-linux)")
	}

	dir := t.TempDir()
	imagePath := filepath.Join(dir, "btrfs.img")
	img, err := os.Create(imagePath)
	if err != nil {
		t.Fatalf("create image file: %v", err)
	}
	if err := img.Truncate(256 * 1024 * 1024); err != nil {
		img.Close()
		t.Fatalf("truncate image file: %v", err)
	}
	img.Close()

	if out, err := exec.Command("mkfs.btrfs", "-f", imagePath).CombinedOutput(); err != nil {
		t.Skipf("skipping btrfs integration test: mkfs.btrfs failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	losetupOut, err := exec.Command("losetup", "--find", "--show", imagePath).CombinedOutput()
	if err != nil {
		t.Skipf("skipping btrfs integration test: losetup unavailable: %v: %s", err, strings.TrimSpace(string(losetupOut)))
	}
	device := strings.TrimSpace(string(losetupOut))
	defer exec.Command("losetup", "-d", device).Run()

	mountDir := t.TempDir()
	ctx := context.Background()
	runner := NewExecRunner()

	if err := runner.MountTopLevel(ctx, device, mountDir); err != nil {
		t.Fatalf("MountTopLevel: %v", err)
	}
	defer runner.Unmount(ctx, mountDir)

	if err := runner.CreateSubvolume(ctx, filepath.Join(mountDir, "@")); err != nil {
		t.Fatalf("CreateSubvolume(@): %v", err)
	}
	if err := runner.CreateSubvolume(ctx, filepath.Join(mountDir, SnapshotsSubvolumeName)); err != nil {
		t.Fatalf("CreateSubvolume(@snapshots): %v", err)
	}
	// Idempotent: creating it again must not error.
	if err := runner.CreateSubvolume(ctx, filepath.Join(mountDir, SnapshotsSubvolumeName)); err != nil {
		t.Fatalf("CreateSubvolume(@snapshots) again: %v", err)
	}

	entries, err := runner.ListSubvolumes(ctx, mountDir)
	if err != nil {
		t.Fatalf("ListSubvolumes: %v", err)
	}
	var sawSnapshots bool
	for _, e := range entries {
		if e.Path == SnapshotsSubvolumeName {
			sawSnapshots = true
		}
	}
	if !sawSnapshots {
		t.Fatalf("expected %s in subvolume list, got %+v", SnapshotsSubvolumeName, entries)
	}

	snapName, err := FormatName(time.Now(), TypeManual, "integration")
	if err != nil {
		t.Fatalf("FormatName: %v", err)
	}
	snapDest := filepath.Join(mountDir, SnapshotsSubvolumeName, snapName)
	if err := runner.CreateReadOnlySnapshot(ctx, filepath.Join(mountDir, "@"), snapDest); err != nil {
		t.Fatalf("CreateReadOnlySnapshot: %v", err)
	}

	entries, err = runner.ListSubvolumes(ctx, mountDir)
	if err != nil {
		t.Fatalf("ListSubvolumes after snapshot: %v", err)
	}
	names := FilterSnapshotNames(entries)
	if len(names) != 1 || names[0] != snapName {
		t.Fatalf("FilterSnapshotNames = %v, want [%s]", names, snapName)
	}

	if err := runner.DeleteSubvolume(ctx, snapDest); err != nil {
		t.Fatalf("DeleteSubvolume: %v", err)
	}
}
