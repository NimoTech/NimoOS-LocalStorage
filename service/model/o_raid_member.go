package model

import "time"

// RAIDMember tracks a single member disk of a RAID array using persistent identifiers.
// DiskByID and DiskSerial are stable across reboots; DevicePathCache is a best-effort cache.
type RAIDMember struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	RAIDArrayID     uint      `json:"raid_array_id"`
	DiskByID        string    `json:"disk_by_id"`        // e.g. ata-Samsung_SSD_870_EVO_S1234
	DiskSerial      string    `json:"disk_serial"`       // serial number fallback
	DevicePathCache string    `json:"device_path_cache"` // e.g. /dev/sda — cache only
	CreatedAt       time.Time `json:"created_at"`
}

func (r *RAIDMember) TableName() string { return "o_raid_member" }
