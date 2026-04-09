package mdadm

import (
	"testing"
)

// ---- fixtures ----

const detailHealthy = `/dev/md0:
           Version : 1.2
     Creation Time : Mon Jan  1 00:00:00 2024
        Raid Level : raid5
        Array Size : 1953512448 (1863.00 GiB 2000.40 GB)
     Used Dev Size : 976756224 (931.50 GiB 1000.20 GB)
      Raid Devices : 3
     Total Devices : 3
       Persistence : Superblock is persistent

       Update Time : Mon Jan  1 12:00:00 2024
             State : clean
    Active Devices : 3
   Working Devices : 3
    Failed Devices : 0
     Spare Devices : 0

            Layout : left-symmetric
        Chunk Size : 512K

Consistency Policy : resync

              Name : hostname:0  (local to host hostname)
              UUID : aabbccdd:11223344:55667788:99aabbcc
            Events : 100

    Number   Major   Minor   RaidDevice State
       0     8        0        0      active sync   /dev/sda
       1     8       16        1      active sync   /dev/sdb
       2     8       32        2      active sync   /dev/sdc
`

const detailRebuilding = `/dev/md0:
           Version : 1.2
        Raid Level : raid5
        Array Size : 1953512448 (1863.00 GiB 2000.40 GB)
     Used Dev Size : 976756224 (931.50 GiB 1000.20 GB)
      Raid Devices : 3
     Total Devices : 3

             State : degraded, recovering
    Active Devices : 2
   Working Devices : 3
    Failed Devices : 0
     Spare Devices : 1

     Rebuild Status : 45% complete

              UUID : 11223344:aabbccdd:55667788:99aabbcc

    Number   Major   Minor   RaidDevice State
       0     8        0        0      active sync   /dev/sda
       1     8       16        1      active sync   /dev/sdb
       3     8       48        2      spare rebuilding   /dev/sdd
`

const mdstatHealthy = `Personalities : [raid5] [raid6] [raid1]
md0 : active raid5 sdc[2] sdb[1] sda[0]
      1953512448 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/3] [UUU]

unused devices: <none>
`

const mdstatRebuilding = `Personalities : [raid5] [raid6] [raid1]
md0 : active raid5 sdd[3] sdb[1] sda[0]
      1953512448 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/2] [UU_]
      [===========>.........]  recovery = 45.2% (441548800/976756224) finish=95.3min speed=88888K/sec

unused devices: <none>
`

const mdstatMultiple = `Personalities : [raid1] [raid5]
md0 : active raid1 sdb[1] sda[0]
      976756224 blocks super 1.2 [2/2] [UU]

md1 : active raid5 sdf[2] sde[1] sdd[0]
      1953512448 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/3] [UUU]

unused devices: <none>
`

// ---- ParseDetail tests ----

func TestParseDetail_Healthy(t *testing.T) {
	d, err := ParseDetail(detailHealthy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Device != "/dev/md0" {
		t.Errorf("Device: got %q, want %q", d.Device, "/dev/md0")
	}
	if d.Level != "raid5" {
		t.Errorf("Level: got %q, want %q", d.Level, "raid5")
	}
	if d.State != "clean" {
		t.Errorf("State: got %q, want %q", d.State, "clean")
	}
	if d.UUID != "aabbccdd:11223344:55667788:99aabbcc" {
		t.Errorf("UUID: got %q, want %q", d.UUID, "aabbccdd:11223344:55667788:99aabbcc")
	}
	if d.ActiveDisks != 3 {
		t.Errorf("ActiveDisks: got %d, want 3", d.ActiveDisks)
	}
	if d.TotalDisks != 3 {
		t.Errorf("TotalDisks: got %d, want 3", d.TotalDisks)
	}
	if d.RebuildPct != -1 {
		t.Errorf("RebuildPct: got %f, want -1", d.RebuildPct)
	}
	if len(d.Members) != 3 {
		t.Fatalf("Members count: got %d, want 3", len(d.Members))
	}
	if d.Members[0].Path != "/dev/sda" {
		t.Errorf("Members[0].Path: got %q, want %q", d.Members[0].Path, "/dev/sda")
	}
	if d.Members[0].State != "active sync" {
		t.Errorf("Members[0].State: got %q, want %q", d.Members[0].State, "active sync")
	}
	if d.Members[0].Number != 0 {
		t.Errorf("Members[0].Number: got %d, want 0", d.Members[0].Number)
	}
	if d.Members[2].Path != "/dev/sdc" {
		t.Errorf("Members[2].Path: got %q, want %q", d.Members[2].Path, "/dev/sdc")
	}
}

func TestParseDetail_Rebuilding(t *testing.T) {
	d, err := ParseDetail(detailRebuilding)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Device != "/dev/md0" {
		t.Errorf("Device: got %q, want %q", d.Device, "/dev/md0")
	}
	if d.State != "degraded, recovering" {
		t.Errorf("State: got %q, want %q", d.State, "degraded, recovering")
	}
	if d.ActiveDisks != 2 {
		t.Errorf("ActiveDisks: got %d, want 2", d.ActiveDisks)
	}
	if d.RebuildPct != 45.0 {
		t.Errorf("RebuildPct: got %f, want 45.0", d.RebuildPct)
	}
	if len(d.Members) != 3 {
		t.Fatalf("Members count: got %d, want 3", len(d.Members))
	}
	// spare rebuilding member
	spare := d.Members[2]
	if spare.Path != "/dev/sdd" {
		t.Errorf("spare Path: got %q, want %q", spare.Path, "/dev/sdd")
	}
	if spare.State != "spare rebuilding" {
		t.Errorf("spare State: got %q, want %q", spare.State, "spare rebuilding")
	}
}

func TestParseDetail_Empty(t *testing.T) {
	_, err := ParseDetail("")
	if err == nil {
		t.Error("expected error for empty input, got nil")
	}
}

// ---- ParseMDStat tests ----

func TestParseMDStat_Healthy(t *testing.T) {
	entries, err := ParseMDStat(mdstatHealthy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count: got %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Device != "md0" {
		t.Errorf("Device: got %q, want %q", e.Device, "md0")
	}
	if e.State != "active" {
		t.Errorf("State: got %q, want %q", e.State, "active")
	}
	if e.Level != "raid5" {
		t.Errorf("Level: got %q, want %q", e.Level, "raid5")
	}
	if len(e.Members) != 3 {
		t.Fatalf("Members count: got %d, want 3", len(e.Members))
	}
	if e.DiskStatus != "[UUU]" {
		t.Errorf("DiskStatus: got %q, want %q", e.DiskStatus, "[UUU]")
	}
	if e.RebuildPct != -1 {
		t.Errorf("RebuildPct: got %f, want -1", e.RebuildPct)
	}
}

func TestParseMDStat_Rebuilding(t *testing.T) {
	entries, err := ParseMDStat(mdstatRebuilding)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count: got %d, want 1", len(entries))
	}
	e := entries[0]
	if e.DiskStatus != "[UU_]" {
		t.Errorf("DiskStatus: got %q, want %q", e.DiskStatus, "[UU_]")
	}
	if e.RebuildPct != 45.2 {
		t.Errorf("RebuildPct: got %f, want 45.2", e.RebuildPct)
	}
}

func TestParseMDStat_Multiple(t *testing.T) {
	entries, err := ParseMDStat(mdstatMultiple)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count: got %d, want 2", len(entries))
	}
	if entries[0].Device != "md0" {
		t.Errorf("entries[0].Device: got %q, want %q", entries[0].Device, "md0")
	}
	if entries[0].Level != "raid1" {
		t.Errorf("entries[0].Level: got %q, want %q", entries[0].Level, "raid1")
	}
	if entries[1].Device != "md1" {
		t.Errorf("entries[1].Device: got %q, want %q", entries[1].Device, "md1")
	}
	if entries[1].Level != "raid5" {
		t.Errorf("entries[1].Level: got %q, want %q", entries[1].Level, "raid5")
	}
	if len(entries[1].Members) != 3 {
		t.Errorf("entries[1] Members count: got %d, want 3", len(entries[1].Members))
	}
}

func TestParseMDStat_Empty(t *testing.T) {
	entries, err := ParseMDStat("")
	if err != nil {
		t.Fatalf("unexpected error for empty input: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}
