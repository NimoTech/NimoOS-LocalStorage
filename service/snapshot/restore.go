package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// maxRestoreSuffixAttempts bounds computeRestoreDestination's numbering
// search, so a pathological pile-up of prior ".restored-*" files can't spin
// forever.
const maxRestoreSuffixAttempts = 10000

// RestoreResult is the response for POST /v2/snapshot/restore.
type RestoreResult struct {
	RestoredPath string `json:"restored_path"`
}

// RestoreOptions carries POST /v2/snapshot/restore's optional overrides,
// beyond volume/snapshot/path, of the default restore behavior.
type RestoreOptions struct {
	// DestDir, if non-empty, is an absolute path to an existing directory to
	// restore into, overriding the default of restoring back to relPath's
	// original parent directory under the source volume's mount point.
	// Validated by resolveRestoreDestDir (restore_path.go): must exist
	// already (never auto-created), and must be located under some
	// currently mounted, snapshot-supported (btrfs) volume's mount point —
	// but that volume need not be relPath's source volume; restoring across
	// volumes is allowed.
	DestDir string
	// WithMarker controls whether the restored name gets the
	// ".restored-<ts>" marker inserted (see computeRestoreDestination). nil
	// or a pointer to true reproduces the original, marker-always-inserted
	// behavior. A pointer to false restores under the original name
	// instead — collisions are still never overwritten: they get a bare
	// numeric "-<n>" suffix (in the same before-the-extension position the
	// marker would otherwise occupy) rather than skipping or clobbering.
	WithMarker *bool
}

// withMarker resolves opts.WithMarker's nil-means-true default.
func (o RestoreOptions) withMarker() bool {
	return o.WithMarker == nil || *o.WithMarker
}

// computeRestoreDestination picks the destination for restoring a file or
// directory named name into destDir, timestamped ts, guaranteeing it never
// overwrites an existing path (handoff §2.1/§6: "取回...不覆盖现有同名文
// 件"/"restore 永不覆盖:目标已存在...同名时追加序号"):
//
//   - withMarker=true (the original, default behavior) — regular files with
//     a recognizable extension: the ".restored-<ts>" marker is inserted
//     BEFORE the extension, so the restored copy keeps the extension the
//     OS/UI use to recognize its type and open it — "photo.jpg" ->
//     "photo.restored-<ts>.jpg" (numbered on collision:
//     "photo.restored-<ts>-2.jpg", then "-3", ...); directories, dotfiles,
//     and extensionless files get the marker appended at the end instead —
//     "<name>.restored-<ts>" (numbered the same way). See splitRestoreExt
//     for exactly what counts as "an extension" (dotfiles and extensionless
//     names don't).
//   - withMarker=false — the unnumbered first choice (n==1) is name
//     unchanged, with no marker at all. A collision falls back to the same
//     placement rule as above (before the extension for a regular file with
//     one, appended at the end otherwise) but with a bare "-<n>" suffix
//     instead of ".restored-<ts>[-<n>]" — "report.docx" ->
//     "report-2.docx", "Projects" -> "Projects-2".
//
// isDir must reflect the SOURCE's type (the restore flow already stats the
// source to confirm it exists — reuse that result rather than re-stating
// here) since a directory named e.g. "archive.tar" must not have its name
// split just because it contains a dot.
//
// This is the actual safety mechanism (not just documentation): Copy itself
// (copy.go) performs no existence check of its own, so whatever path this
// function returns is guaranteed not to collide at the moment it was
// checked.
func computeRestoreDestination(paths PathChecker, destDir, name, ts string, isDir, withMarker bool) (string, error) {
	for n := 1; n <= maxRestoreSuffixAttempts; n++ {
		candidate := restoreCandidateName(name, ts, isDir, withMarker, n)
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

// restoreCandidateName builds the nth candidate name (n starting at 1, so
// n==1 is the unnumbered/unmarked first choice) for restoring name at
// timestamp ts. See computeRestoreDestination for the placement rules this
// implements.
func restoreCandidateName(name, ts string, isDir, withMarker bool, n int) string {
	suffix := restoreSuffix(ts, withMarker, n)
	if suffix == "" {
		return name
	}
	if isDir {
		return name + suffix
	}
	stem, ext, hasExt := splitRestoreExt(name)
	if !hasExt {
		return name + suffix
	}
	return stem + suffix + ext
}

// restoreSuffix builds the nth name suffix (n starting at 1): with
// withMarker, the ".restored-<ts>[-<n>]" marker (n==1 has no "-<n>"); without
// it, n==1 is the empty string (no suffix at all, i.e. the original name),
// and n>1 is a bare "-<n>".
func restoreSuffix(ts string, withMarker bool, n int) string {
	if withMarker {
		if n == 1 {
			return ".restored-" + ts
		}
		return fmt.Sprintf(".restored-%s-%d", ts, n)
	}
	if n == 1 {
		return ""
	}
	return fmt.Sprintf("-%d", n)
}

// splitRestoreExt splits name into a stem and extension for the purpose of
// inserting the ".restored-<ts>" marker before the extension instead of
// after it. It reports hasExt=false (name should be treated as
// extensionless, i.e. the marker is simply appended) when:
//
//   - name contains no "." at all ("README", extensionless), or
//   - name's only "." is a leading one, e.g. ".bashrc" — a dotfile, not an
//     extension.
//
// Otherwise the split point is the LAST "." in name, so a name with
// multiple dots only has its final segment treated as "the extension" —
// e.g. "archive.tar.gz" splits into stem "archive.tar" and ext ".gz"
// (-> "archive.tar.restored-<ts>.gz"), and a name like ".hidden.txt" (a
// leading dot AND another dot) splits into ".hidden" / ".txt". Not
// recognizing compound extensions like ".tar.gz" as a single unit is an
// accepted tradeoff for keeping this split simple.
func splitRestoreExt(name string) (stem, ext string, hasExt bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, "", false
	}
	return name[:i], name[i:], true
}

// Restore copies relPath (a path relative to volume's root) out of
// snapshotName back into volume's live filesystem, landing — by default —
// at "<relPath>.restored-<ts>" (numbered if that already exists) rather
// than ever overwriting anything — POST /v2/snapshot/restore (handoff
// §3.4). opts can override where it lands (opts.DestDir) and whether the
// ".restored-<ts>" marker is inserted at all (opts.WithMarker); see
// RestoreOptions. This is the payoff of the whole snapshot feature (it
// exists so a destructive-move incident becomes a 30-second reflink copy
// instead of a 6-hour recovery), so its safety bar is the highest in this
// package:
//
//   - snapshotName is validated the same way DELETE already validates it
//     (ResolveSnapshotPath: naming convention + no path separators + Clean
//     stays inside the volume's .snapshots directory) and must actually
//     exist on disk right now (post-reconciliation) — not just be a
//     syntactically valid name.
//   - relPath is validated on both sides — inside the snapshot (source) and
//     inside the live volume (destination) — via resolveWithinDir, which
//     independently defends against absolute paths, "../" traversal, and
//     symlink escape (restore_path.go). When opts.DestDir is set, the
//     destination side is instead validated by resolveRestoreDestDir, which
//     defends the same three ways against a caller-chosen directory
//     (absolute-only, known-volume-prefix match, symlink resolution) and
//     additionally requires the directory to already exist.
//   - the source must exist inside the snapshot, or this fails with
//     ErrRestoreSourceNotFound *before* touching the destination at all —
//     never partially creating a destination for a source that isn't
//     there.
//   - the destination NAME is chosen by computeRestoreDestination, which
//     never returns a path that already exists, regardless of
//     opts.WithMarker.
//
// volumes is the caller's current enumeration of every known volume (e.g.
// VolumesFromRAIDArrays(raids)) — needed (independently of volume, the
// snapshot's source volume) only when opts.DestDir is set, since a
// dest_dir override may legitimately point at a different volume than the
// source (cross-volume restore is allowed).
func (s *Service) Restore(ctx context.Context, volumes []VolumeInfo, volume VolumeInfo, snapshotName, relPath string, opts RestoreOptions) (RestoreResult, error) {
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
	srcInfo, ok, err := s.Paths.Stat(srcPath)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("stat restore source: %w", err)
	}
	if !ok {
		return RestoreResult{}, ErrRestoreSourceNotFound
	}

	var destParent string
	if opts.DestDir != "" {
		destParent, err = resolveRestoreDestDir(s.Runner, s.Paths, volumes, opts.DestDir)
		if err != nil {
			return RestoreResult{}, err
		}
		// destParent is caller-chosen and must already exist (resolveRestoreDestDir
		// enforces this) — unlike the default path below, it is deliberately
		// NOT auto-created.
	} else {
		liveBase, err := resolveExistingPrefix(volume.MountPoint)
		if err != nil {
			return RestoreResult{}, fmt.Errorf("resolve volume mount point %s: %w", volume.MountPoint, err)
		}

		destParentRel := filepath.Dir(relClean)
		destParent = liveBase
		if destParentRel != "." {
			destParent, err = resolveWithinDir(liveBase, destParentRel)
			if err != nil {
				return RestoreResult{}, fmt.Errorf("%w: %v", ErrInvalidRestorePath, err)
			}
		}

		if err := s.Paths.MkdirAll(destParent); err != nil {
			return RestoreResult{}, fmt.Errorf("ensure restore destination directory %s: %w", destParent, err)
		}
	}

	ts := s.clock().Now().UTC().Format(nameTimeLayout)
	name := filepath.Base(relClean)
	destPath, err := computeRestoreDestination(s.Paths, destParent, name, ts, srcInfo.IsDir, opts.withMarker())
	if err != nil {
		return RestoreResult{}, err
	}

	if err := s.Copier.Copy(ctx, srcPath, destPath); err != nil {
		return RestoreResult{}, fmt.Errorf("copy restore source to destination: %w", err)
	}

	return RestoreResult{RestoredPath: destPath}, nil
}
