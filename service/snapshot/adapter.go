package snapshot

import (
	"path/filepath"
	"strings"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// VolumesFromRAIDArrays adapts RAID service records into the VolumeInfo
// shape this package works with (mount.go's VolumeInfo doc comment names
// "the RAID service today" as the initial volume source; today, btrfs
// volumes in this codebase are RAID arrays — there is no standalone
// single-disk btrfs volume concept yet).
func VolumesFromRAIDArrays(raids []*model.RAIDArray) []VolumeInfo {
	volumes := make([]VolumeInfo, 0, len(raids))
	for _, r := range raids {
		if r == nil {
			continue
		}
		volumes = append(volumes, VolumeInfo{
			UUID:       r.UUID,
			DevicePath: r.DevicePath,
			MountPoint: r.MountPoint,
			Filesystem: r.Filesystem,
		})
	}
	return volumes
}

// FindVolumeForPath locates which of volumes contains absPath — GET
// /v2/snapshot/file-versions's "绝对路径 → 定位所属卷" step (handoff §3.4)
// — matching by the longest mount-point prefix, so a volume mounted at
// "/media/RAID" isn't mistakenly picked over one mounted at
// "/media/RAID/nested" for a path inside the latter. Returns the matching
// volume and absPath's path relative to that volume's mount point (the
// "path" ResolveSnapshotPath's sibling functions expect). ErrVolumeNotFound
// if no volume's mount point is a prefix of absPath.
func FindVolumeForPath(volumes []VolumeInfo, absPath string) (VolumeInfo, string, error) {
	clean := filepath.Clean(absPath)

	var best VolumeInfo
	bestLen := -1
	for _, v := range volumes {
		mp := filepath.Clean(v.MountPoint)
		if mp == "" {
			continue
		}
		if clean != mp && !strings.HasPrefix(clean, mp+string(filepath.Separator)) {
			continue
		}
		if len(mp) > bestLen {
			bestLen = len(mp)
			best = v
		}
	}
	if bestLen < 0 {
		return VolumeInfo{}, "", ErrVolumeNotFound
	}

	rel, err := filepath.Rel(filepath.Clean(best.MountPoint), clean)
	if err != nil {
		return VolumeInfo{}, "", err
	}
	return best, rel, nil
}
