package v2

import (
	"fmt"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"go.uber.org/zap"
)

// diskRaidTrace is what checkNewDiskRaidTraces found on a candidate disk.
type diskRaidTrace struct {
	// Protected: the disk belongs to an array this system runs or has
	// registered — never usable as a new member, no flag can override.
	Protected bool
	ArrayName string
	// LastActive is the superblock's update time — shown to the user so they
	// can judge how stale the residue is.
	LastActive string
}

// raidTraceGuard turns a trace into the refusal error for using the disk as
// a new array member, or nil when the operation may proceed. Pure — the
// decision table is unit-tested separately from the probing.
func raidTraceGuard(path string, trace *diskRaidTrace, wipeResidue bool) error {
	switch {
	case trace == nil:
		return nil
	case trace.Protected:
		return fmt.Errorf("disk %s is a member of RAID array %q on this system; it cannot be used as a replacement", path, trace.ArrayName)
	case !wipeResidue:
		return fmt.Errorf("disk %s carries RAID metadata of array %q (last active %s); wiping it requires explicit confirmation", path, trace.ArrayName, trace.LastActive)
	default:
		return nil
	}
}

// findDiskRaidTrace probes a candidate member disk for md traces: an md
// device currently holding it, or a superblock on the disk / one of its
// partitions. A running holder or a superblock matching a registered array
// marks the disk protected; any other superblock is foreign residue.
func (s *raidService) findDiskRaidTrace(path string) *diskRaidTrace {
	// 1. A *running* md device claiming the disk → protected, whatever the DB says.
	if dev, err := lsblkQueryMemberDisk(path); err == nil {
		var plan memberPrepPlan
		collectHolderArrays(dev, &plan)
		if len(plan.HolderArrays) > 0 {
			if entries, err := mdadm.ReadMDStat(); err == nil {
				for _, holder := range plan.HolderArrays {
					for _, e := range entries {
						if e.Device == holder && e.State == "active" {
							return &diskRaidTrace{Protected: true, ArrayName: holder}
						}
					}
				}
			}
		}
	}

	// 2. Superblock identity — the disk itself, then its partitions.
	ex := examineDiskAndPartitions(path)
	if ex == nil {
		return nil
	}
	trace := &diskRaidTrace{ArrayName: ex.Name, LastActive: ex.UpdateTime}
	if raids, err := s.listRAIDs(); err == nil {
		for _, r := range raids {
			if r.UUID == ex.ArrayUUID {
				trace.Protected = true
				trace.ArrayName = r.Name
				break
			}
		}
	}
	return trace
}

// examineDiskAndPartitions reads the md superblock of the disk or, failing
// that, of its first partition that carries one. nil = no superblock found.
func examineDiskAndPartitions(path string) *mdadm.ExamineInfo {
	if ex, err := mdadm.Examine(path); err == nil && ex != nil {
		return ex
	} else if err != nil {
		logger.Info("mdadm examine failed", zap.String("disk", path), zap.Error(err))
	}
	dev, err := lsblkQueryMemberDisk(path)
	if err != nil {
		return nil
	}
	for _, child := range dev.Children {
		if child.Type != "part" || child.Path == "" {
			continue
		}
		if ex, err := mdadm.Examine(child.Path); err == nil && ex != nil {
			return ex
		}
	}
	return nil
}
