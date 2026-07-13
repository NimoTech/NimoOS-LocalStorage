package snapshot

import "testing"

func TestDefaultPolicyMatchesHandoffDefaults(t *testing.T) {
	p := DefaultPolicy("vol-uuid-123")

	if p.VolumeUUID != "vol-uuid-123" {
		t.Errorf("VolumeUUID = %q, want %q", p.VolumeUUID, "vol-uuid-123")
	}
	if p.Enabled {
		t.Error("Enabled = true, want false (must be explicitly opted in)")
	}
	if p.HourlyKeep != 24 {
		t.Errorf("HourlyKeep = %d, want 24", p.HourlyKeep)
	}
	if p.DailyKeep != 7 {
		t.Errorf("DailyKeep = %d, want 7", p.DailyKeep)
	}
	if p.WeeklyKeep != 4 {
		t.Errorf("WeeklyKeep = %d, want 4", p.WeeklyKeep)
	}
	if p.PauseThresholdPct != 90 {
		t.Errorf("PauseThresholdPct = %d, want 90", p.PauseThresholdPct)
	}
}
