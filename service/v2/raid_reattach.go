package v2

import (
	"sort"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/diskid"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"go.uber.org/zap"
)

// ReattachMember is a disk that belongs to an array (its superblock carries
// the array's UUID) but is not currently attached to it — the signature left
// by pulling a member from a *running* array and plugging it back: the
// member's event count falls behind and udev will not hot re-add it, so the
// array stays degraded until an explicit `mdadm --re-add`.
type ReattachMember struct {
	Path       string `json:"path"`
	Serial     string `json:"serial,omitempty"`
	Role       string `json:"role,omitempty"`        // superblock Device Role, e.g. "Active device 1"
	LastUpdate string `json:"last_update,omitempty"` // when the member was last in sync
}

// findReattachableMembers picks, out of candidate disks, the ones whose md
// superblock belongs to arrayUUID while the disk is not attached to the
// array. Pure decision — examine is injected for tests.
func findReattachableMembers(arrayUUID string, attached map[string]bool, candidates []string, examine func(string) (*mdadm.ExamineInfo, error)) []ReattachMember {
	var out []ReattachMember
	for _, path := range candidates {
		if path == "" || attached[path] {
			continue
		}
		ex, err := examine(path)
		if err != nil || ex == nil || ex.ArrayUUID != arrayUUID {
			continue
		}
		out = append(out, ReattachMember{Path: path, Role: ex.DeviceRole, LastUpdate: ex.UpdateTime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// detachedMembers probes the system for this array's re-attachable member
// disks. detail must be the array's current mdadm detail.
func (s *raidService) detachedMembers(arrayUUID string, detail *mdadm.ArrayDetail) []ReattachMember {
	attached := map[string]bool{}
	for _, m := range detail.Members {
		if m.Path != "" {
			attached[m.Path] = true
		}
	}
	serials := diskid.SerialMap()
	candidates := make([]string, 0, len(serials))
	for path := range serials {
		candidates = append(candidates, path)
	}
	sort.Strings(candidates)
	members := findReattachableMembers(arrayUUID, attached, candidates, mdadm.Examine)
	for i := range members {
		members[i].Serial = serials[members[i].Path]
	}
	return members
}

// reattachDetachedMembers reclaims every re-attachable member disk back into
// the array: --re-add first (bitmap delta resync), plain --add as fallback
// (full resync of that member — still this array's own disk, never a wipe).
// Returns the device paths that were successfully brought back.
func (s *raidService) reattachDetachedMembers(devicePath, arrayUUID string) []string {
	detail, err := mdadm.Detail(devicePath)
	if err != nil {
		logger.Info("mdadm detail failed before reattach", zap.String("device", devicePath), zap.Error(err))
		return nil
	}
	var readded []string
	for _, m := range s.detachedMembers(arrayUUID, detail) {
		if err := mdadm.ReAddDisk(devicePath, m.Path); err != nil {
			logger.Info("re-add failed, falling back to --add (full resync of this member)",
				zap.String("disk", m.Path), zap.Error(err))
			if err := mdadm.AddDisk(devicePath, m.Path); err != nil {
				logger.Error("add fallback failed during reattach", zap.String("disk", m.Path), zap.Error(err))
				continue
			}
		}
		readded = append(readded, m.Path)
	}
	return readded
}
