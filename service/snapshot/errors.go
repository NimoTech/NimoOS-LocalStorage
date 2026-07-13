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
)
