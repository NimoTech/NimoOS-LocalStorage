package snapshot

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveSnapshotPath validates a caller-supplied snapshot name (e.g. from
// DELETE /v2/snapshot/:name) and returns the absolute path to that snapshot
// subvolume on disk, under volume's .snapshots directory.
//
// Three independent checks defend against path traversal (handoff's "校验
// name 属于该卷 .snapshots,防路径穿越"):
//
//  1. name must not contain a path separator at all. A name produced by our
//     own FormatName never does (SanitizeLabel strips "/" and "\" from the
//     label at creation time) — so any name containing one is rejected
//     outright. This matters because ParseName's generic
//     "<timestamp>_<type>[_<label>]" split does not itself reject "/"
//     embedded in the label segment (that's only enforced by SanitizeLabel
//     on the *creation* path in FormatName); a name like
//     "20260101T000000Z_manual_/etc/passwd" would otherwise parse
//     successfully and, once joined under .snapshots, land on a
//     harmless-looking *nested* path rather than escaping it — which is
//     still not one of ours (no disk-observed name could ever match it;
//     FilterSnapshotNames drops multi-level entries) and must be rejected
//     before it's even worth trying to parse.
//  2. name must otherwise be recognized by ParseName — i.e. match our own
//     "<timestamp>_<type>[_<label>]" naming convention.
//  3. As defense in depth, after joining with the volume's .snapshots
//     directory and filepath.Clean-ing the result, the resulting path must
//     still live inside that directory.
//
// All checks must pass; any failing is reported as ErrInvalidSnapshotName.
func ResolveSnapshotPath(volume VolumeInfo, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty name", ErrInvalidSnapshotName)
	}
	if strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("%w: %q contains a path separator", ErrInvalidSnapshotName, name)
	}
	if _, err := ParseName(name); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidSnapshotName, err)
	}

	dir := filepath.Clean(SnapshotsDir(volume))
	full := filepath.Clean(filepath.Join(dir, name))
	if full != dir && !strings.HasPrefix(full, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes the volume's snapshots directory", ErrInvalidSnapshotName, name)
	}
	return full, nil
}
