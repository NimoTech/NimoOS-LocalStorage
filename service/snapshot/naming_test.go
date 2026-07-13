package snapshot

import (
	"testing"
	"time"
)

func TestFormatNameBasic(t *testing.T) {
	ts := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	name, err := FormatName(ts, TypeAutoHourly, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "20260712T030000Z_auto-hourly"
	if name != want {
		t.Errorf("FormatName() = %q, want %q", name, want)
	}
}

func TestFormatNameWithLabel(t *testing.T) {
	ts := time.Date(2026, 7, 12, 10, 15, 2, 0, time.UTC)
	name, err := FormatName(ts, TypeManual, "改版前")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "20260712T101502Z_manual_改版前"
	if name != want {
		t.Errorf("FormatName() = %q, want %q", name, want)
	}
}

func TestFormatNameConvertsToUTC(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	ts := time.Date(2026, 7, 12, 11, 0, 0, 0, loc) // == 2026-07-12T03:00:00Z
	name, err := FormatName(ts, TypeAutoDaily, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "20260712T030000Z_auto-daily"
	if name != want {
		t.Errorf("FormatName() = %q, want %q", name, want)
	}
}

func TestFormatNameRejectsInvalidType(t *testing.T) {
	if _, err := FormatName(time.Now(), "bogus", ""); err == nil {
		t.Error("expected error for invalid snapshot type, got nil")
	}
}

func TestFormatNameOmitsEmptyLabelSegment(t *testing.T) {
	ts := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	// A label that sanitizes to nothing (only path separators) must not
	// leave a trailing empty "_" segment.
	name, err := FormatName(ts, TypeManual, "///")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "20260712T030000Z_manual"
	if name != want {
		t.Errorf("FormatName() = %q, want %q", name, want)
	}
}

func TestParseNameRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		snapT string
		label string
	}{
		{"20260712T030000Z_auto-hourly", TypeAutoHourly, ""},
		{"20260712T101502Z_manual_改版前", TypeManual, "改版前"},
		{"20260712T000000Z_auto-weekly_weekly-backup", TypeAutoWeekly, "weekly-backup"},
		{"20260712T000000Z_preop", TypePreop, ""},
	}
	for _, c := range cases {
		parsed, err := ParseName(c.name)
		if err != nil {
			t.Fatalf("ParseName(%q) error: %v", c.name, err)
		}
		if parsed.Type != c.snapT {
			t.Errorf("ParseName(%q).Type = %q, want %q", c.name, parsed.Type, c.snapT)
		}
		if parsed.Label != c.label {
			t.Errorf("ParseName(%q).Label = %q, want %q", c.name, parsed.Label, c.label)
		}
		if parsed.Time.IsZero() {
			t.Errorf("ParseName(%q).Time is zero", c.name)
		}
	}
}

func TestParseNameRoundTripsWithFormatName(t *testing.T) {
	ts := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	name, err := FormatName(ts, TypeAutoHourly, "hello world")
	if err != nil {
		t.Fatalf("FormatName error: %v", err)
	}
	parsed, err := ParseName(name)
	if err != nil {
		t.Fatalf("ParseName(%q) error: %v", name, err)
	}
	if !parsed.Time.Equal(ts) {
		t.Errorf("parsed.Time = %v, want %v", parsed.Time, ts)
	}
	if parsed.Type != TypeAutoHourly {
		t.Errorf("parsed.Type = %q, want %q", parsed.Type, TypeAutoHourly)
	}
	if parsed.Label != "hello world" {
		t.Errorf("parsed.Label = %q, want %q", parsed.Label, "hello world")
	}
}

func TestParseNameRejectsMalformedNames(t *testing.T) {
	cases := []string{
		"",
		"notatimestamp_manual",
		"20260712T030000Z",            // missing type
		"20260712T030000Z_bogus-type", // invalid type
		"20260712_manual",             // wrong timestamp shape (missing time part)
	}
	for _, name := range cases {
		if _, err := ParseName(name); err == nil {
			t.Errorf("ParseName(%q) expected error, got nil", name)
		}
	}
}

func TestSanitizeLabelStripsPathSeparatorsAndControlChars(t *testing.T) {
	// Path separators and control chars are dropped; leading/trailing dots
	// are also trimmed (defense against ".."-style or hidden-file labels).
	got := SanitizeLabel("../etc/passwd\x00\n")
	if got != "etcpasswd" {
		t.Errorf("SanitizeLabel = %q, want %q", got, "etcpasswd")
	}
}

func TestSanitizeLabelReplacesUnderscoreWithHyphen(t *testing.T) {
	got := SanitizeLabel("before_change")
	if got != "before-change" {
		t.Errorf("SanitizeLabel = %q, want %q", got, "before-change")
	}
	// Guarantees FormatName/ParseName round-trip never gets confused by
	// stray underscores inside a label.
	name, err := FormatName(time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC), TypeManual, "before_change")
	if err != nil {
		t.Fatalf("FormatName error: %v", err)
	}
	parsed, err := ParseName(name)
	if err != nil {
		t.Fatalf("ParseName(%q) error: %v", name, err)
	}
	if parsed.Label != "before-change" {
		t.Errorf("parsed.Label = %q, want %q", parsed.Label, "before-change")
	}
}

func TestSanitizeLabelPreservesUnicode(t *testing.T) {
	got := SanitizeLabel("改版前")
	if got != "改版前" {
		t.Errorf("SanitizeLabel = %q, want %q", got, "改版前")
	}
}

func TestSanitizeLabelCapsLength(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := SanitizeLabel(long)
	if len(got) > 80 {
		t.Errorf("SanitizeLabel length = %d, want <= 80", len(got))
	}
}

func TestIsValidType(t *testing.T) {
	for _, valid := range []string{TypeAutoHourly, TypeAutoDaily, TypeAutoWeekly, TypeManual, TypePreop} {
		if !IsValidType(valid) {
			t.Errorf("IsValidType(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "auto", "unknown", "AUTO-HOURLY"} {
		if IsValidType(invalid) {
			t.Errorf("IsValidType(%q) = true, want false", invalid)
		}
	}
}
