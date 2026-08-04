package snapshot

import "github.com/NimoTech/NimoOS-LocalStorage/service/model"

// Recommended default policy values (handoff §2.1 "the simple-mode defaults are the recommended values"):
// hourly x24 (a day), daily x7 (a week), weekly x4 (a month), pause
// automatic snapshots once the volume is 90% full.
const (
	DefaultHourlyKeep        = 24
	DefaultDailyKeep         = 7
	DefaultWeeklyKeep        = 4
	DefaultPauseThresholdPct = 90
)

// DefaultPolicy returns the recommended default policy for a volume that
// doesn't have one configured yet. Enabled is false: a volume never starts
// taking automatic snapshots on its own — the user (or an API call in a
// later task) must opt in explicitly.
func DefaultPolicy(volumeUUID string) model.SnapshotPolicy {
	return model.SnapshotPolicy{
		VolumeUUID:        volumeUUID,
		Enabled:           false,
		HourlyKeep:        DefaultHourlyKeep,
		DailyKeep:         DefaultDailyKeep,
		WeeklyKeep:        DefaultWeeklyKeep,
		PauseThresholdPct: DefaultPauseThresholdPct,
	}
}
