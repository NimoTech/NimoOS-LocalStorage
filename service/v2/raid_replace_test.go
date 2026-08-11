package v2

import (
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// Serial lookup fixture: /dev/sdb belongs to the brand-new disk NEW-1 — the
// device letter freed by the pulled disk OLD-4 got reused (2026-08-11 incident).
func fakeSerialOf(path string) string {
	return map[string]string{
		"/dev/sda": "OLD-3",
		"/dev/sdb": "NEW-1",
		"/dev/sdc": "OLD-2",
		"/dev/sdd": "OLD-1",
	}[path]
}

func degradedMembers() []mdadm.MemberDisk {
	return []mdadm.MemberDisk{
		{Path: "/dev/sdd", State: "active sync", Slot: 0},
		{Path: "/dev/sdc", State: "active sync", Slot: 1},
		{Path: "/dev/sda", State: "active sync", Slot: 2},
		{Path: "", State: "removed", Slot: 3},
	}
}

func TestAttachedMembersDropsRemovedPlaceholders(t *testing.T) {
	got := attachedMembers(degradedMembers())
	if len(got) != 3 {
		t.Fatalf("attachedMembers: got %d members, want 3", len(got))
	}
	for _, m := range got {
		if m.Path == "" {
			t.Errorf("attachedMembers kept a pathless row: %+v", m)
		}
	}
}

func TestFindOldDiskLivePath(t *testing.T) {
	attached := attachedMembers(degradedMembers())
	serials := map[string]string{}
	for _, m := range attached {
		serials[m.Path] = fakeSerialOf(m.Path)
	}

	cases := []struct {
		name              string
		oldSerial, oldPath string
		want              string
	}{
		// The incident: pulled disk's stale path now belongs to the new disk.
		// Serial OLD-4 is attached nowhere → must report "gone", NOT /dev/sdb.
		{"pulled disk, stale path reused", "OLD-4", "/dev/sdb", ""},
		// Faulty-but-present disk found by serial.
		{"present disk by serial", "OLD-2", "", "/dev/sdc"},
		// Serial wins over a contradictory path.
		{"serial wins over path", "OLD-2", "/dev/sda", "/dev/sdc"},
		// Legacy client, path only: trusted only when attached.
		{"legacy path attached", "", "/dev/sda", "/dev/sda"},
		{"legacy path not attached", "", "/dev/sdb", ""},
	}
	for _, c := range cases {
		if got := findOldDiskLivePath(attached, serials, c.oldSerial, c.oldPath); got != c.want {
			t.Errorf("%s: findOldDiskLivePath(serial=%q, path=%q) = %q, want %q",
				c.name, c.oldSerial, c.oldPath, got, c.want)
		}
	}
}

func TestMemberRowToReplace(t *testing.T) {
	members := []*model.RAIDMember{
		{ID: 1, DiskSerial: "OLD-1", DevicePathCache: "/dev/sdd"},
		{ID: 2, DiskSerial: "OLD-2", DevicePathCache: "/dev/sdc"},
		{ID: 3, DiskSerial: "OLD-3", DevicePathCache: "/dev/sda"},
		{ID: 4, DiskSerial: "OLD-4", DevicePathCache: "/dev/sdb"},
	}

	if got := memberRowToReplace(members, "OLD-4", ""); got == nil || got.ID != 4 {
		t.Errorf("by serial: got %+v, want row 4", got)
	}
	// Serial wins even when the stale path points at another row.
	if got := memberRowToReplace(members, "OLD-1", "/dev/sdb"); got == nil || got.ID != 1 {
		t.Errorf("serial over path: got %+v, want row 1", got)
	}
	// Legacy client: path-only fallback.
	if got := memberRowToReplace(members, "", "/dev/sdb"); got == nil || got.ID != 4 {
		t.Errorf("by path: got %+v, want row 4", got)
	}
	if got := memberRowToReplace(members, "GONE", "/dev/nope"); got != nil {
		t.Errorf("no match: got %+v, want nil", got)
	}
}
