package snapshot

import "github.com/NimoTech/NimoOS-LocalStorage/service/model"

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
