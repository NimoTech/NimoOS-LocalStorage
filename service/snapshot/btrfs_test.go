package snapshot

import (
	"reflect"
	"testing"
)

func TestParseSubvolumeList(t *testing.T) {
	out := []byte(`ID 256 gen 15 top level 5 path @
ID 257 gen 15 top level 5 path @snapshots
ID 258 gen 20 top level 257 path @snapshots/20260712T030000Z_auto-hourly
ID 259 gen 21 top level 257 path @snapshots/20260712T101502Z_manual_改版前
`)

	got := parseSubvolumeList(out)
	want := []SubvolumeEntry{
		{ID: "256", Path: "@"},
		{ID: "257", Path: "@snapshots"},
		{ID: "258", Path: "@snapshots/20260712T030000Z_auto-hourly"},
		{ID: "259", Path: "@snapshots/20260712T101502Z_manual_改版前"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSubvolumeList() = %+v, want %+v", got, want)
	}
}

func TestParseSubvolumeListEmpty(t *testing.T) {
	got := parseSubvolumeList([]byte(""))
	if len(got) != 0 {
		t.Errorf("parseSubvolumeList(empty) = %+v, want empty", got)
	}
}

func TestParseSubvolumeListIgnoresBlankLines(t *testing.T) {
	out := []byte("\nID 256 gen 15 top level 5 path @\n\n")
	got := parseSubvolumeList(out)
	if len(got) != 1 || got[0].Path != "@" {
		t.Errorf("parseSubvolumeList() = %+v, want single entry with path @", got)
	}
}

func TestFilterSnapshotNames(t *testing.T) {
	entries := []SubvolumeEntry{
		{ID: "256", Path: "@"},
		{ID: "257", Path: "@snapshots"}, // the subvolume itself, not a snapshot under it
		{ID: "258", Path: "@snapshots/20260712T030000Z_auto-hourly"},
		{ID: "259", Path: "@snapshots/20260712T101502Z_manual_改版前"},
		{ID: "260", Path: "@snapshots/nested/deeper"}, // not a direct child, ignored
	}

	got := FilterSnapshotNames(entries)
	want := []string{
		"20260712T030000Z_auto-hourly",
		"20260712T101502Z_manual_改版前",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterSnapshotNames() = %v, want %v", got, want)
	}
}

func TestExecRunnerImplementsRunner(t *testing.T) {
	var _ Runner = NewExecRunner()
}
