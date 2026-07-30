package v2

import (
	"errors"
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/fstab"
)

// cleanupRAIDPersistence reports every failure through the logger, which panics
// on a nil global (same convention as service/snapshot/mount_test.go).
func init() {
	logger.LogInitConsoleOnly()
}

// swapCleanupSeams replaces the fstab/mdadm seams used by
// cleanupRAIDPersistence and restores them when the test ends.
func swapCleanupSeams(t *testing.T,
	get func(string) (*fstab.Entry, error),
	remove func(string) error,
	save func() error,
) {
	t.Helper()
	origGet, origRemove, origSave := fstabEntryByMountPoint, fstabRemoveByMountPoint, mdadmSaveConfig
	fstabEntryByMountPoint, fstabRemoveByMountPoint, mdadmSaveConfig = get, remove, save
	t.Cleanup(func() {
		fstabEntryByMountPoint, fstabRemoveByMountPoint, mdadmSaveConfig = origGet, origRemove, origSave
	})
}

// The regression this whole file exists for: deleting an array used to leave
// its @snapshots line in /etc/fstab pointing at a device that no longer
// exists, and its ARRAY line in /etc/mdadm/mdadm.conf naming a stale UUID.
func TestCleanupRAIDPersistenceRemovesSnapshotsFstabEntryAndRewritesMdadmConf(t *testing.T) {
	var removed []string
	var asked []string
	saved := 0
	swapCleanupSeams(t,
		func(mp string) (*fstab.Entry, error) {
			asked = append(asked, mp)
			return &fstab.Entry{MountPoint: mp, Source: "/dev/md0", FSType: "btrfs"}, nil
		},
		func(mp string) error { removed = append(removed, mp); return nil },
		func() error { saved++; return nil },
	)

	cleanupRAIDPersistence("/media/RAID_HDD_md0")

	wantMP := "/media/RAID_HDD_md0/.snapshots"
	if len(asked) != 1 || asked[0] != wantMP {
		t.Errorf("looked up fstab entries %v, want exactly [%q]", asked, wantMP)
	}
	if len(removed) != 1 || removed[0] != wantMP {
		t.Errorf("removed fstab entries %v, want exactly [%q]", removed, wantMP)
	}
	if saved != 1 {
		t.Errorf("mdadm SaveConfig called %d times, want 1", saved)
	}
}

// An ext4 array (or a btrfs one whose @snapshots mount never succeeded) has no
// fstab entry. Removing it anyway would rewrite /etc/fstab — and refresh
// /etc/fstab.nimoos.bak from it — for nothing.
func TestCleanupRAIDPersistenceSkipsFstabRewriteWhenNoEntryExists(t *testing.T) {
	removeCalled := false
	saved := 0
	swapCleanupSeams(t,
		func(string) (*fstab.Entry, error) { return nil, nil },
		func(string) error { removeCalled = true; return nil },
		func() error { saved++; return nil },
	)

	cleanupRAIDPersistence("/media/RAID_SSD_md1")

	if removeCalled {
		t.Error("rewrote /etc/fstab even though the array had no entry in it")
	}
	if saved != 1 {
		t.Errorf("mdadm SaveConfig called %d times, want 1 (mdadm.conf cleanup is independent of fstab)", saved)
	}
}

// The two files are cleaned independently: an fstab problem must not cost the
// caller its mdadm.conf cleanup, or one stale line silently becomes two.
func TestCleanupRAIDPersistenceCleansMdadmConfEvenWhenFstabFails(t *testing.T) {
	for _, tc := range []struct {
		name             string
		get              func(string) (*fstab.Entry, error)
		remove           func(string) error
		wantRemoveCalled bool
	}{
		{
			name:             "lookup fails",
			get:              func(string) (*fstab.Entry, error) { return nil, errors.New("read /etc/fstab: permission denied") },
			remove:           func(string) error { return nil },
			wantRemoveCalled: false,
		},
		{
			name:             "removal fails",
			get:              func(mp string) (*fstab.Entry, error) { return &fstab.Entry{MountPoint: mp}, nil },
			remove:           func(string) error { return errors.New("rename /etc/fstab: read-only file system") },
			wantRemoveCalled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			removeCalled := false
			saved := 0
			swapCleanupSeams(t,
				tc.get,
				func(mp string) error { removeCalled = true; return tc.remove(mp) },
				func() error { saved++; return nil },
			)

			cleanupRAIDPersistence("/media/RAID_HDD_md0")

			if removeCalled != tc.wantRemoveCalled {
				t.Errorf("fstab removal called = %v, want %v", removeCalled, tc.wantRemoveCalled)
			}
			if saved != 1 {
				t.Errorf("mdadm SaveConfig called %d times, want 1 despite the fstab failure", saved)
			}
		})
	}
}

// Cleanup runs after the array is already stopped and out of the DB, so there
// is nothing left to retry: a failure here can only be logged, never returned.
func TestCleanupRAIDPersistenceSurvivesMdadmSaveConfigFailure(t *testing.T) {
	swapCleanupSeams(t,
		func(string) (*fstab.Entry, error) { return nil, nil },
		func(string) error { return nil },
		func() error { return errors.New("mdadm: cannot open /etc/mdadm/mdadm.conf") },
	)

	cleanupRAIDPersistence("/media/RAID_HDD_md0") // must not panic
}

// A DB row with an empty mount point would make filepath.Join yield
// "/.snapshots" — a path belonging to the root filesystem, not this array.
func TestCleanupRAIDPersistenceIgnoresEmptyMountPoint(t *testing.T) {
	touchedFstab := false
	saved := 0
	swapCleanupSeams(t,
		func(string) (*fstab.Entry, error) { touchedFstab = true; return nil, nil },
		func(string) error { touchedFstab = true; return nil },
		func() error { saved++; return nil },
	)

	cleanupRAIDPersistence("")

	if touchedFstab {
		t.Error("consulted /etc/fstab for an array with no mount point; /.snapshots is not this array's entry")
	}
	if saved != 1 {
		t.Errorf("mdadm SaveConfig called %d times, want 1", saved)
	}
}
