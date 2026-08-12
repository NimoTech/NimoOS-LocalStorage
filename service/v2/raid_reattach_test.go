package v2

import (
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/pkg/mdadm"
)

func TestFindReattachableMembers(t *testing.T) {
	const uuid = "d94eb423:250d5f1f"
	// The 2026-08-12 incident: two members pulled from the running array and
	// plugged back (ports swapped) — detached, superblocks carry the array's
	// UUID, events behind. A foreign residue disk and the attached members
	// must not be picked up.
	examine := func(path string) (*mdadm.ExamineInfo, error) {
		switch path {
		case "/dev/sdb":
			return &mdadm.ExamineInfo{ArrayUUID: uuid, DeviceRole: "Active device 3", UpdateTime: "Wed Aug 12 03:44:37 2026"}, nil
		case "/dev/sdc":
			return &mdadm.ExamineInfo{ArrayUUID: uuid, DeviceRole: "Active device 1", UpdateTime: "Wed Aug 12 03:43:02 2026"}, nil
		case "/dev/sde":
			return &mdadm.ExamineInfo{ArrayUUID: "ffff:0000", Name: "zimaos:x"}, nil // foreign residue
		default:
			return nil, nil // clean disk / no superblock
		}
	}
	attached := map[string]bool{"/dev/sda": true, "/dev/sdd": true}
	candidates := []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/nvme0n1"}

	got := findReattachableMembers(uuid, attached, candidates, examine)
	if len(got) != 2 {
		t.Fatalf("got %d members, want 2: %+v", len(got), got)
	}
	if got[0].Path != "/dev/sdb" || got[0].Role != "Active device 3" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Path != "/dev/sdc" || got[1].LastUpdate == "" {
		t.Errorf("got[1] = %+v", got[1])
	}

	// Attached member listed as candidate must never be re-examined as detached.
	got = findReattachableMembers(uuid, map[string]bool{"/dev/sdb": true, "/dev/sdc": true, "/dev/sda": true, "/dev/sdd": true}, candidates, examine)
	if len(got) != 0 {
		t.Errorf("fully attached array: got %+v, want none", got)
	}
}
