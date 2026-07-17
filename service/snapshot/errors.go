package snapshot

import "errors"

// Sentinel errors Service methods return, so the API layer (route/snapshot.go)
// can map them to HTTP status codes with errors.Is instead of string matching.
var (
	// ErrVolumeNotFound means the given volume_uuid doesn't match any
	// currently known volume.
	ErrVolumeNotFound = errors.New("snapshot: volume not found")
	// ErrVolumeNotBtrfs means the volume exists but its filesystem isn't
	// btrfs, so it can never support snapshots.
	ErrVolumeNotBtrfs = errors.New("snapshot: volume filesystem is not btrfs")
	// ErrVolumeNotMounted means the volume is known (e.g. present in the
	// RAID array table) but isn't currently mounted, so no snapshot
	// operation against it can be trusted right now.
	ErrVolumeNotMounted = errors.New("snapshot: volume is not currently mounted")
	// ErrVolumeNotSupported means EnsureSnapshotsMount could not make
	// @snapshots available for this volume (see the wrapped reason).
	ErrVolumeNotSupported = errors.New("snapshot: volume does not support snapshots")
	// ErrInvalidSnapshotName means a caller-supplied snapshot name failed
	// naming/path validation (see ResolveSnapshotPath) — either it doesn't
	// match our naming convention, or it would escape the volume's
	// .snapshots directory.
	ErrInvalidSnapshotName = errors.New("snapshot: invalid snapshot name")
	// ErrSnapshotNotFound means the named snapshot doesn't currently exist
	// on disk for the given volume (post-reconciliation).
	ErrSnapshotNotFound = errors.New("snapshot: snapshot not found")
	// ErrInvalidRestorePath means a caller-supplied restore/file-versions
	// "path" is malformed or would escape the volume/snapshot boundary:
	// empty, absolute, "../" traversal, or a symlink redirecting outside
	// the volume's mount point or the snapshot's directory (see
	// resolveWithinDir in restore_path.go).
	ErrInvalidRestorePath = errors.New("snapshot: invalid restore path")
	// ErrRestoreSourceNotFound means the requested path does not exist
	// inside the given (on-disk, validated) snapshot.
	ErrRestoreSourceNotFound = errors.New("snapshot: restore source not found in snapshot")
	// ErrRestoreDestinationExists means the destination Copy was about to
	// write to already exists, despite computeRestoreDestination having
	// found it free moments earlier: another process (most plausibly a
	// concurrent restore) created something at that exact path in the
	// window between that check and the actual copy. This is retryable —
	// a fresh call to Restore will get a freshly (re-)computed, still
	// collision-free destination from computeRestoreDestination.
	ErrRestoreDestinationExists = errors.New("snapshot: restore destination already exists")
	// ErrInvalidRestoreDestDir means a caller-supplied restore "dest_dir"
	// override (POST /v2/snapshot/restore) failed validation: empty or not
	// an absolute path, not located under any currently known volume's
	// mount point, that volume isn't a currently mounted, snapshot-supported
	// (btrfs) volume, a symlink redirecting outside that volume's real
	// mount point, or the directory doesn't already exist (dest_dir is
	// never auto-created) — see resolveRestoreDestDir in restore_path.go.
	ErrInvalidRestoreDestDir = errors.New("snapshot: invalid restore destination directory")
	// ErrInvalidRestoreOnConflict means a caller-supplied restore
	// "on_conflict" value (POST /v2/snapshot/restore) is neither empty,
	// "keep_both", nor "overwrite" — see RestoreOptions.onConflict.
	ErrInvalidRestoreOnConflict = errors.New("snapshot: invalid restore on_conflict value")
	// ErrRestoreOverwriteUnsupported means on_conflict=overwrite was
	// requested but either the restore source or the existing destination
	// it would replace is a directory: the atomic copy-to-temp-then-rename
	// replacement this feature performs is only safe for a single file (see
	// Service.restoreOverwrite's doc comment for why this doesn't generalize
	// to directories). Callers hitting this should fall back to
	// on_conflict=keep_both.
	ErrRestoreOverwriteUnsupported = errors.New("snapshot: overwrite is not supported for directories, use on_conflict=keep_both")
)
