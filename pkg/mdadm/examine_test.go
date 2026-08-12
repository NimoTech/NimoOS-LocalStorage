package mdadm

import "testing"

// Real output captured 2026-08-11 from a disk carrying a leftover superblock
// of a foreign (ZimaOS) array — the incident that motivated Examine.
const examineSample = `/dev/sdb:
          Magic : a92b4efc
        Version : 1.2
    Feature Map : 0x1
     Array UUID : 55d27042:876715c5:14fe3a89:95401f08
           Name : zimaos:fc5616382c017331
  Creation Time : Thu Aug  6 21:54:49 2026
     Raid Level : raid5
   Raid Devices : 4

 Avail Dev Size : 1953260976 sectors (931.39 GiB 1000.07 GB)
     Array Size : 2929890816 KiB (2.73 TiB 3.00 TB)
  Used Dev Size : 1953260544 sectors (931.39 GiB 1000.07 GB)
    Data Offset : 264192 sectors
   Super Offset : 8 sectors
   Unused Space : before=264112 sectors, after=432 sectors
          State : clean
    Device UUID : 67b264d9:ac54c042:1e05033e:98fac794

Internal Bitmap : 8 sectors from superblock
    Update Time : Fri Aug  7 00:29:17 2026
  Bad Block Log : 512 entries available at offset 16 sectors
       Checksum : 5142ee43 - correct
         Events : 1648

         Layout : left-symmetric
     Chunk Size : 512K

   Device Role : Active device 0
   Array State : AAAA ('A' == active, '.' == missing, 'R' == replacing)
`

func TestParseExamine(t *testing.T) {
	info, err := ParseExamine(examineSample)
	if err != nil {
		t.Fatalf("ParseExamine: %v", err)
	}
	cases := []struct{ name, got, want string }{
		{"ArrayUUID", info.ArrayUUID, "55d27042:876715c5:14fe3a89:95401f08"},
		{"Name", info.Name, "zimaos:fc5616382c017331"},
		{"Level", info.Level, "raid5"},
		{"CreationTime", info.CreationTime, "Thu Aug  6 21:54:49 2026"},
		{"UpdateTime", info.UpdateTime, "Fri Aug  7 00:29:17 2026"},
		{"DeviceUUID", info.DeviceUUID, "67b264d9:ac54c042:1e05033e:98fac794"},
		{"DeviceRole", info.DeviceRole, "Active device 0"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestParseExamineNoSuperblock(t *testing.T) {
	if _, err := ParseExamine("mdadm: No md superblock detected on /dev/sdb.\n"); err == nil {
		t.Error("expected error for output without Array UUID")
	}
	if _, err := ParseExamine(""); err == nil {
		t.Error("expected error for empty output")
	}
}
