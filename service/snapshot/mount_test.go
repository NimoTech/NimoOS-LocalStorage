package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
)

func init() {
	logger.LogInitConsoleOnly()
}

// testVolume returns a VolumeInfo backed by a real temp directory (so
// EnsureSnapshotsMount's os.MkdirAll(".snapshots") succeeds without root),
// per test.
func testVolume(t *testing.T) VolumeInfo {
	t.Helper()
	return VolumeInfo{
		UUID:       "vol-uuid-1",
		DevicePath: "/dev/md0",
		MountPoint: t.TempDir(),
		Filesystem: "btrfs",
	}
}

func TestEnsureSnapshotsMountNonBtrfsIsUnsupported(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	vol.Filesystem = "ext4"

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Supported {
		t.Error("Supported = true, want false for non-btrfs volume")
	}
	if status.Reason == "" {
		t.Error("Reason is empty, want an explanation")
	}
	if len(runner.CreatedSubvolumes) != 0 {
		t.Error("expected no CreateSubvolume calls for a non-btrfs volume")
	}
}

func TestEnsureSnapshotsMountAlreadyMountedIsNoop(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	runner.SeedMounted(vol.DevicePath, SnapshotsDir(vol))

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Supported || !status.Mounted {
		t.Errorf("status = %+v, want Supported=true Mounted=true", status)
	}
	if len(runner.CreatedSubvolumes) != 0 {
		t.Error("expected no CreateSubvolume calls when already mounted")
	}
	if len(persister.Persisted) != 0 {
		t.Error("expected no fstab persistence when already mounted")
	}
}

func TestEnsureSnapshotsMountExistingSubvolumeJustMounts(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	runner.SeedTopLevelSubvolume(vol.DevicePath, "@")
	runner.SeedTopLevelSubvolume(vol.DevicePath, SnapshotsSubvolumeName)

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Supported || !status.Mounted {
		t.Errorf("status = %+v, want Supported=true Mounted=true", status)
	}
	if len(runner.CreatedSubvolumes) != 0 {
		t.Errorf("expected no CreateSubvolume calls, @snapshots already exists; got %v", runner.CreatedSubvolumes)
	}
	entry, ok := persister.Persisted[SnapshotsDir(vol)]
	if !ok {
		t.Fatal("expected fstab entry to be persisted")
	}
	if entry.FSType != "btrfs" || !strings.Contains(entry.Options, SnapshotsSubvolumeName) {
		t.Errorf("persisted entry = %+v, want fstype btrfs and options mentioning %s", entry, SnapshotsSubvolumeName)
	}
	// The parent volume isn't in fstab (it's mounted from the DB at service
	// startup, after local-fs.target), so this entry MUST be nofail: without
	// it, a boot where the .snapshots mountpoint doesn't exist yet fails
	// local-fs.target and drops the box into emergency mode.
	if !strings.Contains(entry.Options, "nofail") {
		t.Errorf("persisted entry options = %q, want it to contain \"nofail\" so a missing parent mount at boot doesn't fail local-fs.target", entry.Options)
	}
	mounted, _ := runner.IsMounted(vol.DevicePath, SnapshotsDir(vol))
	if !mounted {
		t.Error("expected runner to report the volume as mounted after EnsureSnapshotsMount")
	}
}

func TestEnsureSnapshotsMountLegacyVolumeCreatesSubvolume(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	// Legacy volume: only "@" exists, no "@snapshots" yet.
	runner.SeedTopLevelSubvolume(vol.DevicePath, "@")

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Supported || !status.Mounted {
		t.Errorf("status = %+v, want Supported=true Mounted=true", status)
	}
	if len(runner.CreatedSubvolumes) != 1 {
		t.Fatalf("expected exactly one CreateSubvolume call, got %v", runner.CreatedSubvolumes)
	}
	if !strings.HasSuffix(runner.CreatedSubvolumes[0], "/"+SnapshotsSubvolumeName) {
		t.Errorf("CreateSubvolume path = %q, want suffix /%s", runner.CreatedSubvolumes[0], SnapshotsSubvolumeName)
	}
}

func TestEnsureSnapshotsMountIncompatibleLayoutIsUnsupported(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	runner.CreateSubvolumeErrByDevice[vol.DevicePath] = errors.New("read-only filesystem")

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Supported {
		t.Error("Supported = true, want false when @snapshots can't be created")
	}
	if !strings.Contains(status.Reason, "read-only filesystem") {
		t.Errorf("Reason = %q, want it to mention the underlying error", status.Reason)
	}
	if len(persister.Persisted) != 0 {
		t.Error("expected no fstab persistence when unsupported")
	}
}

func TestEnsureSnapshotsMountMountFailureIsUnsupported(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	runner.SeedTopLevelSubvolume(vol.DevicePath, SnapshotsSubvolumeName)
	runner.MountSubvolumeErr[vol.DevicePath] = errors.New("mount: wrong fs type")

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Supported {
		t.Error("Supported = true, want false when the mount itself fails")
	}
	if !strings.Contains(status.Reason, "wrong fs type") {
		t.Errorf("Reason = %q, want it to mention the underlying error", status.Reason)
	}
}

func TestEnsureSnapshotsMountPersistFailureIsNonFatal(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	runner.SeedTopLevelSubvolume(vol.DevicePath, SnapshotsSubvolumeName)
	persister.PersistErr[SnapshotsDir(vol)] = errors.New("disk full writing /etc/fstab")

	status, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Supported || !status.Mounted {
		t.Errorf("status = %+v, want Supported=true Mounted=true even if fstab persistence fails", status)
	}
}

func TestEnsureSnapshotsMountRejectsMissingFields(t *testing.T) {
	runner := NewFakeRunner()
	persister := NewFakeFstabPersister()
	vol := testVolume(t)
	vol.MountPoint = ""

	if _, err := EnsureSnapshotsMount(context.Background(), runner, persister, vol); err == nil {
		t.Error("expected error for missing mount point, got nil")
	}
}
