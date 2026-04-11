package mdadm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseDetail parses the output of `mdadm --detail /dev/mdX` and returns an ArrayDetail.
// Returns an error if the output is empty or the device header cannot be found.
func ParseDetail(output string) (*ArrayDetail, error) {
	if strings.TrimSpace(output) == "" {
		return nil, fmt.Errorf("mdadm --detail output is empty")
	}

	detail := &ArrayDetail{
		RebuildPct: -1,
	}

	// Regex for member disk lines:
	// "   number  major  minor  raiddevice  state...  /dev/sdX"
	// Fields: Number Major Minor RaidDevice State... /dev/sdX
	memberRe := regexp.MustCompile(`^\s+(\d+)\s+\d+\s+\d+\s+\d+\s+(.+?)\s+(/dev/\S+)\s*$`)

	// Regex for removed member lines (physically pulled disk):
	// "   -   0   0   N   removed"
	removedRe := regexp.MustCompile(`^\s+-\s+\d+\s+\d+\s+(\d+)\s+(removed)\s*$`)

	// Rebuild status line: "     Rebuild Status : 45% complete"
	rebuildRe := regexp.MustCompile(`(?i)rebuild\s+status\s*:\s*([\d.]+)%`)

	inTable := false

	for _, line := range strings.Split(output, "\n") {
		// Device header: "/dev/md0:"
		if !inTable && strings.HasSuffix(strings.TrimSpace(line), ":") && strings.HasPrefix(strings.TrimSpace(line), "/dev/md") {
			detail.Device = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}

		// Key-value lines
		if idx := strings.Index(line, " : "); idx >= 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+3:])

			switch key {
			case "Raid Level":
				detail.Level = value
			case "State":
				detail.State = value
			case "Name":
				detail.Name = value
			case "UUID":
				detail.UUID = value
			case "Active Devices":
				n, _ := strconv.Atoi(value)
				detail.ActiveDisks = n
			case "Raid Devices":
				n, _ := strconv.Atoi(value)
				detail.TotalDisks = n
			}

			// Rebuild status (key may be "Rebuild Status")
			if m := rebuildRe.FindStringSubmatch(line); m != nil {
				pct, _ := strconv.ParseFloat(m[1], 64)
				detail.RebuildPct = pct
			}
			continue
		}

		// Detect member table header
		if strings.Contains(line, "Number") && strings.Contains(line, "RaidDevice") && strings.Contains(line, "State") {
			inTable = true
			continue
		}

		// Member disk lines
		if inTable {
			if m := memberRe.FindStringSubmatch(line); m != nil {
				number, _ := strconv.Atoi(strings.TrimSpace(m[1]))
				state := strings.TrimSpace(m[2])
				path := m[3]
				detail.Members = append(detail.Members, MemberDisk{
					Path:   path,
					State:  state,
					Number: number,
				})
			} else if m := removedRe.FindStringSubmatch(line); m != nil {
				// Physically removed disk: slot exists but device is gone
				number, _ := strconv.Atoi(strings.TrimSpace(m[1]))
				detail.Members = append(detail.Members, MemberDisk{
					Path:   "",
					State:  "removed",
					Number: number,
				})
			}
		}
	}

	if detail.Device == "" {
		return nil, fmt.Errorf("could not parse device from mdadm --detail output")
	}

	return detail, nil
}

// ParseMDStat parses the contents of /proc/mdstat and returns a slice of MDStatEntry.
// Returns an empty slice (no error) for empty input.
func ParseMDStat(output string) ([]MDStatEntry, error) {
	var entries []MDStatEntry

	if strings.TrimSpace(output) == "" {
		return entries, nil
	}

	// Device line pattern: "md0 : active raid5 sdc[2] sdb[1] sda[0]"
	// Also handles states like "active (auto-read-only) raid1 ..."
	deviceRe := regexp.MustCompile(`^(md\d+)\s*:\s*(\w+(?:\s*\([^)]*\))?)\s+(\w+)\s+(.+)$`)
	// Disk status bracket pattern: "[UUU]" or "[UU_]"
	diskStatusRe := regexp.MustCompile(`\[([U_]+)\]`)
	// Rebuild/recovery percentage: "recovery = 45.2%"
	rebuildRe := regexp.MustCompile(`(?:recovery|resync)\s*=\s*([\d.]+)%`)

	lines := strings.Split(output, "\n")
	var current *MDStatEntry

	for i, line := range lines {
		if m := deviceRe.FindStringSubmatch(line); m != nil {
			// Save previous entry if exists
			if current != nil {
				entries = append(entries, *current)
			}
			// Parse members: "sdc[2] sdb[1] sda[0]"
			memberParts := strings.Fields(m[4])
			current = &MDStatEntry{
				Device:     m[1],
				State:      m[2],
				Level:      m[3],
				Members:    memberParts,
				RebuildPct: -1,
			}
			_ = i
			continue
		}

		if current == nil {
			continue
		}

		// Look for disk status and rebuild pct in subsequent lines
		if ds := diskStatusRe.FindString(line); ds != "" {
			current.DiskStatus = ds
		}
		if rb := rebuildRe.FindStringSubmatch(line); rb != nil {
			pct, _ := strconv.ParseFloat(rb[1], 64)
			current.RebuildPct = pct
		}
	}

	// Save last entry
	if current != nil {
		entries = append(entries, *current)
	}

	return entries, nil
}
