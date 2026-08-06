package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// MaxFileVersionsSnapshots bounds how many of a volume's most recent
// snapshots FileVersions inspects, so a volume with a long snapshot history
// can't turn one GET into an unbounded stat storm (handoff §3.4: "cap the
// traversal count (most recent 60 snapshots)").
const MaxFileVersionsSnapshots = 60

// FileVersion is one row of GET /v2/snapshot/file-versions (handoff §3.4 /
// §2.2): relPath's size/mtime as it exists in one particular snapshot.
// Snapshots where relPath doesn't exist are omitted entirely (handoff:
// "don't list snapshots where it doesn't exist"), never returned with zero values.
type FileVersion struct {
	Snapshot string    `json:"snapshot"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mtime"`
}

// FileVersions returns, most-recent-snapshot-first, up to
// MaxFileVersionsSnapshots versions of relPath (a path relative to volume's
// root, as returned by FindVolumeForPath) found across volume's snapshots.
func (s *Service) FileVersions(ctx context.Context, volume VolumeInfo, relPath string) ([]FileVersion, error) {
	if _, err := ReconcileVolume(ctx, s.Runner, s.Store, volume); err != nil {
		return nil, fmt.Errorf("reconcile volume %s: %w", volume.UUID, err)
	}
	recs, err := s.Store.ListSnapshots(volume.UUID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots for volume %s: %w", volume.UUID, err)
	}

	sort.Slice(recs, func(i, j int) bool { return recs[i].CreatedAt.After(recs[j].CreatedAt) })
	if len(recs) > MaxFileVersionsSnapshots {
		recs = recs[:MaxFileVersionsSnapshots]
	}

	relClean := filepath.Clean(relPath)
	versions := make([]FileVersion, 0, len(recs))
	for _, r := range recs {
		snapDir := filepath.Join(SnapshotsDir(volume), r.Name)
		full, err := resolveWithinDir(snapDir, relClean)
		if err != nil {
			// relPath's validity doesn't depend on any particular
			// snapshot's on-disk layout (it's the same caller-supplied
			// path every time) — a traversal/escape attempt here is a
			// caller error that should fail the whole request loudly,
			// not be silently swallowed into an empty result the same way
			// a merely-absent file is.
			return nil, fmt.Errorf("%w: %v", ErrInvalidRestorePath, err)
		}
		info, ok, err := s.Paths.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("stat %s in snapshot %s: %w", relPath, r.Name, err)
		}
		if !ok {
			continue
		}
		versions = append(versions, FileVersion{Snapshot: r.Name, Size: info.Size, ModTime: info.ModTime})
	}
	return versions, nil
}
