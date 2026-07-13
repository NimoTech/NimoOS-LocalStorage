package snapshot

import (
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

// --- computeDueTypes -------------------------------------------------------

func TestComputeDueTypesAllDueWhenNeverCreated(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	due := computeDueTypes(now, map[string]time.Time{})
	want := map[string]bool{TypeAutoHourly: true, TypeAutoDaily: true, TypeAutoWeekly: true}
	if len(due) != 3 {
		t.Fatalf("expected all 3 cadences due on first run, got %v", due)
	}
	for _, d := range due {
		if !want[d] {
			t.Fatalf("unexpected due type %q", d)
		}
	}
}

func TestComputeDueTypesHourlyDueAfterOneHour(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	last := map[string]time.Time{
		TypeAutoHourly: now.Add(-1 * time.Hour),
		TypeAutoDaily:  now.Add(-1 * time.Hour),
		TypeAutoWeekly: now.Add(-1 * time.Hour),
	}
	due := computeDueTypes(now, last)
	if len(due) != 1 || due[0] != TypeAutoHourly {
		t.Fatalf("expected only hourly due, got %v", due)
	}
}

func TestComputeDueTypesNotDueBeforePeriodElapsed(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	last := map[string]time.Time{
		TypeAutoHourly: now.Add(-30 * time.Minute),
		TypeAutoDaily:  now.Add(-1 * time.Hour),
		TypeAutoWeekly: now.Add(-1 * time.Hour),
	}
	due := computeDueTypes(now, last)
	if len(due) != 0 {
		t.Fatalf("expected nothing due, got %v", due)
	}
}

func TestComputeDueTypesDailyAndWeeklyDueOnBoundary(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	last := map[string]time.Time{
		TypeAutoHourly: now.Add(-1 * time.Hour),
		TypeAutoDaily:  now.Add(-24 * time.Hour),
		TypeAutoWeekly: now.Add(-7 * 24 * time.Hour),
	}
	due := computeDueTypes(now, last)
	if len(due) != 3 {
		t.Fatalf("expected all 3 due exactly at their period boundary, got %v", due)
	}
}

// --- selectRetentionVictims -------------------------------------------------

func snap(id uint, typ string, createdAt time.Time) model.Snapshot {
	return model.Snapshot{ID: id, VolumeUUID: "vol-1", Name: typ, Type: typ, CreatedAt: createdAt}
}

func TestSelectRetentionVictimsKeepsNewestNPerType(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	snaps := []model.Snapshot{
		snap(1, TypeAutoHourly, now.Add(-3*time.Hour)),
		snap(2, TypeAutoHourly, now.Add(-2*time.Hour)),
		snap(3, TypeAutoHourly, now.Add(-1*time.Hour)),
	}
	policy := model.SnapshotPolicy{HourlyKeep: 2, DailyKeep: 7, WeeklyKeep: 4}

	victims := selectRetentionVictims(snaps, policy, now)
	if len(victims) != 1 || victims[0].ID != 1 {
		t.Fatalf("expected only the oldest hourly (id=1) to be a victim, got %+v", victims)
	}
}

func TestSelectRetentionVictimsOnlyTouchesOwnType(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	snaps := []model.Snapshot{
		snap(1, TypeAutoHourly, now.Add(-3*time.Hour)),
		snap(2, TypeAutoHourly, now.Add(-2*time.Hour)),
		snap(3, TypeAutoDaily, now.Add(-100*24*time.Hour)), // very old daily, but keep=7 and only 1 daily exists
	}
	policy := model.SnapshotPolicy{HourlyKeep: 1, DailyKeep: 7, WeeklyKeep: 4}

	victims := selectRetentionVictims(snaps, policy, now)
	if len(victims) != 1 || victims[0].ID != 1 {
		t.Fatalf("expected only oldest hourly (id=1) to be trimmed, daily untouched, got %+v", victims)
	}
}

func TestSelectRetentionVictimsManualNeverAutoDeleted(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	snaps := []model.Snapshot{
		snap(1, TypeManual, now.Add(-1000*24*time.Hour)),
		snap(2, TypeManual, now.Add(-999*24*time.Hour)),
	}
	// Even with keep counts of 0 (degenerate/clamped), manual must never be
	// selected — it's not in the keep-N cadence map at all.
	policy := model.SnapshotPolicy{HourlyKeep: 0, DailyKeep: 0, WeeklyKeep: 0}

	victims := selectRetentionVictims(snaps, policy, now)
	if len(victims) != 0 {
		t.Fatalf("expected manual snapshots never auto-deleted, got %+v", victims)
	}
}

func TestSelectRetentionVictimsPreopExpiredByProtectUntil(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	expired := now.Add(-1 * time.Minute)
	notYet := now.Add(1 * time.Hour)

	s1 := snap(1, TypePreop, now.Add(-2*time.Hour))
	s1.ProtectUntil = &expired
	s2 := snap(2, TypePreop, now.Add(-1*time.Hour))
	s2.ProtectUntil = &notYet

	policy := model.SnapshotPolicy{HourlyKeep: 24, DailyKeep: 7, WeeklyKeep: 4}
	victims := selectRetentionVictims([]model.Snapshot{s1, s2}, policy, now)
	if len(victims) != 1 || victims[0].ID != 1 {
		t.Fatalf("expected only the expired preop (id=1) to be a victim, got %+v", victims)
	}
}

func TestSelectRetentionVictimsClampsNonPositiveKeepToDefault(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	// 3 hourly snapshots, HourlyKeep=0 (defensive clamp note: a corrupt
	// policy shouldn't be able to wipe an entire cadence's snapshots via a
	// zero/negative keep count landing in the DB).
	snaps := []model.Snapshot{
		snap(1, TypeAutoHourly, now.Add(-3*time.Hour)),
		snap(2, TypeAutoHourly, now.Add(-2*time.Hour)),
		snap(3, TypeAutoHourly, now.Add(-1*time.Hour)),
	}
	policy := model.SnapshotPolicy{HourlyKeep: 0, DailyKeep: 7, WeeklyKeep: 4}

	victims := selectRetentionVictims(snaps, policy, now)
	// DefaultHourlyKeep is 24, so with only 3 existing, none should be
	// trimmed once the clamp kicks in.
	if len(victims) != 0 {
		t.Fatalf("expected clamp to DefaultHourlyKeep to prevent trimming, got %+v", victims)
	}
}

// --- clamp helpers -----------------------------------------------------

func TestClampPauseThreshold(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{90, 90},
		{1, 1},
		{100, 100},
		{0, DefaultPauseThresholdPct},
		{-5, DefaultPauseThresholdPct},
		{101, DefaultPauseThresholdPct},
		{500, DefaultPauseThresholdPct},
	}
	for _, c := range cases {
		if got := clampPauseThreshold(c.in); got != c.want {
			t.Errorf("clampPauseThreshold(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
