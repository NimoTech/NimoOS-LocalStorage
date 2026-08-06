package mdadm

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
)

// SaveConfig logs, and the logger panics on a nil global (same convention as
// service/snapshot/mount_test.go).
func init() {
	logger.LogInitConsoleOnly()
}

// fakeMdadm installs a stub `mdadm` for the duration of the test: a shell
// script that copies stdout to its stdout, stderrText to its stderr, and exits
// with exitCode. The payloads go through files so no shell quoting is involved.
func fakeMdadm(t *testing.T, stdout, stderrText string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "stdout")
	errFile := filepath.Join(dir, "stderr")
	for path, content := range map[string]string{outFile: stdout, errFile: stderrText} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write stub payload %s: %v", path, err)
		}
	}

	script := filepath.Join(dir, "mdadm-stub")
	body := "#!/bin/sh\ncat " + outFile + "\ncat " + errFile + " >&2\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mdadm stub: %v", err)
	}

	orig := MdadmPath
	MdadmPath = script
	t.Cleanup(func() { MdadmPath = orig })
}

// useTempConfigPath points ConfigPath at a temp file, optionally pre-seeded.
func useTempConfigPath(t *testing.T, existing string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mdadm.conf")
	if existing != "" {
		if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
			t.Fatalf("seed config: %v", err)
		}
	}
	orig := ConfigPath
	ConfigPath = path
	t.Cleanup(func() { ConfigPath = orig })
	return path
}

func TestSaveConfigWritesScanOutput(t *testing.T) {
	scan := "ARRAY /dev/md0 metadata=1.2 UUID=aaaa:bbbb:cccc:dddd\n"
	path := useTempConfigPath(t, "ARRAY /dev/md9 UUID=stale\n")
	fakeMdadm(t, scan, "", 0)

	if err := SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != scan {
		t.Errorf("config = %q, want %q (scan output replaces the file wholesale)", got, scan)
	}
}

// With no live arrays `mdadm --detail --scan` prints nothing and exits 0, so an
// empty config is the correct result of deleting the last array — not a failure.
func TestSaveConfigTruncatesWhenNoArraysRemain(t *testing.T) {
	path := useTempConfigPath(t, "ARRAY /dev/md0 UUID=deleted:one\n")
	fakeMdadm(t, "", "", 0)

	if err := SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("config = %q, want empty", got)
	}
}

// The old implementation shelled out to `mdadm --detail --scan > ConfigPath`.
// The shell truncates the target while setting up the redirect, before mdadm
// runs, so a transient mdadm failure wiped the config and every surviving
// array lost its boot-time ARRAY line.
func TestSaveConfigLeavesExistingConfigIntactWhenMdadmFails(t *testing.T) {
	existing := "ARRAY /dev/md0 metadata=1.2 UUID=survivor:1111:2222:3333\n"
	path := useTempConfigPath(t, existing)
	fakeMdadm(t, "", "mdadm: cannot open /dev/md0: No such device\n", 1)

	err := SaveConfig()
	if err == nil {
		t.Fatal("SaveConfig returned nil, want an error when mdadm exits non-zero")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if string(got) != existing {
		t.Errorf("config = %q, want it untouched (%q)", got, existing)
	}
}

// The stderr from a failed scan is the only diagnosis the operator gets.
func TestSaveConfigErrorIncludesMdadmStderr(t *testing.T) {
	useTempConfigPath(t, "")
	fakeMdadm(t, "", "mdadm: cannot open /dev/md0: No such device", 1)

	err := SaveConfig()
	if err == nil {
		t.Fatal("SaveConfig returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "No such device") {
		t.Errorf("error %q does not carry mdadm's stderr", err)
	}
}

// A failed SaveConfig must not leave its scratch file next to the config,
// where a later reader (or a packaging diff) would mistake it for real state.
func TestSaveConfigLeavesNoTempFileBehindOnFailure(t *testing.T) {
	path := useTempConfigPath(t, "ARRAY /dev/md0 UUID=survivor\n")
	fakeMdadm(t, "", "boom", 1)

	if err := SaveConfig(); err == nil {
		t.Fatal("SaveConfig returned nil, want an error")
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("stray file left in config dir: %q", e.Name())
		}
	}
}
