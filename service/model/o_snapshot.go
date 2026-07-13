package model

import "time"

// Snapshot records one btrfs read-only snapshot subvolume living under a
// volume's @snapshots (snapshot feature handoff §3.2). The disk (a
// `btrfs subvolume list` of the volume, filtered to entries under
// @snapshots) is the source of truth for which snapshots actually exist;
// this row carries metadata — label, type, protection — that isn't
// recoverable from the subvolume name alone once it's not our own naming
// convention (see service/snapshot/reconcile.go).
type Snapshot struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	VolumeUUID   string     `json:"volume_uuid" gorm:"uniqueIndex:idx_snapshot_volume_name"`
	Name         string     `json:"name" gorm:"uniqueIndex:idx_snapshot_volume_name"`
	Type         string     `json:"type"`
	Label        string     `json:"label"`
	CreatedAt    time.Time  `json:"created_at"`
	ProtectUntil *time.Time `json:"protect_until"` // nullable; only set for type "preop"
	CreatedBy    string     `json:"created_by"`    // user_id (as string) or "system"
}

func (s *Snapshot) TableName() string {
	return "o_snapshot"
}
