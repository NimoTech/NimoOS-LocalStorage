package snapshot

import (
	"testing"

	v2 "github.com/NimoTech/NimoOS-LocalStorage/service/v2"
)

// service/v2 has to duplicate SnapshotsMountSubdir: it is where RAID deletion
// removes the /etc/fstab line EnsureSnapshotsMount wrote, but this package
// already imports service/v2 (usage_raid.go), so it cannot be imported back.
//
// This test is the guard on that duplication. If it fails, RAID deletion is
// looking for the wrong mount point and silently leaves the fstab line behind
// — the exact regression service/v2/raid_cleanup.go exists to prevent.
func TestSnapshotsMountSubdirMatchesRAIDCleanupCopy(t *testing.T) {
	if v2.SnapshotsMountSubdir != SnapshotsMountSubdir {
		t.Fatalf("service/v2.SnapshotsMountSubdir = %q, snapshot.SnapshotsMountSubdir = %q; "+
			"RAID delete would look for the wrong fstab mount point",
			v2.SnapshotsMountSubdir, SnapshotsMountSubdir)
	}
}
