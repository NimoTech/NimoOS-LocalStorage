package v1

import (
	"testing"

	model1 "github.com/NimoTech/NimoOS-LocalStorage/model"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
)

func testLookup() raidLookup {
	reg := registeredArray{Name: "raid10", UUID: "aaaa:bbbb", Level: "raid10"}
	return raidLookup{
		activeLevel: map[string]string{"md127": "raid10"},
		registered:  map[string]registeredArray{"aaaa:bbbb": reg},
		byMdDevice:  map[string]registeredArray{"md127": reg},
	}
}

func fakeExamine(uuid, name string) func(string) (*mdadm.ExamineInfo, error) {
	return func(string) (*mdadm.ExamineInfo, error) {
		if uuid == "" {
			return nil, nil
		}
		return &mdadm.ExamineInfo{
			ArrayUUID: uuid, Name: name, Level: "raid5",
			CreationTime: "Thu Aug  6 21:54:49 2026", UpdateTime: "Fri Aug  7 00:29:17 2026",
		}, nil
	}
}

func TestClassifyDriveRaid(t *testing.T) {
	memberOfRunning := model1.LSBLKModel{
		Name: "sda", Path: "/dev/sda", FsType: "linux_raid_member",
		Children: []model1.LSBLKModel{{Name: "md127", Type: "raid10"}},
	}
	// The 2026-08-11 incident: foreign (ZimaOS) superblock, udev assembled it
	// as an inactive md126 — must be residue, offered with a wipe warning.
	zimaosResidue := model1.LSBLKModel{
		Name: "sdb", Path: "/dev/sdb", FsType: "linux_raid_member",
		Children: []model1.LSBLKModel{{Name: "md126", Type: "md"}},
	}
	// Boot retry window: registered array not assembled, member has bare
	// signature and no md child — must stay protected.
	retryWindowMember := model1.LSBLKModel{Name: "sdc", Path: "/dev/sdc", FsType: "linux_raid_member"}
	partitionMember := model1.LSBLKModel{
		Name: "sdd", Path: "/dev/sdd",
		Children: []model1.LSBLKModel{{Name: "sdd1", Path: "/dev/sdd1", Type: "part", FsType: "linux_raid_member"}},
	}
	clean := model1.LSBLKModel{Name: "sde", Path: "/dev/sde"}

	lk := testLookup()

	t.Run("member of running registered array", func(t *testing.T) {
		got := classifyDriveRaid(memberOfRunning, lk, fakeExamine("", ""))
		if got == nil || got.Role != "member" || !got.Active || !got.Registered || got.ArrayName != "raid10" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("foreign residue held by inactive md", func(t *testing.T) {
		got := classifyDriveRaid(zimaosResidue, lk, fakeExamine("5555:6666", "zimaos:fc56"))
		if got == nil || got.Role != "residue" || got.ArrayName != "zimaos:fc56" || got.MdDevice != "/dev/md126" {
			t.Errorf("got %+v", got)
		}
		if got.UpdatedAt == "" {
			t.Error("residue should carry the superblock update time")
		}
	})
	t.Run("registered array member during boot retry window", func(t *testing.T) {
		got := classifyDriveRaid(retryWindowMember, lk, fakeExamine("aaaa:bbbb", "NimoOS:0"))
		if got == nil || got.Role != "member" || !got.Registered || got.Active {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("signature on a partition", func(t *testing.T) {
		got := classifyDriveRaid(partitionMember, lk, fakeExamine("7777:8888", "old:array"))
		if got == nil || got.Role != "residue" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("clean disk", func(t *testing.T) {
		if got := classifyDriveRaid(clean, lk, fakeExamine("", "")); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
	t.Run("inactive md but examine unavailable still flags residue", func(t *testing.T) {
		got := classifyDriveRaid(zimaosResidue, lk, fakeExamine("", ""))
		if got == nil || got.Role != "residue" {
			t.Errorf("got %+v", got)
		}
	})
}
