package v1

import (
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	model1 "github.com/NimoTech/NimoOS-LocalStorage/model"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	"go.uber.org/zap"
)

// raidLookup carries the system-wide RAID facts a single disk is classified
// against: which md devices are running, and which arrays this system has
// registered in its DB (a registered array's members stay protected even
// while the array is inactive, e.g. during the boot retry window).
type raidLookup struct {
	activeLevel map[string]string          // md name ("md127") → level, active arrays only
	registered  map[string]registeredArray // array UUID → registered array
	byMdDevice  map[string]registeredArray // md name → registered array
}

type registeredArray struct {
	Name  string
	UUID  string
	Level string
}

// buildRaidLookup snapshots mdstat + the RAID DB once per disk-list request.
func buildRaidLookup() raidLookup {
	lk := raidLookup{
		activeLevel: map[string]string{},
		registered:  map[string]registeredArray{},
		byMdDevice:  map[string]registeredArray{},
	}
	if entries, err := mdadm.ReadMDStat(); err == nil {
		for _, e := range entries {
			if e.State == "active" {
				lk.activeLevel[e.Device] = e.Level
			}
		}
	} else {
		logger.Info("read mdstat failed while building raid lookup", zap.Error(err))
	}
	raids, err := service.MyService.RAID().ListRAIDArrays()
	if err != nil {
		logger.Info("list RAID arrays failed while building raid lookup", zap.Error(err))
		return lk
	}
	for _, r := range raids {
		reg := registeredArray{Name: r.Name, UUID: r.UUID, Level: mdadm.LevelName(r.Level)}
		lk.registered[r.UUID] = reg
		lk.byMdDevice[strings.TrimPrefix(r.DevicePath, "/dev/")] = reg
	}
	return lk
}

// collectMdDescendantNames walks the lsblk tree collecting every md device
// that claims this disk (e.g. "md127"), without descending into them.
func collectMdDescendantNames(dev model1.LSBLKModel) []string {
	var out []string
	for _, c := range dev.Children {
		if strings.HasPrefix(c.Type, "raid") || strings.HasPrefix(c.Name, "md") {
			out = append(out, c.Name)
			continue
		}
		out = append(out, collectMdDescendantNames(c)...)
	}
	return out
}

// raidSignatureDevice returns the device to `mdadm --examine`: the disk
// itself when it is a whole-disk member, else its first raid-member
// partition, else "".
func raidSignatureDevice(dev model1.LSBLKModel) string {
	if dev.FsType == "linux_raid_member" {
		return dev.Path
	}
	for _, c := range dev.Children {
		if c.FsType == "linux_raid_member" && c.Path != "" {
			return c.Path
		}
	}
	return ""
}

// classifyDriveRaid decides a disk's relationship to md RAID.
//
//   - attached to a *running* md device → member (protected)
//   - superblock UUID matches a *registered* array → member (protected even
//     when the array is inactive — boot retry window, mount failure)
//   - any other superblock → residue: a foreign or abandoned array's
//     leftover, usable after an explicit wipe confirmation
//   - nothing → nil
//
// examine is injected so the decision table is unit-testable.
func classifyDriveRaid(dev model1.LSBLKModel, lk raidLookup, examine func(string) (*mdadm.ExamineInfo, error)) *model1.DriveRaidInfo {
	mdNames := collectMdDescendantNames(dev)
	for _, name := range mdNames {
		if lvl, ok := lk.activeLevel[name]; ok {
			info := &model1.DriveRaidInfo{
				Role: "member", MdDevice: "/dev/" + name, Level: lvl,
				Active: true, ArrayName: name,
			}
			if reg, ok := lk.byMdDevice[name]; ok {
				info.ArrayName, info.ArrayUUID, info.Registered = reg.Name, reg.UUID, true
				if reg.Level != "" {
					info.Level = reg.Level
				}
			}
			return info
		}
	}

	sig := raidSignatureDevice(dev)
	if sig == "" && len(mdNames) == 0 {
		return nil
	}
	if sig == "" {
		sig = dev.Path
	}
	ex, err := examine(sig)
	if err != nil || ex == nil {
		if len(mdNames) > 0 {
			// Held by an inactive md we cannot identify — still a RAID trace;
			// surface it as residue rather than pretending the disk is clean.
			return &model1.DriveRaidInfo{Role: "residue", ArrayName: mdNames[0], MdDevice: "/dev/" + mdNames[0]}
		}
		return nil
	}

	info := &model1.DriveRaidInfo{
		ArrayName: ex.Name, ArrayUUID: ex.ArrayUUID, Level: ex.Level,
		CreatedAt: ex.CreationTime, UpdatedAt: ex.UpdateTime,
	}
	if len(mdNames) > 0 {
		info.MdDevice = "/dev/" + mdNames[0]
	}
	if reg, ok := lk.registered[ex.ArrayUUID]; ok {
		info.Role, info.Registered = "member", true
		info.ArrayName = reg.Name
	} else {
		info.Role = "residue"
	}
	return info
}
