package v2

import (
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// attachedMembers filters mdadm detail rows down to disks actually attached to
// the array, dropping the pathless "removed" placeholder rows left behind by a
// physically pulled disk.
func attachedMembers(members []mdadm.MemberDisk) []mdadm.MemberDisk {
	var out []mdadm.MemberDisk
	for _, m := range members {
		if m.Path != "" {
			out = append(out, m)
		}
	}
	return out
}

// findOldDiskLivePath returns the current device path of the disk being
// replaced, or "" when it is no longer attached to the array.
//
// Device letters are reused after hot swaps: the stored path of a pulled disk
// may now belong to the brand-new replacement disk. So a serial always wins
// over a path, and a bare path is only trusted while it is still an attached
// member — never resolved via os.Stat, which would happily hit the new disk.
func findOldDiskLivePath(attached []mdadm.MemberDisk, serialByPath map[string]string, oldSerial, oldPath string) string {
	if oldSerial != "" {
		for _, m := range attached {
			if serialByPath[m.Path] == oldSerial {
				return m.Path
			}
		}
		return ""
	}
	for _, m := range attached {
		if m.Path == oldPath {
			return oldPath
		}
	}
	return ""
}

// memberRowToReplace picks the DB member row to rewrite with the new disk's
// identifiers. Serial match wins; the cached path is a fallback for legacy
// clients that only send a path.
func memberRowToReplace(members []*model.RAIDMember, oldSerial, oldPath string) *model.RAIDMember {
	if oldSerial != "" {
		for _, m := range members {
			if m.DiskSerial == oldSerial {
				return m
			}
		}
		return nil
	}
	for _, m := range members {
		if m.DevicePathCache == oldPath {
			return m
		}
	}
	return nil
}
