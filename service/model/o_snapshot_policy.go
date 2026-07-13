package model

import "time"

// SnapshotPolicy is the per-volume automatic btrfs snapshot configuration
// (snapshot feature handoff §3.2). One row per volume_uuid.
type SnapshotPolicy struct {
	ID                uint      `gorm:"primarykey" json:"id"`
	VolumeUUID        string    `json:"volume_uuid" gorm:"uniqueIndex"`
	Enabled           bool      `json:"enabled"`
	HourlyKeep        int       `json:"hourly_keep"`
	DailyKeep         int       `json:"daily_keep"`
	WeeklyKeep        int       `json:"weekly_keep"`
	PauseThresholdPct int       `json:"pause_threshold_pct"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (p *SnapshotPolicy) TableName() string {
	return "o_snapshot_policy"
}
