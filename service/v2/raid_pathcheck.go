package v2

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// pathConfigFile is written by NimoOS to record the active locations for the
// three system data categories (Docker images, application data, user
// database). LocalStorage reads it before deleting a RAID so it can refuse
// the request when the RAID still hosts one of those locations.
const pathConfigFile = "/var/lib/nimoos/path_config.json"

type pathConfigSnapshot struct {
	AppData  string `json:"app_data"`
	Images   string `json:"images"`
	Database string `json:"database"`
}

// systemPathOnRAID is one entry of the "this RAID is in use" preflight
// result, used to build a user-readable error message.
type systemPathOnRAID struct {
	Kind string
	Path string
}

// detectSystemPathsOnRAID reports which system data locations resolve to
// (a subdirectory of) mountPoint. Returns nil when the config file is missing
// or unparseable so a broken config can never block deletion.
func detectSystemPathsOnRAID(mountPoint string) []systemPathOnRAID {
	data, err := os.ReadFile(pathConfigFile)
	if err != nil {
		return nil
	}
	var cfg pathConfigSnapshot
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}

	var out []systemPathOnRAID
	add := func(kind, path string) {
		if pathIsUnder(path, mountPoint) {
			out = append(out, systemPathOnRAID{Kind: kind, Path: path})
		}
	}
	add("Docker images & containers", cfg.Images)
	add("Application data", cfg.AppData)
	add("User database", cfg.Database)
	return out
}

// pathIsUnder reports whether p equals mountPoint or sits under it.
func pathIsUnder(p, mountPoint string) bool {
	if p == "" || mountPoint == "" {
		return false
	}
	p = strings.TrimRight(p, "/")
	mp := strings.TrimRight(mountPoint, "/")
	return p == mp || strings.HasPrefix(p, mp+"/")
}

// formatSystemPathConflict renders the error message returned when a delete
// is blocked because the RAID still hosts a system data location.
func formatSystemPathConflict(mountPoint string, items []systemPathOnRAID) string {
	var b strings.Builder
	b.WriteString("Cannot delete this RAID: it is currently used as a system data location.\n\n")
	fmt.Fprintf(&b, "The following data is stored on %s:\n", mountPoint)
	for _, it := range items {
		fmt.Fprintf(&b, "  • %s → %s\n", it.Kind, it.Path)
	}
	b.WriteString("\nPlease move this data to another location first via System Settings → Storage Paths, then delete the RAID.")
	return b.String()
}

// formatBusyErrorForSubmount renders the error returned when the RAID
// itself is unmountable because a child submount cannot be released.
// The message names the specific submount and what is keeping it busy, so
// the user can act on the actual blocker rather than the parent path.
func formatBusyErrorForSubmount(parent, child, umountStderr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cannot delete this RAID: a submount under %s is still busy.\n\n", parent)
	fmt.Fprintf(&b, "Submount that could not be unmounted:\n  • %s\n", child)
	if msg := strings.TrimSpace(umountStderr); msg != "" {
		fmt.Fprintf(&b, "(umount: %s)\n", msg)
	}

	entries := collectBusyEntries(child)
	if len(entries) == 0 {
		b.WriteString("\nNo process appears to hold files open inside this submount, but the kernel still reports it busy.\n")
		b.WriteString("This can happen with stale Docker/containerd overlays. Try restarting the docker and containerd services, then delete the RAID again.")
		return b.String()
	}

	type group struct {
		Command string
		PID     string
		Files   []string
	}
	order := []string{}
	groups := map[string]*group{}
	for _, e := range entries {
		key := e.Command + "|" + e.PID
		g, ok := groups[key]
		if !ok {
			g = &group{Command: e.Command, PID: e.PID}
			groups[key] = g
			order = append(order, key)
		}
		g.Files = append(g.Files, e.Path)
	}
	fmt.Fprintf(&b, "\n%d process%s holding files open inside this submount:\n", len(order), pluralEs(len(order)))
	for _, k := range order {
		g := groups[k]
		fmt.Fprintf(&b, "  • %s (PID %s) — %d file%s open\n", g.Command, g.PID, len(g.Files), pluralS(len(g.Files)))
		shown := g.Files
		hidden := 0
		if len(shown) > 5 {
			hidden = len(shown) - 5
			shown = shown[:5]
		}
		for _, f := range shown {
			fmt.Fprintf(&b, "      - %s\n", f)
		}
		if hidden > 0 {
			fmt.Fprintf(&b, "      … and %d more file%s\n", hidden, pluralS(hidden))
		}
	}
	b.WriteString("\nStop these processes (or unmount this submount manually), then try deleting the RAID again.")
	return b.String()
}

// busyEntry is one process holding a file open under a busy mount.
type busyEntry struct {
	Command string
	PID     string
	Path    string
}

// formatBusyError renders a human-readable error for an unmount-busy failure.
// It enumerates child mounts and processes that hold files open, grouping by
// (command, pid) so a long lsof dump becomes a short readable list.
func formatBusyError(target, umountStderr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Failed to unmount %s: target is busy.\n", target)
	if msg := strings.TrimSpace(umountStderr); msg != "" && !strings.Contains(msg, "target is busy") {
		fmt.Fprintf(&b, "(umount: %s)\n", msg)
	}

	children := collectChildMounts(target)
	entries := collectBusyEntries(target)

	if len(children) == 0 && len(entries) == 0 {
		b.WriteString("\nUnable to identify which process is holding the mount open.\n")
		b.WriteString("Try stopping any service that may be using files under the mount and retry.")
		return b.String()
	}

	if len(children) > 0 {
		fmt.Fprintf(&b, "\n%d submount%s under this mount must be unmounted first:\n", len(children), pluralS(len(children)))
		shown := children
		hidden := 0
		if len(shown) > 8 {
			hidden = len(shown) - 8
			shown = shown[:8]
		}
		for _, c := range shown {
			fmt.Fprintf(&b, "  • %s\n", c)
		}
		if hidden > 0 {
			fmt.Fprintf(&b, "  … and %d more submount%s\n", hidden, pluralS(hidden))
		}
	}

	if len(entries) > 0 {
		type group struct {
			Command string
			PID     string
			Files   []string
		}
		order := []string{}
		groups := map[string]*group{}
		for _, e := range entries {
			key := e.Command + "|" + e.PID
			g, ok := groups[key]
			if !ok {
				g = &group{Command: e.Command, PID: e.PID}
				groups[key] = g
				order = append(order, key)
			}
			g.Files = append(g.Files, e.Path)
		}

		fmt.Fprintf(&b, "\n%d process%s holding files open on this mount:\n", len(order), pluralEs(len(order)))
		for _, k := range order {
			g := groups[k]
			fmt.Fprintf(&b, "  • %s (PID %s) — %d file%s open\n", g.Command, g.PID, len(g.Files), pluralS(len(g.Files)))
			shown := g.Files
			hidden := 0
			if len(shown) > 5 {
				hidden = len(shown) - 5
				shown = shown[:5]
			}
			for _, f := range shown {
				fmt.Fprintf(&b, "      - %s\n", f)
			}
			if hidden > 0 {
				fmt.Fprintf(&b, "      … and %d more file%s\n", hidden, pluralS(hidden))
			}
		}
	}

	b.WriteString("\nStop these processes (or unmount the submounts), then try deleting the RAID again.")
	return b.String()
}

// collectChildMounts returns mount points strictly under target (excluding
// target itself) by scanning /proc/mounts.
func collectChildMounts(target string) []string {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	prefix := strings.TrimRight(target, "/") + "/"
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mp := fields[1]
		if strings.HasPrefix(mp, prefix) {
			out = append(out, mp)
		}
	}
	return out
}

// collectBusyEntries lists processes that hold files open under target. It
// uses lsof's -F (machine-readable) format so we don't have to deal with the
// 9-character COMMAND truncation in the default output, and resolves each
// PID via /proc/<pid>/comm to get the real command name.
func collectBusyEntries(target string) []busyEntry {
	stat, err := os.Stat(target)
	if err != nil {
		return nil
	}
	arg := target
	if stat.IsDir() {
		arg = "+D " + target
	}

	out, err := exec.Command("sh", "-c", "lsof -F pcn "+arg+" 2>/dev/null").CombinedOutput()
	if err != nil || len(out) == 0 {
		return nil
	}

	var entries []busyEntry
	var curPID, curCmd string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			curPID = line[1:]
			curCmd = ""
			if name, err := os.ReadFile("/proc/" + curPID + "/comm"); err == nil {
				curCmd = strings.TrimSpace(string(name))
			}
		case 'c':
			if curCmd == "" {
				curCmd = line[1:]
			}
		case 'n':
			path := line[1:]
			if path == "" {
				continue
			}
			entries = append(entries, busyEntry{Command: curCmd, PID: curPID, Path: path})
		}
	}
	return entries
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralEs(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
