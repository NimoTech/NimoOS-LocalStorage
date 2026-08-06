package mdadm

import (
	"strings"
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

// detailDegradedFaulty 是 2026-07-28 从真机 scsi_debug 测试台逐字抓下来的
// `mdadm --detail /dev/md0` 输出(RAID 5 三成员,对 sda 打过 --fail 但未 --remove)。
//
// 上面两个 fixture 是手编的,漏掉了真实输出里最关键的一行:故障盘会以
// RaidDevice 字段为 `-`(而非槽位号)的形式单独列在表格末尾。detailRebuilding
// 甚至写着 `Failed Devices : 0` 却声称 degraded —— 现实中不会出现。手编 fixture
// 让 memberRe 的 `\d+` 缺陷一直全绿,直到实盘验收才暴露。新增/修改这个解析器时
// 请以真机输出为准。
const detailDegradedFaulty = `/dev/md0:
           Version : 1.2
     Creation Time : Tue Jul 28 18:15:25 2026
        Raid Level : raid5
        Array Size : 1044480 (1020.00 MiB 1069.55 MB)
     Used Dev Size : 522240 (510.00 MiB 534.77 MB)
      Raid Devices : 3
     Total Devices : 3
       Persistence : Superblock is persistent

       Update Time : Tue Jul 28 18:16:17 2026
             State : clean, degraded
    Active Devices : 2
   Working Devices : 2
    Failed Devices : 1
     Spare Devices : 0

            Layout : left-symmetric
        Chunk Size : 512K

Consistency Policy : resync

              Name : NimoOS:0  (local to host NimoOS)
              UUID : 515b480b:4e70c57a:ec793de6:6c737ef7
            Events : 23

    Number   Major   Minor   RaidDevice State
       -       0        0        0      removed
       1       8       16        1      active sync   /dev/sdb
       3       8       32        2      active sync   /dev/sdc

       0       8        0        -      faulty   /dev/sda
`

// detailIdleSpare 覆盖闲置热备盘:它同样把 RaidDevice 写成 `-`,和 faulty 行
// 共用一条代码路径。格式取自 mdadm 手册与 detailDegradedFaulty 的实测行形状
// (Number 数字、RaidDevice 为 `-`、State 后接 /dev 路径)。
const detailIdleSpare = `/dev/md0:
        Raid Level : raid5
      Raid Devices : 3
     Total Devices : 4

             State : clean
    Active Devices : 3
   Working Devices : 4
    Failed Devices : 0
     Spare Devices : 1

              UUID : 515b480b:4e70c57a:ec793de6:6c737ef7

    Number   Major   Minor   RaidDevice State
       0       8        0        0      active sync   /dev/sda
       1       8       16        1      active sync   /dev/sdb
       3       8       32        2      active sync   /dev/sdc

       4       8       48        -      spare   /dev/sdd
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
	// 健康阵列每块盘都占槽位
	for i, m := range d.Members {
		if m.Slot != i {
			t.Errorf("Members[%d].Slot: got %d, want %d", i, m.Slot, i)
		}
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

// 故障盘(--fail 未 --remove)必须出现在 Members 里,且 State 为 "faulty"。
// 前端「更换硬盘」入口的判定条件正是 state === "faulty"(New-UI
// RaidMemberList.vue showReplace,逐字移植 Vue2 RaidTab.vue openReplaceDisk),
// 解析器丢掉这一行 = UI 上永远换不了故障盘。
func TestParseDetail_DegradedWithFaultyMember(t *testing.T) {
	d, err := ParseDetail(detailDegradedFaulty)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.State != "clean, degraded" {
		t.Errorf("State: got %q, want %q", d.State, "clean, degraded")
	}
	// 3 条:removed 空槽 + sdb + sdc,再加故障盘 sda = 4
	if len(d.Members) != 4 {
		t.Fatalf("Members count: got %d, want 4 (removed slot + sdb + sdc + faulty sda); members=%+v", len(d.Members), d.Members)
	}

	var faulty *MemberDisk
	for i := range d.Members {
		if d.Members[i].State == "faulty" {
			faulty = &d.Members[i]
		}
	}
	if faulty == nil {
		t.Fatalf("no member with State==\"faulty\"; members=%+v", d.Members)
	}
	if faulty.Path != "/dev/sda" {
		t.Errorf("faulty Path: got %q, want %q", faulty.Path, "/dev/sda")
	}
	if faulty.Number != 0 {
		t.Errorf("faulty Number: got %d, want 0", faulty.Number)
	}
	// Slot = 占哪个阵列槽位,-1 = 不占。被踢出槽位的 faulty 盘必须是 -1,否则调用方
	// 无法把"组成阵列的行"与"挂在阵列上但不占槽位的盘"分开 —— 3 盘 RAID 5 坏 1 块
	// 会被数成 4 块盘(卡片 4 个方块却写 2/3、详情页头写 MEMBER DISKS (4))。
	if faulty.Slot != -1 {
		t.Errorf("faulty Slot: got %d, want -1(RaidDevice 列是 -)", faulty.Slot)
	}

	// removed 空槽仍应保留(物理拔盘场景依赖它)
	var removed *MemberDisk
	for i := range d.Members {
		if d.Members[i].State == "removed" {
			removed = &d.Members[i]
		}
	}
	if removed == nil {
		t.Fatalf("no member with State==\"removed\"; members=%+v", d.Members)
	}
	if removed.Path != "" {
		t.Errorf("removed Path: got %q, want empty", removed.Path)
	}
	if removed.Slot != 0 {
		t.Errorf("removed Slot: got %d, want 0", removed.Slot)
	}

	// 占槽位的两块好盘:Slot 取 RaidDevice 列(1 和 2),不是 Number 列(1 和 3)。
	slots := map[string]int{}
	for _, m := range d.Members {
		if m.Path != "" && strings.HasPrefix(m.State, "active sync") {
			slots[m.Path] = m.Slot
		}
	}
	if slots["/dev/sdb"] != 1 {
		t.Errorf("sdb Slot: got %d, want 1", slots["/dev/sdb"])
	}
	if slots["/dev/sdc"] != 2 {
		t.Errorf("sdc Slot: got %d, want 2(RaidDevice 列;它的 Number 列是 3)", slots["/dev/sdc"])
	}

	// 占槽位的行数应等于阵列盘位数(3),而不是总行数(4)。
	occupied := 0
	for _, m := range d.Members {
		if m.Slot >= 0 {
			occupied++
		}
	}
	if occupied != 3 {
		t.Errorf("占槽位行数: got %d, want 3(总行数 %d)", occupied, len(d.Members))
	}
}

// 闲置热备盘的 RaidDevice 同为 `-`,走同一条代码路径,不应被丢弃。
func TestParseDetail_IdleSpare(t *testing.T) {
	d, err := ParseDetail(detailIdleSpare)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(d.Members) != 4 {
		t.Fatalf("Members count: got %d, want 4; members=%+v", len(d.Members), d.Members)
	}
	spare := d.Members[3]
	if spare.Path != "/dev/sdd" {
		t.Errorf("spare Path: got %q, want %q", spare.Path, "/dev/sdd")
	}
	if spare.State != "spare" {
		t.Errorf("spare State: got %q, want %q", spare.State, "spare")
	}
	if spare.Number != 4 {
		t.Errorf("spare Number: got %d, want 4", spare.Number)
	}
	if spare.Slot != -1 {
		t.Errorf("spare Slot: got %d, want -1(闲置热备不占槽位)", spare.Slot)
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

func TestParseMDStat_InactiveArray(t *testing.T) {
	out := `Personalities : [raid0] [raid1] [raid5]
md0 : active raid5 sdd[3] sdf[1] sda[0]
      1953260544 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/3] [UUU]

md1 : inactive sdg[1](S)
      976630488 blocks super 1.2

unused devices: <none>
`
	entries, err := ParseMDStat(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (inactive array must not vanish)", len(entries))
	}
	var md1 *MDStatEntry
	for i := range entries {
		if entries[i].Device == "md1" {
			md1 = &entries[i]
		}
	}
	if md1 == nil {
		t.Fatal("md1 missing from parse")
	}
	if md1.State != "inactive" {
		t.Fatalf("md1.State = %q, want inactive", md1.State)
	}
	if len(md1.Members) != 1 || md1.Members[0] != "sdg[1](S)" {
		t.Fatalf("md1.Members = %v, want [sdg[1](S)]", md1.Members)
	}
}
