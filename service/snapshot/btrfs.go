package snapshot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

// DefaultCommandTimeout bounds every external command Runner executes, so a
// wedged btrfs/mount binary can never hang the caller (handoff §3.1: "所有
// 外部命令经 exec 封装、超时保护、stderr 入日志").
const DefaultCommandTimeout = 30 * time.Second

// SubvolumeEntry is one row parsed from `btrfs subvolume list`.
type SubvolumeEntry struct {
	// ID is the btrfs subvolume ID (opaque, informational only).
	ID string
	// Path is the subvolume's path relative to the filesystem's top level,
	// e.g. "@snapshots" or "@snapshots/20260712T030000Z_auto-hourly".
	Path string
}

// Runner is the seam between this package and the real btrfs/mount tooling.
// Production code uses ExecRunner; tests use FakeRunner (btrfs_fake.go) so
// naming/ensure-mount/reconciliation logic can be fully unit tested without
// root or a real btrfs filesystem.
type Runner interface {
	// CreateReadOnlySnapshot runs `btrfs subvolume snapshot -r sourcePath destPath`.
	CreateReadOnlySnapshot(ctx context.Context, sourcePath, destPath string) error
	// DeleteSubvolume runs `btrfs subvolume delete path`.
	DeleteSubvolume(ctx context.Context, path string) error
	// CreateSubvolume runs `btrfs subvolume create path`. Idempotent: an
	// "already exists" failure is treated as success, mirroring
	// createBtrfsSubvolume in service/v2/raid_filesystem.go.
	CreateSubvolume(ctx context.Context, path string) error
	// ListSubvolumes runs `btrfs subvolume list mountPoint` and returns every
	// subvolume in that filesystem (paths are relative to the filesystem's
	// top level, regardless of which subvolume mountPoint itself is).
	ListSubvolumes(ctx context.Context, mountPoint string) ([]SubvolumeEntry, error)
	// MountTopLevel mounts device's top-level (subvolid=5) view at mountPoint,
	// giving access to every subvolume regardless of the default-mounted one.
	MountTopLevel(ctx context.Context, device, mountPoint string) error
	// MountSubvolume mounts device's named top-level subvolume (e.g.
	// "@snapshots") at mountPoint.
	MountSubvolume(ctx context.Context, device, mountPoint, subvolume string) error
	// Unmount runs `umount mountPoint`.
	Unmount(ctx context.Context, mountPoint string) error
	// IsMounted reports whether device is currently mounted at mountPoint.
	// Reads /proc/mounts directly; no exec involved (mirrors
	// isAlreadyMounted in service/v2/raid_filesystem.go).
	IsMounted(device, mountPoint string) (bool, error)
	// Fstype returns the filesystem type reported for mountPoint in
	// /proc/mounts (e.g. "btrfs"), or "" if it isn't currently mounted.
	Fstype(mountPoint string) (string, error)
}

// FilterSnapshotNames extracts snapshot subvolume names — the direct
// children of @snapshots — from a full `btrfs subvolume list` result
// (handoff §3.2: "以 btrfs subvolume list(过滤 .snapshots 下)为事实来源").
// Entries deeper than one level under @snapshots (which shouldn't normally
// occur) are ignored rather than mis-parsed.
func FilterSnapshotNames(entries []SubvolumeEntry) []string {
	prefix := SnapshotsSubvolumeName + "/"
	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		name := strings.TrimPrefix(e.Path, prefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		names = append(names, name)
	}
	return names
}

// ExecRunner is the production Runner: every method shells out to the real
// btrfs/mount/umount binaries, with a timeout and stderr-to-log on failure.
type ExecRunner struct {
	// Timeout bounds each command; DefaultCommandTimeout is used if zero.
	Timeout time.Duration
}

var _ Runner = (*ExecRunner)(nil)

// NewExecRunner returns an ExecRunner configured with DefaultCommandTimeout.
func NewExecRunner() *ExecRunner {
	return &ExecRunner{Timeout: DefaultCommandTimeout}
}

func (r *ExecRunner) timeout() time.Duration {
	if r.Timeout <= 0 {
		return DefaultCommandTimeout
	}
	return r.Timeout
}

func (r *ExecRunner) run(parent context.Context, name string, args ...string) ([]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout())
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		logger.Error("snapshot: command failed",
			zap.String("command", name),
			zap.Strings("args", args),
			zap.String("output", strings.TrimSpace(string(out))),
			zap.Error(err))
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (r *ExecRunner) CreateReadOnlySnapshot(ctx context.Context, sourcePath, destPath string) error {
	_, err := r.run(ctx, "btrfs", "subvolume", "snapshot", "-r", sourcePath, destPath)
	return err
}

func (r *ExecRunner) DeleteSubvolume(ctx context.Context, path string) error {
	_, err := r.run(ctx, "btrfs", "subvolume", "delete", path)
	return err
}

func (r *ExecRunner) CreateSubvolume(ctx context.Context, path string) error {
	out, err := r.run(ctx, "btrfs", "subvolume", "create", path)
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "file exists") {
			return nil
		}
		return err
	}
	return nil
}

func (r *ExecRunner) ListSubvolumes(ctx context.Context, mountPoint string) ([]SubvolumeEntry, error) {
	out, err := r.run(ctx, "btrfs", "subvolume", "list", mountPoint)
	if err != nil {
		return nil, err
	}
	return parseSubvolumeList(out), nil
}

func (r *ExecRunner) MountTopLevel(ctx context.Context, device, mountPoint string) error {
	_, err := r.run(ctx, "mount", "-t", "btrfs", "-o", "subvolid=5", device, mountPoint)
	return err
}

func (r *ExecRunner) MountSubvolume(ctx context.Context, device, mountPoint, subvolume string) error {
	opt := "subvol=/" + strings.TrimPrefix(subvolume, "/")
	_, err := r.run(ctx, "mount", "-t", "btrfs", "-o", opt, device, mountPoint)
	return err
}

func (r *ExecRunner) Unmount(ctx context.Context, mountPoint string) error {
	_, err := r.run(ctx, "umount", mountPoint)
	return err
}

func (r *ExecRunner) IsMounted(device, mountPoint string) (bool, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false, fmt.Errorf("read /proc/mounts: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == device && fields[1] == mountPoint {
			return true, nil
		}
	}
	return false, nil
}

func (r *ExecRunner) Fstype(mountPoint string) (string, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", fmt.Errorf("read /proc/mounts: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == mountPoint {
			return fields[2], nil
		}
	}
	return "", nil
}

// parseSubvolumeList parses the plain-text output of `btrfs subvolume list`,
// e.g. a line like:
//
//	ID 257 gen 15 top level 5 path @snapshots
//
// into a SubvolumeEntry{ID: "257", Path: "@snapshots"}. Unrecognized/blank
// lines are skipped rather than erroring, so minor btrfs version differences
// in the surrounding fields don't break parsing.
func parseSubvolumeList(out []byte) []SubvolumeEntry {
	var entries []SubvolumeEntry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)

		var id, path string
		for i := 0; i < len(fields); i++ {
			switch fields[i] {
			case "ID":
				if i+1 < len(fields) {
					id = fields[i+1]
				}
			case "path":
				if i+1 < len(fields) {
					path = strings.Join(fields[i+1:], " ")
				}
			}
		}
		if path == "" {
			continue
		}
		entries = append(entries, SubvolumeEntry{ID: id, Path: path})
	}
	return entries
}
