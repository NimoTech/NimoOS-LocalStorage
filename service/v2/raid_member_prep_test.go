package v2

import (
	"strings"
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
)

func TestPlanMemberDiskPrep_TwoPartitionsOneMounted(t *testing.T) {
	disk := memberBlockDevice{
		Name: "sdb",
		Path: "/dev/sdb",
		Type: "disk",
		Children: []memberBlockDevice{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", MountPoint: "/mnt/old"},
			{Name: "sdb2", Path: "/dev/sdb2", Type: "part"},
		},
	}

	plan, err := planMemberDiskPrep(disk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Unmounts) != 1 || plan.Unmounts[0] != "/mnt/old" {
		t.Fatalf("Unmounts = %v, want [/mnt/old]", plan.Unmounts)
	}
	want := []string{"/dev/sdb1", "/dev/sdb2", "/dev/sdb"}
	if len(plan.WipeTargets) != len(want) {
		t.Fatalf("WipeTargets = %v, want %v", plan.WipeTargets, want)
	}
	for i := range want {
		if plan.WipeTargets[i] != want[i] {
			t.Fatalf("WipeTargets = %v, want %v", plan.WipeTargets, want)
		}
	}
}

func TestPlanMemberDiskPrep_CollectsHolderArrays(t *testing.T) {
	disk := memberBlockDevice{
		Name: "sdc",
		Path: "/dev/sdc",
		Type: "disk",
		Children: []memberBlockDevice{
			{Name: "sdc1", Path: "/dev/sdc1", Type: "part", Children: []memberBlockDevice{
				{Name: "md1", Path: "/dev/md1", Type: "raid1"},
			}},
			// The same md device can appear more than once in the lsblk tree
			// (e.g. via both the whole disk and the partition path), so it must be deduplicated.
			{Name: "md1", Path: "/dev/md1", Type: ""},
		},
	}

	plan, err := planMemberDiskPrep(disk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.HolderArrays) != 1 || plan.HolderArrays[0] != "md1" {
		t.Fatalf("HolderArrays = %v, want [md1]", plan.HolderArrays)
	}
	// A disk occupied by an array: the array device itself must never end up in the wipe list.
	for _, w := range plan.WipeTargets {
		if strings.HasPrefix(w, "/dev/md") {
			t.Fatalf("WipeTargets = %v must not contain md devices", plan.WipeTargets)
		}
	}
}

func TestClassifyHolder(t *testing.T) {
	entries := []mdadm.MDStatEntry{
		{Device: "md0", State: "active", Level: "raid5"},
		{Device: "md1", State: "inactive"},
	}

	t.Run("active holder is refused", func(t *testing.T) {
		needStop, err := classifyHolder(entries, "md0")
		if err == nil || !strings.Contains(err.Error(), "active RAID array md0") {
			t.Fatalf("err = %v, want active RAID array refusal", err)
		}
		if needStop {
			t.Fatal("needStop = true, want false for active holder")
		}
	})

	t.Run("inactive holder needs stop", func(t *testing.T) {
		needStop, err := classifyHolder(entries, "md1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !needStop {
			t.Fatal("needStop = false, want true for inactive holder")
		}
	})

	t.Run("holder missing from mdstat needs nothing", func(t *testing.T) {
		needStop, err := classifyHolder(entries, "md9")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if needStop {
			t.Fatal("needStop = true, want false for already-released holder")
		}
	})
}

func TestPlanMemberDiskPrep_WholeDiskFilesystemMounted(t *testing.T) {
	disk := memberBlockDevice{
		Name:       "sdd",
		Path:       "/dev/sdd",
		Type:       "disk",
		MountPoint: "/mnt/usbstick",
	}

	plan, err := planMemberDiskPrep(disk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Unmounts) != 1 || plan.Unmounts[0] != "/mnt/usbstick" {
		t.Fatalf("Unmounts = %v, want [/mnt/usbstick]", plan.Unmounts)
	}
	if len(plan.WipeTargets) != 1 || plan.WipeTargets[0] != "/dev/sdd" {
		t.Fatalf("WipeTargets = %v, want [/dev/sdd]", plan.WipeTargets)
	}
}

func TestPlanMemberDiskPrep_RefusesSystemMounts(t *testing.T) {
	cases := []struct {
		name       string
		mountPoint string
	}{
		{"root", "/"},
		{"DATA", "/DATA"},
		{"DATA subdir", "/DATA/Documents"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			disk := memberBlockDevice{
				Name:       "nvme0n1",
				Path:       "/dev/nvme0n1",
				Type:       "disk",
				MountPoint: c.mountPoint,
			}
			_, err := planMemberDiskPrep(disk)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "system mount") {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), "system mount")
			}
		})
	}
}

func TestPlanMemberDiskPrep_CleanBareDisk(t *testing.T) {
	disk := memberBlockDevice{
		Name: "sde",
		Path: "/dev/sde",
		Type: "disk",
	}

	plan, err := planMemberDiskPrep(disk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Unmounts) != 0 {
		t.Fatalf("Unmounts = %v, want empty", plan.Unmounts)
	}
	if len(plan.WipeTargets) != 1 || plan.WipeTargets[0] != "/dev/sde" {
		t.Fatalf("WipeTargets = %v, want [/dev/sde]", plan.WipeTargets)
	}
}
