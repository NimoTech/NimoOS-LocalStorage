package model

import "time"

type RAIDArray struct {
	ID          uint          `gorm:"primarykey" json:"id"`
	Name        string        `json:"name"`
	Level       int           `json:"level"`
	Filesystem  string        `json:"filesystem"`
	DevicePath  string        `json:"device_path"`
	MountPoint  string        `json:"mount_point"`
	UUID        string        `json:"uuid" gorm:"uniqueIndex"`
	State       string        `json:"state"`
	ChunkKB     int           `json:"chunk_kb"`
	MemberDisks []*RAIDMember `gorm:"foreignKey:RAIDArrayID" json:"member_disks"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

func (r *RAIDArray) TableName() string {
	return "o_raid"
}
