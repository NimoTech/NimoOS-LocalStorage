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

// TestFilterSnapshotChildren_ExcludesSnapshotMount is the direct repro of
// the acceptance defect: GetStorageList must never surface the .snapshots
// infrastructure mount as a regular storage child, while leaving unrelated
// mounts on the same disk untouched.
func TestFilterSnapshotChildren_ExcludesSnapshotMount(t *testing.T) {
	children := []model1.LSBLKModel{
		{Name: "sda1", Path: "/dev/sda1", MountPoint: "/media/RAID_0"},
		{Name: "sda2", Path: "/dev/sda2", MountPoint: "/media/RAID_0/.snapshots"},
	}

	got := filterSnapshotChildren(children)

	if len(got) != 1 {
		t.Fatalf("expected 1 child after filtering, got %d: %+v", len(got), got)
	}
	if got[0].MountPoint != "/media/RAID_0" {
		t.Errorf("expected surviving child to be the normal mount, got %+v", got[0])
	}
	for _, c := range got {
		if c.MountPoint == "/media/RAID_0/.snapshots" {
			t.Errorf("snapshot mount leaked through filter: %+v", c)
		}
	}
}

// TestFilterSnapshotChildren_MultiMemberRAID reproduces the live symptom
// verbatim: a btrfs volume built on md-RAID exposes the SAME .snapshots
// mount once per member disk (sda, sdc, sdd). GetStorageList iterates each
// member disk independently, so the filter must strip .snapshots on every
// member — not just the first — or the UI still renders duplicate
// ".snapshots" drives with Format/Remove buttons.
func TestFilterSnapshotChildren_MultiMemberRAID(t *testing.T) {
	members := map[string][]model1.LSBLKModel{
		"/dev/sda": {
			{Name: "sda1", Path: "/dev/sda1", MountPoint: "/media/RAID_0"},
			{Name: "sda1", Path: "/dev/sda1", MountPoint: "/media/RAID_0/.snapshots"},
		},
		"/dev/sdc": {
			{Name: "sdc1", Path: "/dev/sdc1", MountPoint: "/media/RAID_0"},
			{Name: "sdc1", Path: "/dev/sdc1", MountPoint: "/media/RAID_0/.snapshots"},
		},
		"/dev/sdd": {
			{Name: "sdd1", Path: "/dev/sdd1", MountPoint: "/media/RAID_0"},
			{Name: "sdd1", Path: "/dev/sdd1", MountPoint: "/media/RAID_0/.snapshots"},
		},
	}

	for disk, children := range members {
		got := filterSnapshotChildren(children)
		for _, c := range got {
			if c.MountPoint == "/media/RAID_0/.snapshots" {
				t.Errorf("disk %s: snapshot mount leaked through filter: %+v", disk, c)
			}
		}
		if len(got) != 1 {
			t.Errorf("disk %s: expected exactly the normal mount to survive, got %+v", disk, got)
		}
	}
}
