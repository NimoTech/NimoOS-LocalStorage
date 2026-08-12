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

func TestFindOldDiskMatches(t *testing.T) {
	attached := attachedMembers(degradedMembers())
	serials := map[string]string{}
	for _, m := range attached {
		serials[m.Path] = fakeSerialOf(m.Path)
	}

	cases := []struct {
		name              string
		oldSerial, oldPath string
		wantPaths         []string
	}{
		// The incident: pulled disk's stale path now belongs to the new disk.
		// Serial OLD-4 is attached nowhere → must report "gone", NOT /dev/sdb.
		{"pulled disk, stale path reused", "OLD-4", "/dev/sdb", nil},
		// Faulty-but-present disk found by serial.
		{"present disk by serial", "OLD-2", "", []string{"/dev/sdc"}},
		// Serial wins over a contradictory path.
		{"serial wins over path", "OLD-2", "/dev/sda", []string{"/dev/sdc"}},
		// Legacy client, path only: trusted only when attached.
		{"legacy path attached", "", "/dev/sda", []string{"/dev/sda"}},
		{"legacy path not attached", "", "/dev/sdb", nil},
	}
	for _, c := range cases {
		got := findOldDiskMatches(attached, serials, c.oldSerial, c.oldPath)
		var gotPaths []string
		for _, m := range got {
			gotPaths = append(gotPaths, m.Path)
		}
		if len(gotPaths) != len(c.wantPaths) {
			t.Errorf("%s: got %v, want %v", c.name, gotPaths, c.wantPaths)
			continue
		}
		for i := range gotPaths {
			if gotPaths[i] != c.wantPaths[i] {
				t.Errorf("%s: got %v, want %v", c.name, gotPaths, c.wantPaths)
			}
		}
	}
}

func TestFindOldDiskMatchesDuplicateSerials(t *testing.T) {
	// Cheap USB bridges report one fake serial for every disk: the pulled
	// disk's serial also belongs to a healthy attached twin. All matches must
	// surface so the caller can refuse to guess.
	attached := []mdadm.MemberDisk{
		{Path: "/dev/sdd", State: "active sync", Slot: 0},
		{Path: "/dev/sdc", State: "active sync", Slot: 1},
	}
	serials := map[string]string{"/dev/sdd": "DUP", "/dev/sdc": "DUP"}
	if got := findOldDiskMatches(attached, serials, "DUP", ""); len(got) != 2 {
		t.Errorf("expected both duplicate-serial members, got %v", got)
	}
}

func TestMapMdadmState(t *testing.T) {
	cases := []struct{ in, want string }{
		{"clean", "active"},
		{"active", "active"},
		{"clean, degraded", "degraded"},
		{"clean, degraded, recovering", "rebuilding"},
		{"active, resyncing", "rebuilding"},
		// A dead array must never read as active or merely degraded.
		{"clean, FAILED", "failed"},
		{"clean, degraded, FAILED", "failed"},
		{"broken", "failed"},
		{"inactive", "failed"},
	}
	for _, c := range cases {
		if got := mapMdadmState(c.in); got != c.want {
			t.Errorf("mapMdadmState(%q) = %q, want %q", c.in, got, c.want)
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

func TestRaidTraceGuard(t *testing.T) {
	// Clean disk: proceed with or without the flag.
	if err := raidTraceGuard("/dev/sdb", nil, false); err != nil {
		t.Errorf("clean disk: %v", err)
	}
	// This system's array member: refused even with the wipe flag.
	prot := &diskRaidTrace{Protected: true, ArrayName: "raid10"}
	if err := raidTraceGuard("/dev/sdb", prot, true); err == nil {
		t.Error("protected member must be refused even with wipe flag")
	}
	// Foreign residue: refused without the flag, allowed with it.
	res := &diskRaidTrace{ArrayName: "zimaos:fc56", LastActive: "Fri Aug  7 00:29:17 2026"}
	if err := raidTraceGuard("/dev/sdb", res, false); err == nil {
		t.Error("residue without confirmation must be refused")
	}
	if err := raidTraceGuard("/dev/sdb", res, true); err != nil {
		t.Errorf("confirmed residue wipe should proceed: %v", err)
	}
}
