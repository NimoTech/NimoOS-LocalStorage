package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
)

// maxRestoreSuffixAttempts bounds computeRestoreDestination's numbering
// search, so a pathological pile-up of prior ".restored-*" files can't spin
// forever.
const maxRestoreSuffixAttempts = 10000

// RestoreResult is the response for POST /v2/snapshot/restore.
type RestoreResult struct {
	RestoredPath string `json:"restored_path"`
}

// computeRestoreDestination picks the destination for restoring a file or
// directory named name into destDir, timestamped ts, guaranteeing it never
// overwrites an existing path (handoff §2.1/§6: "取回...不覆盖现有同名文
// 件"/"restore 永不覆盖:目标已存在...同名时追加序号"):
//
//   - first choice: "<name>.restored-<ts>"
//   - if that already exists: "<name>.restored-<ts>-2", then "-3", ...
//
// This is the actual safety mechanism (not just documentation): Copy itself
// (copy.go) performs no existence check of its own, so whatever path this
// function returns is guaranteed not to collide at the moment it was
// checked.
func computeRestoreDestination(paths PathChecker, destDir, name, ts string) (string, error) {
	base := fmt.Sprintf("%s.restored-%s", name, ts)
	for n := 1; n <= maxRestoreSuffixAttempts; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		full := filepath.Join(destDir, candidate)
		exists, err := paths.Exists(full)
		if err != nil {
			return "", fmt.Errorf("check restore destination %s: %w", full, err)
		}
		if !exists {
			return full, nil
		}
	}
	return "", fmt.Errorf("too many existing restore destinations for %s (giving up after %d attempts)", name, maxRestoreSuffixAttempts)
}

// Restore copies relPath (a path relative to volume's root) out of
// snapshotName back into volume's live filesystem, landing at
// "<relPath>.restored-<ts>" (numbered if that already exists) rather than
// ever overwriting anything — POST /v2/snapshot/restore (handoff §3.4).
// This is the payoff of the whole snapshot feature (it exists so a
// destructive-move incident becomes a 30-second reflink copy instead of a
// 6-hour recovery), so its safety bar is the highest in this package:
//
//   - snapshotName is validated the same way DELETE already validates it
//     (ResolveSnapshotPath: naming convention + no path separators + Clean
//     stays inside the volume's .snapshots directory) and must actually
//     exist on disk right now (post-reconciliation) — not just be a
//     syntactically valid name.
//   - relPath is validated on both sides — inside the snapshot (source) and
//     inside the live volume (destination) — via resolveWithinDir, which
//     independently defends against absolute paths, "../" traversal, and
//     symlink escape (restore_path.go).
//   - the source must exist inside the snapshot, or this fails with
//     ErrRestoreSourceNotFound *before* touching the destination at all —
//     never partially creating a destination for a source that isn't
//     there.
//   - the destination is chosen by computeRestoreDestination, which never
//     returns a path that already exists.
func (s *Service) Restore(ctx context.Context, volume VolumeInfo, snapshotName, relPath string) (RestoreResult, error) {
	snapDir, err := ResolveSnapshotPath(volume, snapshotName)
	if err != nil {
		return RestoreResult{}, err
	}

	if _, err := ReconcileVolume(ctx, s.Runner, s.Store, volume); err != nil {
		return RestoreResult{}, fmt.Errorf("reconcile volume %s: %w", volume.UUID, err)
	}
	recs, err := s.Store.ListSnapshots(volume.UUID)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("list snapshots for volume %s: %w", volume.UUID, err)
	}
	exists := false
	for _, r := range recs {
		if r.Name == snapshotName {
			exists = true
			break
		}
	}
	if !exists {
		return RestoreResult{}, ErrSnapshotNotFound
	}

	relClean := filepath.Clean(relPath)
	if relClean == "." {
		return RestoreResult{}, fmt.Errorf("%w: refusing to restore the volume root (path \".\")", ErrInvalidRestorePath)
	}

	srcPath, err := resolveWithinDir(snapDir, relClean)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%w: %v", ErrInvalidRestorePath, err)
	}
	if _, ok, err := s.Paths.Stat(srcPath); err != nil {
		return RestoreResult{}, fmt.Errorf("stat restore source: %w", err)
	} else if !ok {
		return RestoreResult{}, ErrRestoreSourceNotFound
	}

	liveBase, err := resolveExistingPrefix(volume.MountPoint)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("resolve volume mount point %s: %w", volume.MountPoint, err)
	}

	destParentRel := filepath.Dir(relClean)
	destParent := liveBase
	if destParentRel != "." {
		destParent, err = resolveWithinDir(liveBase, destParentRel)
		if err != nil {
			return RestoreResult{}, fmt.Errorf("%w: %v", ErrInvalidRestorePath, err)
		}
	}

	if err := s.Paths.MkdirAll(destParent); err != nil {
		return RestoreResult{}, fmt.Errorf("ensure restore destination directory %s: %w", destParent, err)
	}

	ts := s.clock().Now().UTC().Format(nameTimeLayout)
	name := filepath.Base(relClean)
	destPath, err := computeRestoreDestination(s.Paths, destParent, name, ts)
	if err != nil {
		return RestoreResult{}, err
	}

	if err := s.Copier.Copy(ctx, srcPath, destPath); err != nil {
		return RestoreResult{}, fmt.Errorf("copy restore source to destination: %w", err)
	}

	return RestoreResult{RestoredPath: destPath}, nil
}
