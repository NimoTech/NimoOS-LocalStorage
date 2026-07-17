package snapshot

import (
	"os"
	"time"
)

// PathInfo is the subset of file metadata restore/file-versions need.
type PathInfo struct {
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// PathChecker is the injectable seam for the plain filesystem checks
// restore and file-versions need beyond btrfs commands (Runner) and
// copying (Copier): "does this destination already exist" (restore's
// never-overwrite suffix-numbering search) and "what's this path's
// size/mtime" (file-versions). Kept separate from Runner because these are
// ordinary filesystem operations, not btrfs-specific external commands.
//
// OSPathChecker is the production implementation. FakePathChecker
// (fs_fake.go) is an in-memory test double used to unit test pure
// orchestration logic (e.g. computeRestoreDestination's suffix-numbering)
// without touching disk; the fuller Service.Restore/FileVersions tests use
// OSPathChecker against real temp directories (restore_test.go /
// file_versions_test.go), matching this package's existing convention of
// using t.TempDir() as a VolumeInfo.MountPoint (see route/snapshot_test.go's
// btrfsVolume helper).
type PathChecker interface {
	// Exists reports whether path currently exists (any type).
	Exists(path string) (bool, error)
	// Stat returns size/mtime/isDir for path if it currently exists; ok is
	// false (err nil) if it doesn't.
	Stat(path string) (info PathInfo, ok bool, err error)
	// MkdirAll ensures dir (and any missing parents) exist, so restoring a
	// file whose original parent directory no longer exists in the live
	// volume — the exact "the whole folder is gone" incident this feature
	// exists for — can still succeed.
	MkdirAll(dir string) error
	// Rename atomically replaces newPath with oldPath (moving oldPath to
	// newPath in a single filesystem operation). Restore's
	// on_conflict=overwrite path (restore.go's restoreOverwrite) is this
	// method's only caller: it copies the restore source to a temporary
	// file alongside the real destination, then Renames the temporary file
	// into place, so the real destination is only ever replaced by one
	// atomic rename — never by an in-place overwrite that could leave a
	// half-written file if it failed partway through.
	Rename(oldPath, newPath string) error
	// Remove deletes path if it exists (a no-op, not an error, if it
	// doesn't). Restore's on_conflict=overwrite path uses this to clean up
	// its temporary file if the copy-to-temp or the rename-into-place step
	// fails, so a failed overwrite never leaves stray
	// "<name>.nimoos-restoring-*" files behind.
	Remove(path string) error
}

// OSPathChecker is the production PathChecker, backed by the real
// filesystem.
type OSPathChecker struct{}

var _ PathChecker = OSPathChecker{}

func (OSPathChecker) Exists(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (OSPathChecker) Stat(path string) (PathInfo, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PathInfo{}, false, nil
		}
		return PathInfo{}, false, err
	}
	return PathInfo{Size: fi.Size(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}, true, nil
}

func (OSPathChecker) MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func (OSPathChecker) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (OSPathChecker) Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
