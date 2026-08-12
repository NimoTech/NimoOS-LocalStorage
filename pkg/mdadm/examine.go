package mdadm

import (
	"fmt"
	"os/exec"
	"strings"
)

// ExamineInfo holds the identity a member disk's md superblock carries —
// everything needed to tell the user whose array this disk belonged to.
type ExamineInfo struct {
	ArrayUUID    string // e.g. 55d27042:876715c5:14fe3a89:95401f08
	Name         string // "hostname:arrayname", e.g. "zimaos:fc5616382c017331"
	Level        string // raid0/raid1/raid5/raid6/raid10
	CreationTime string // as printed by mdadm, e.g. "Thu Aug  6 21:54:49 2026"
	UpdateTime   string // last superblock update — when the array was last alive
	DeviceUUID   string // this member's own identity within the array
	DeviceRole   string // e.g. "Active device 0", "spare"
}

// Examine reads the md superblock of a member device via `mdadm --examine`.
// Returns (nil, nil) when the device carries no md superblock — that is a
// normal answer, not an error.
func Examine(device string) (*ExamineInfo, error) {
	out, err := exec.Command(MdadmPath, "--examine", device).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No md superblock") {
			return nil, nil
		}
		return nil, fmt.Errorf("mdadm examine %s: %w: %s", device, err, string(out))
	}
	info, perr := ParseExamine(string(out))
	if perr != nil {
		return nil, fmt.Errorf("parse mdadm examine %s: %w", device, perr)
	}
	return info, nil
}

// ParseExamine parses `mdadm --examine` output. Key-value lines share the
// " : " separator with `mdadm --detail`, so the same splitting applies.
func ParseExamine(output string) (*ExamineInfo, error) {
	info := &ExamineInfo{}
	for _, line := range strings.Split(output, "\n") {
		idx := strings.Index(line, " : ")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+3:])
		switch key {
		case "Array UUID":
			info.ArrayUUID = value
		case "Name":
			info.Name = value
		case "Raid Level":
			info.Level = value
		case "Creation Time":
			info.CreationTime = value
		case "Update Time":
			info.UpdateTime = value
		case "Device UUID":
			info.DeviceUUID = value
		case "Device Role":
			info.DeviceRole = value
		}
	}
	if info.ArrayUUID == "" {
		return nil, fmt.Errorf("no Array UUID in mdadm --examine output")
	}
	return info, nil
}
