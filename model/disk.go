/*@Author: LinkLeong link@icewhale.com
 *@Date: 2022-07-13 10:43:45
 *@LastEditors: LinkLeong
 *@LastEditTime: 2022-08-03 14:45:35
 *@FilePath: /NimoOS/model/disk.go
 *@Description:
 *Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 *Copyright (c) 2026 NimoTech
 *Licensed under the Apache License, Version 2.0.
 *Modified from the original CasaOS source by NimoTech.
 */
package model

import "encoding/json"

type LSBLKModel struct {
	Name        string       `json:"name"`
	FsType      string       `json:"fstype"`
	Size        uint64       `json:"size"`
	FSSize      json.Number  `json:"fssize"`
	Path        string       `json:"path"`
	Model       string       `json:"model"` // device identifier
	RM          bool         `json:"rm"`    // whether the device is removable
	RO          bool         `json:"ro"`    // whether the device is read-only
	State       string       `json:"state"`
	PhySec      int          `json:"phy-sec"` // physical sector size
	Type        string       `json:"type"`
	Vendor      string       `json:"vendor"`  // vendor
	Rev         string       `json:"rev"`     // revision
	FSAvail     json.Number  `json:"fsavail"` // available space
	FSUse       string       `json:"fsuse%"`  // used percentage
	MountPoint  string       `json:"mountpoint"`
	MountPoints []string     `json:"mountpoints"`
	Format      string       `json:"format"`
	Health      string       `json:"health"`
	HotPlug     bool         `json:"hotplug"`
	UUID        string       `json:"uuid"`
	PTUUID      string       `json:"ptuuid"`
	PartUUID    string       `json:"partuuid"`
	FSUsed      json.Number  `json:"fsused"`
	Temperature int          `json:"temperature"`
	Tran        string       `json:"tran"`
	MinIO       uint64       `json:"min-io"`
	UsedPercent float64      `json:"used_percent"`
	Serial      string       `json:"serial"`
	Children    []LSBLKModel `json:"children"`
	SubSystems  string       `json:"subsystems"`
	Label       string       `json:"label"`
	// detail-only fields
	StartSector uint64 `json:"start_sector,omitempty"`
	Rota        bool   `json:"rota"` // true(hhd) false(ssd)
	DiskType    string `json:"disk_type"`
	EndSector   uint64 `json:"end_sector,omitempty"`
}

type Drive struct {
	Name           string         `json:"name"`
	Size           uint64         `json:"size"`
	Model          string         `json:"model"`
	Health         string         `json:"health"`
	Temperature    int            `json:"temperature"`
	PowerOnTime    int            `json:"power_on_time"`
	DiskType       string         `json:"disk_type"`
	NeedFormat     bool           `json:"need_format"`
	Serial         string         `json:"serial"`
	DiskByID       string         `json:"disk_by_id,omitempty"` // /dev/disk/by-id entry name
	Path           string         `json:"path"`
	ChildrenNumber int            `json:"children_number"`
	Children       []DiskChildren `json:"children"`
	Supported      bool           `json:"supported"`
	// Raid describes this disk's relationship to md RAID, if any: an active
	// or registered member (protected — never offered as a free disk) or a
	// leftover superblock of a foreign array (usable after an explicit wipe
	// confirmation). nil for disks with no RAID trace.
	Raid *DriveRaidInfo `json:"raid,omitempty"`
}

// DriveRaidInfo is the UI-facing summary of a disk's md superblock state.
type DriveRaidInfo struct {
	// Role is "member" (part of an array this system runs or has registered)
	// or "residue" (superblock of a foreign/abandoned array).
	Role       string `json:"role"`
	ArrayName  string `json:"array_name"`
	ArrayUUID  string `json:"array_uuid,omitempty"`
	Level      string `json:"level,omitempty"`      // e.g. raid10
	MdDevice   string `json:"md_device,omitempty"`  // /dev/mdX when attached to one
	Registered bool   `json:"registered"`           // known to this system's DB
	Active     bool   `json:"active"`               // md device currently running
	CreatedAt  string `json:"created_at,omitempty"` // superblock creation time (residue)
	UpdatedAt  string `json:"updated_at,omitempty"` // last superblock update (residue)
}

type DiskChildren struct {
	Name       string `json:"name"`
	Size       uint64 `json:"size"`
	Format     string `json:"format"`
	Supported  bool   `json:"supported"`
	MountPoint string `json:"mount_point,omitempty"`
	// UsedBytes is filled from statfs for mounted partitions; 0 otherwise.
	UsedBytes uint64 `json:"used_bytes,omitempty"`
}

type USBDriveStatus struct {
	Name     string        `json:"name"`
	Size     uint64        `json:"size"`
	Model    string        `json:"model"`
	Avail    uint64        `json:"avail"`
	Children []USBChildren `json:"children"`
}
type USBChildren struct {
	Name       string `json:"name"`
	Size       uint64 `json:"size"`
	Avail      uint64 `json:"avail"`
	MountPoint string `json:"mount_point"`
}

type Storage struct {
	UUID        string `json:"uuid"`
	MountPoint  string `json:"mount_point"`
	Size        string `json:"size"`
	Avail       string `json:"avail"`
	Used        string `json:"used"`
	Type        string `json:"type"`
	Path        string `json:"path"`
	DriveName   string `json:"drive_name"`
	Label       string `json:"label"`
	PersistedIn string `json:"persisted_in"` // none, fstab, nimoos
}
type Storages struct {
	DiskName string    `json:"disk_name"`
	Size     uint64    `json:"size"`
	Path     string    `json:"path"`
	Children []Storage `json:"children"`
	Type     string    `json:"type"`
}

type DiskStatus struct {
	Size   uint64 `json:"size"`
	Avail  uint64 `json:"avail"` // available space
	Health bool   `json:"health"`
	Used   uint64 `json:"used"`
}
