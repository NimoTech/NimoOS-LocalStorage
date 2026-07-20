package v1

import (
	"testing"

	model1 "github.com/NimoTech/NimoOS-LocalStorage/model"
)

// TestIsSnapshotInfraMount exercises the discriminator used to hide the
// btrfs @snapshots infrastructure subvolume from GetStorageList's output.
// The mountpoint's basename (".snapshots") is the discriminator chosen
// because lsblk (the only data source available in this code path — see
// service.MyService.Disk().LSBLK) does not report mount options such as
// the btrfs "subvol=" value, so the more specific subvol-based check isn't
// available here.
func TestIsSnapshotInfraMount(t *testing.T) {
	cases := []struct {
		name string
		mp   string
		want bool
	}{
		{"snapshots mount on RAID volume", "/media/RAID_0/.snapshots", true},
		{"snapshots mount on plain volume", "/media/Disk-1/.snapshots", true},
		{"root snapshots mount", "/.snapshots", true},
		{"normal RAID mount", "/media/RAID_0", false},
		{"normal disk mount", "/media/Disk-1", false},
		{"system_data is a different hidden dir, not this discriminator", "/media/Disk-1/.system_data", false},
		{"empty mountpoint", "", false},
		{"lookalike suffix must not match", "/media/Disk-1/not.snapshots", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSnapshotInfraMount(tc.mp); got != tc.want {
				t.Errorf("isSnapshotInfraMount(%q) = %v, want %v", tc.mp, got, tc.want)
			}
		})
	}
}

// TestNormalizeSnapshotChildren_RewritesToPrimaryMount is the direct repro
// of the acceptance defect: lsblk's singular "mountpoint" field only
// reports a device's LAST mount, so when a btrfs volume's @ subvolume
// (real storage) and @snapshots subvolume (infrastructure) are BOTH
// mounted on the same block device, the singular field ends up as the
// ".snapshots" path. GetStorageList must not discard the whole child in
// that case — it must rewrite MountPoint back to the real mount recovered
// from the plural "mountpoints" array.
func TestNormalizeSnapshotChildren_RewritesToPrimaryMount(t *testing.T) {
	children := []model1.LSBLKModel{
		{
			Name:        "sda1",
			Path:        "/dev/sda1",
			MountPoint:  "/media/RAID_0/.snapshots",
			MountPoints: []string{"/media/RAID_0/.snapshots", "/media/RAID_0"},
		},
	}

	got := normalizeSnapshotChildren(children)

	if len(got) != 1 {
		t.Fatalf("expected the RAID child to survive normalization, got %d: %+v", len(got), got)
	}
	if got[0].MountPoint != "/media/RAID_0" {
		t.Errorf("expected MountPoint rewritten to the primary mount, got %+v", got[0])
	}
}

// TestNormalizeSnapshotChildren_DropsWhenAllMountsAreSnapshotInfra covers
// the case where every mount recorded for the device is .snapshots
// infrastructure (or the mountpoints list is empty/blank) — this must
// still be hidden, preserving the pre-fix behavior for pure infra mounts.
func TestNormalizeSnapshotChildren_DropsWhenAllMountsAreSnapshotInfra(t *testing.T) {
	cases := []struct {
		name  string
		child model1.LSBLKModel
	}{
		{
			name: "only the snapshots mount is present",
			child: model1.LSBLKModel{
				Name:        "sda2",
				Path:        "/dev/sda2",
				MountPoint:  "/media/RAID_0/.snapshots",
				MountPoints: []string{"/media/RAID_0/.snapshots"},
			},
		},
		{
			name: "mountpoints is empty",
			child: model1.LSBLKModel{
				Name:        "sda2",
				Path:        "/dev/sda2",
				MountPoint:  "/media/RAID_0/.snapshots",
				MountPoints: []string{},
			},
		},
		{
			name: "mountpoints contains only blank entries",
			child: model1.LSBLKModel{
				Name:        "sda2",
				Path:        "/dev/sda2",
				MountPoint:  "/media/RAID_0/.snapshots",
				MountPoints: []string{""},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeSnapshotChildren([]model1.LSBLKModel{tc.child})
			if len(got) != 0 {
				t.Errorf("expected child to be dropped, got %+v", got)
			}
		})
	}
}

// TestNormalizeSnapshotChildren_PlainChildUntouched ensures ordinary
// children whose singular mountpoint isn't .snapshots infrastructure pass
// through unmodified.
func TestNormalizeSnapshotChildren_PlainChildUntouched(t *testing.T) {
	children := []model1.LSBLKModel{
		{Name: "sda1", Path: "/dev/sda1", MountPoint: "/media/RAID_0", MountPoints: []string{"/media/RAID_0"}},
	}

	got := normalizeSnapshotChildren(children)

	if len(got) != 1 {
		t.Fatalf("expected 1 child, got %d: %+v", len(got), got)
	}
	if got[0].MountPoint != "/media/RAID_0" {
		t.Errorf("expected untouched MountPoint, got %+v", got[0])
	}
}

// TestNormalizeSnapshotChildren_NoPluralFieldFallsBackToDrop covers older
// util-linux lsblk builds that don't emit "mountpoints" at all (MountPoints
// is nil). There's no plural data to recover the real mount from, so the
// child is dropped — same as the pre-fix behavior — rather than risk
// surfacing the wrong mountpoint.
func TestNormalizeSnapshotChildren_NoPluralFieldFallsBackToDrop(t *testing.T) {
	children := []model1.LSBLKModel{
		{Name: "sda2", Path: "/dev/sda2", MountPoint: "/media/RAID_0/.snapshots", MountPoints: nil},
	}

	got := normalizeSnapshotChildren(children)

	if len(got) != 0 {
		t.Errorf("expected child dropped when no plural mountpoints available, got %+v", got)
	}
}

// TestNormalizeSnapshotChildren_MultiMemberRAID reproduces the live symptom
// verbatim: a btrfs volume built on md-RAID exposes the SAME .snapshots
// mount once per member disk (sda, sdc, sdd), each also carrying the real
// mount in its plural mountpoints array. GetStorageList iterates each
// member disk independently, so every member must be rewritten to the real
// mount — not dropped — and never leak the .snapshots path.
func TestNormalizeSnapshotChildren_MultiMemberRAID(t *testing.T) {
	members := map[string][]model1.LSBLKModel{
		"/dev/sda": {
			{Name: "sda1", Path: "/dev/sda1", MountPoint: "/media/RAID_0/.snapshots", MountPoints: []string{"/media/RAID_0/.snapshots", "/media/RAID_0"}},
		},
		"/dev/sdc": {
			{Name: "sdc1", Path: "/dev/sdc1", MountPoint: "/media/RAID_0/.snapshots", MountPoints: []string{"/media/RAID_0/.snapshots", "/media/RAID_0"}},
		},
		"/dev/sdd": {
			{Name: "sdd1", Path: "/dev/sdd1", MountPoint: "/media/RAID_0/.snapshots", MountPoints: []string{"/media/RAID_0/.snapshots", "/media/RAID_0"}},
		},
	}

	for disk, children := range members {
		got := normalizeSnapshotChildren(children)
		if len(got) != 1 {
			t.Errorf("disk %s: expected exactly the normalized RAID child to survive, got %+v", disk, got)
			continue
		}
		if got[0].MountPoint != "/media/RAID_0" {
			t.Errorf("disk %s: expected MountPoint rewritten to /media/RAID_0, got %+v", disk, got[0])
		}
	}
}
