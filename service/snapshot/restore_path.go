package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveWithinDir validates a caller-supplied relative path rel, once
// joined onto base, still resolves — after following any symlinks along
// the way — to somewhere inside base. This is restore's and file-versions'
// path-safety boundary (handoff §3.4: "path 拼接后 filepath.Clean 必须仍在
// 快照目录/卷挂载点内"), and defends independently against three escape
// shapes:
//
//  1. an absolute path (rejected outright — rel must be relative to base);
//  2. lexical "../" traversal (caught by filepath.Clean before any disk
//     access, so it's rejected even if base doesn't exist);
//  3. a symlink — at any level, including one whose target doesn't exist
//     yet only in the path's final component — redirecting outside base.
//     filepath.Clean alone cannot catch this (it's purely lexical), so this
//     also resolves real symlinks via resolveExistingPrefix.
//
// This deliberately talks to the real filesystem (os.Lstat /
// filepath.EvalSymlinks) rather than going through the injectable
// PathChecker seam (fs.go): it IS the security boundary, so it must observe
// genuine filesystem semantics rather than a fake's model of them. It's
// still fully unit-testable without root or btrfs — see
// restore_path_test.go, which uses only t.TempDir() and os.Symlink.
func resolveWithinDir(base, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative, got absolute path %q", rel)
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes its base directory", rel)
	}

	realBase, err := resolveExistingPrefix(base)
	if err != nil {
		return "", fmt.Errorf("resolve base directory %s: %w", base, err)
	}

	joined := filepath.Join(realBase, cleanRel)
	resolved, err := resolveExistingPrefix(joined)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if resolved != realBase && !strings.HasPrefix(resolved, realBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes its base directory", rel)
	}
	return resolved, nil
}

// resolveRestoreDestDir validates and resolves a caller-supplied absolute
// "dest_dir" override for POST /v2/snapshot/restore (choosing a restore
// destination directory other than relPath's original location): destDir
// must be an absolute path that already exists as a directory (it is never
// auto-created — a missing dest_dir is a caller error, not something to
// paper over), located under some volume in volumes that is both known-btrfs
// and currently mounted — the same "snapshot-supported" bar
// VolumeStatus.Supported uses (service.go), checked the same way
// Service.ResolveVolume does (filesystem + Runner.IsMounted).
//
// Three independent defenses mirror resolveWithinDir's own (restore's
// original-location path):
//
//  1. destDir must be absolute (rejected outright otherwise — no "../"
//     relative trickery to reason about lexically);
//  2. FindVolumeForPath's longest-mount-point-prefix match both confirms
//     destDir is under a known volume at all AND identifies which one;
//  3. resolveExistingPrefix resolves real symlinks (destDir itself or any
//     ancestor) so a symlink can't redirect the actual write outside that
//     volume's real mount point — filepath.Clean alone (step 2) is purely
//     lexical and cannot catch this.
//
// Cross-volume restores are allowed by design: the resolved volume need not
// be relPath's source volume — Copier's reflink-then-plain-copy fallback
// (copy.go) already handles a destination on a different filesystem.
func resolveRestoreDestDir(runner Runner, paths PathChecker, volumes []VolumeInfo, destDir string) (string, error) {
	if destDir == "" {
		return "", fmt.Errorf("%w: dest_dir must not be empty", ErrInvalidRestoreDestDir)
	}
	if !filepath.IsAbs(destDir) {
		return "", fmt.Errorf("%w: dest_dir must be an absolute path, got %q", ErrInvalidRestoreDestDir, destDir)
	}
	clean := filepath.Clean(destDir)

	vol, _, err := FindVolumeForPath(volumes, clean)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not under any known volume's mount point", ErrInvalidRestoreDestDir, destDir)
	}
	if !strings.EqualFold(vol.Filesystem, "btrfs") {
		return "", fmt.Errorf("%w: volume %s is not a snapshot-supported (btrfs) volume", ErrInvalidRestoreDestDir, vol.UUID)
	}
	mounted, err := runner.IsMounted(vol.DevicePath, vol.MountPoint)
	if err != nil {
		return "", fmt.Errorf("check destination volume mount state: %w", err)
	}
	if !mounted {
		return "", fmt.Errorf("%w: volume %s is not currently mounted", ErrInvalidRestoreDestDir, vol.UUID)
	}

	resolved, err := resolveExistingPrefix(clean)
	if err != nil {
		return "", fmt.Errorf("%w: resolve dest_dir %q: %v", ErrInvalidRestoreDestDir, destDir, err)
	}
	realMount, err := resolveExistingPrefix(vol.MountPoint)
	if err != nil {
		return "", fmt.Errorf("resolve volume mount point %s: %w", vol.MountPoint, err)
	}
	if resolved != realMount && !strings.HasPrefix(resolved, realMount+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes its volume's mount point", ErrInvalidRestoreDestDir, destDir)
	}

	info, ok, err := paths.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat dest_dir %s: %w", resolved, err)
	}
	if !ok {
		return "", fmt.Errorf("%w: dest_dir %q does not exist (it is never created automatically)", ErrInvalidRestoreDestDir, destDir)
	}
	if !info.IsDir {
		return "", fmt.Errorf("%w: dest_dir %q is not a directory", ErrInvalidRestoreDestDir, destDir)
	}

	return resolved, nil
}

// resolveExistingPrefix resolves symlinks along the longest existing prefix
// of path (mirroring filepath.EvalSymlinks, which requires the full path to
// exist), then rejoins any not-yet-existing trailing components literally.
// This lets it be used both for a source path (which must exist) and a
// not-yet-existing restore destination (whose parent directories may or may
// not exist) — either way, any *existing* component that happens to be a
// symlink is still resolved, so it can't redirect the result outside of
// wherever the caller expects.
func resolveExistingPrefix(path string) (string, error) {
	cur := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding anything that
			// exists; nothing left to resolve.
			break
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}

	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %s: %w", cur, err)
	}

	full := real
	for _, s := range suffix {
		full = filepath.Join(full, s)
	}
	return filepath.Clean(full), nil
}
