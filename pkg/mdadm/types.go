package mdadm

// ArrayDetail holds parsed output from `mdadm --detail /dev/mdX`
type ArrayDetail struct {
	Device      string       // e.g. /dev/md0
	Name        string       // array name from mdadm, format "hostname:arrayname"
	UUID        string       // array UUID
	Level       string       // raid0, raid1, raid5, raid6
	State       string       // clean, degraded, recovering, inactive
	ActiveDisks int          // number of active disks
	TotalDisks  int          // total configured disk slots
	RebuildPct  float64      // rebuild progress 0-100, -1 if not rebuilding
	Members     []MemberDisk // member disk list
}

// MemberDisk represents a single disk in a RAID array
type MemberDisk struct {
	Path  string // e.g. /dev/sda
	State string // active sync, faulty, spare, rebuilding
	// Number is mdadm's "Number" column. Overloaded for historical reasons: on a
	// `removed` placeholder row that column is `-`, and Number carries the array
	// slot instead. Prefer Slot for "which slot is this".
	Number int
	// Slot is mdadm's "RaidDevice" column — which array slot this entry occupies,
	// or -1 when it occupies none (`-` in that column: a `faulty` disk that has
	// been ejected from its slot, or an idle `spare`).
	//
	// Consumers need this to tell "rows that make up the array" from "disks
	// attached to the array but holding no slot". Without it a degraded 3-disk
	// RAID 5 looks like 4 disks: the vacated slot plus the ejected faulty disk
	// are two separate rows (2026-07-30, 实盘验收).
	Slot int
}

// MDStatEntry holds parsed info from one array in /proc/mdstat
type MDStatEntry struct {
	Device        string   // e.g. md0 (no /dev/ prefix)
	State         string   // active, inactive
	Level         string   // raid0, raid1, raid5, raid6
	Members       []string // member device names e.g. ["sda[0]", "sdb[1]"]
	DiskStatus    string   // e.g. "[UUU_]" — U=up, _=down
	RebuildPct    float64  // -1 if not rebuilding
	RebuildFinish string   // estimated time remaining, e.g. "95.3min"
	RebuildSpeed  string   // rebuild speed, e.g. "88888K/sec"
	// RebuildPos/RebuildTotal are the "(N/M)" pair of the progress line.
	// The kernel's finish/speed only count *copied* blocks, so during a
	// bitmap delta resync (which skips clean chunks) they are wildly wrong —
	// the position advance rate is the only honest ETA basis. 0 when absent.
	RebuildPos   int64
	RebuildTotal int64
}
