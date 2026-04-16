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
	Path   string // e.g. /dev/sda
	State  string // active sync, faulty, spare, rebuilding
	Number int    // raid disk number
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
}
