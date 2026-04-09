package model

import "time"

// RAIDArray represents a software RAID array managed by mdadm.
type RAIDArray struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	Name        string    `json:"name"`
	Level       int       `json:"level"`                                     // 0, 1, 5, 6
	DevicePath  string    `json:"device_path"`                               // /dev/md0
	MountPoint  string    `json:"mount_point"`                               // /media/RAID_xxx
	UUID        string    `json:"uuid" gorm:"uniqueIndex"`                   // mdadm array UUID
	State       string    `json:"state"`                                     // active, degraded, failed, rebuilding
	ChunkKB     int       `json:"chunk_kb"`                                  // chunk size in KB
	MemberDisks []*Volume `json:"member_disks" gorm:"many2many:o_raid_disk"` // member disk volumes
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (r *RAIDArray) TableName() string {
	return "o_raid"
}
