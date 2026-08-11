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

// findOldDiskMatches returns every attached member matching the disk being
// replaced — empty when it is no longer attached to the array.
//
// Device letters are reused after hot swaps: the stored path of a pulled disk
// may now belong to the brand-new replacement disk. So a serial always wins
// over a path, and a bare path is only trusted while it is still an attached
// member — never resolved via os.Stat, which would happily hit the new disk.
//
// Serials are not guaranteed unique (cheap USB-SATA bridges report one fake
// serial for every disk, cloned VM disks share theirs), so all matches are
// returned and the caller must refuse to act on an ambiguous result.
func findOldDiskMatches(attached []mdadm.MemberDisk, serialByPath map[string]string, oldSerial, oldPath string) []mdadm.MemberDisk {
	if oldSerial != "" {
		var out []mdadm.MemberDisk
		for _, m := range attached {
			if serialByPath[m.Path] == oldSerial {
				out = append(out, m)
			}
		}
		return out
	}
	for _, m := range attached {
		if m.Path == oldPath {
			return []mdadm.MemberDisk{m}
		}
	}
	return nil
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
