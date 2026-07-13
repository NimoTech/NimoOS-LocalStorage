// Package snapshot implements the foundation layer for NimoOS's btrfs
// snapshot feature: a testable wrapper around the btrfs/mount tooling,
// ensuring the @snapshots subvolume is mounted for eligible volumes,
// snapshot subvolume naming/parsing, and disk<->DB reconciliation.
//
// See the feature handoff document (§3.1/§3.2) for the exact naming format,
// table schemas and reconciliation rules this package implements verbatim.
package snapshot

import (
	"fmt"
	"strings"
	"time"
)

// Snapshot types (handoff §3.1). These are the only values FormatName
// accepts; TypeUnknown is not a valid input here — it's only ever produced
// by reconciliation (see reconcile.go) for on-disk subvolumes whose name
// doesn't match our own convention.
const (
	TypeAutoHourly = "auto-hourly"
	TypeAutoDaily  = "auto-daily"
	TypeAutoWeekly = "auto-weekly"
	TypeManual     = "manual"
	TypePreop      = "preop"
	TypeUnknown    = "unknown"
)

var validTypes = map[string]struct{}{
	TypeAutoHourly: {},
	TypeAutoDaily:  {},
	TypeAutoWeekly: {},
	TypeManual:     {},
	TypePreop:      {},
}

// IsValidType reports whether t is one of the known snapshot types that may
// be passed to FormatName. TypeUnknown is intentionally excluded: it is a
// reconciliation-only marker, never a snapshot a caller asks us to create.
func IsValidType(t string) bool {
	_, ok := validTypes[t]
	return ok
}

// nameTimeLayout is the ISO 8601 basic UTC format used in snapshot
// subvolume names, e.g. "20260712T030000Z".
const nameTimeLayout = "20060102T150405Z"

// maxLabelLen bounds a sanitized label so a runaway user-supplied string
// can't produce an absurdly long subvolume name.
const maxLabelLen = 80

// FormatName builds a snapshot subvolume name of the form
// "<ISO8601 basic>_<type>[_<label>]" (handoff §3.1), e.g.
// "20260712T030000Z_auto-hourly" or "20260712T101502Z_manual_改版前".
// t is converted to UTC. label is sanitized via SanitizeLabel; if that
// yields an empty string, the label segment is omitted entirely.
func FormatName(t time.Time, snapshotType, label string) (string, error) {
	if !IsValidType(snapshotType) {
		return "", fmt.Errorf("invalid snapshot type: %q", snapshotType)
	}
	name := t.UTC().Format(nameTimeLayout) + "_" + snapshotType
	if clean := SanitizeLabel(label); clean != "" {
		name += "_" + clean
	}
	return name, nil
}

// ParsedName is the result of parsing a snapshot subvolume name produced by
// FormatName back into its constituent parts.
type ParsedName struct {
	Time  time.Time
	Type  string
	Label string
}

// ParseName parses a snapshot subvolume name of the shape
// "<ISO8601 basic>_<type>[_<label>]" back into its timestamp, type and
// (possibly empty) label. It returns an error if the timestamp segment
// doesn't parse or the type segment isn't one of the known types (see
// IsValidType) — both are treated as "this isn't one of ours".
func ParseName(name string) (ParsedName, error) {
	parts := strings.SplitN(name, "_", 3)
	if len(parts) < 2 {
		return ParsedName{}, fmt.Errorf("snapshot name %q does not match <timestamp>_<type>[_<label>]", name)
	}

	t, err := time.Parse(nameTimeLayout, parts[0])
	if err != nil {
		return ParsedName{}, fmt.Errorf("invalid timestamp in snapshot name %q: %w", name, err)
	}

	if !IsValidType(parts[1]) {
		return ParsedName{}, fmt.Errorf("invalid snapshot type in snapshot name %q: %q", name, parts[1])
	}

	result := ParsedName{Time: t.UTC(), Type: parts[1]}
	if len(parts) == 3 {
		result.Label = parts[2]
	}
	return result, nil
}

// SanitizeLabel makes a user-supplied label safe to embed as one "_"
// delimited segment of a snapshot subvolume name:
//   - trims leading/trailing whitespace,
//   - drops path separators, NUL, and other control characters,
//   - replaces "_" with "-" so it can never be confused with our own field
//     separator when the name is parsed back (see ParseName),
//   - caps the result to maxLabelLen runes.
//
// Unicode letters (e.g. CJK, as in the handoff's own "改版前" example) are
// preserved — btrfs subvolume names are UTF-8 safe.
func SanitizeLabel(label string) string {
	label = strings.TrimSpace(label)

	var b strings.Builder
	for _, r := range label {
		switch {
		case r == '/' || r == '\\' || r == 0:
			continue
		case r < 0x20 || r == 0x7f:
			continue
		case r == '_':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}

	result := strings.Trim(b.String(), ". ")

	if runes := []rune(result); len(runes) > maxLabelLen {
		result = strings.TrimSpace(string(runes[:maxLabelLen]))
	}

	return result
}
