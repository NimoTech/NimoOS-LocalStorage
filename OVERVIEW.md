# NimoOS-LocalStorage Overview

NimoOS-LocalStorage is the microservice responsible for local disk and storage management, providing disk discovery, mount management, software RAID (mdadm), and USB/new-partition auto-mount.

---

## Core Responsibilities

- Disk and partition discovery and info collection (lsblk + S.M.A.R.T. + hwmon live temperature)
- Mount point management (create, mount, unmount; persisted to the database and fstab)
- Software RAID: mdadm-based array create/delete/status/disk-replace/boot recovery (`/v2/raid`)
- USB device and new-partition auto-mount (listens for udev hotplug events)
- Filesystem formatting (fdisk, mkfs)
- Remote/cloud storage mounting (via rclone, `/v1/cloud`)
- Publishes disk events to MessageBus (added/removed/status)
- System disk detection (including overlayroot scenarios) and capacity accounting

> The MergerFS merged-volume code still exists (`service/v2/merge.go`, `service/v2/fs/mergerfs.go`, the `o_merge` table), but the v2 API no longer exposes the `/merge` endpoint; `EnsureDefaultMergePoint` in `main.go` is commented out and the config defaults to `EnableMergerFS=false` — currently dormant.

---

## Directory Structure

```
NimoOS-LocalStorage/
├── main.go                  # Entry point: RAID boot recovery, route registration, 5s periodic status reporting
├── misc.go                  # udev event listening/forwarding, partition auto-mount trigger, disk status push
├── api/local_storage/       # OpenAPI spec (V2 mount API)
├── model/                   # V1 data models (disk, USB, S.M.A.R.T., power_on_time)
├── service/
│   ├── disk.go              # DiskService (lsblk, SMART cache, auto-mount, InitCheck)
│   ├── storage.go           # rclone remote storage
│   ├── usb.go               # USB auto-mount configuration
│   ├── model/
│   │   ├── o_volume.go      # Volume model (disk mount record, table name o_disk)
│   │   ├── o_merge.go       # Merge model (merged volume, dormant)
│   │   ├── o_raid.go        # RAIDArray model (o_raid table)
│   │   └── o_raid_member.go # RAIDMember model (persistent member-disk identity)
│   └── v2/
│       ├── mount.go         # V2 mount/unmount core logic
│       ├── merge.go         # Merged volume management (code kept, API not exposed)
│       ├── fstab.go         # fstab persistence layer
│       ├── fs/mergerfs.go   # MergerFS filesystem extension (PreMount hook)
│       ├── raid.go          # RAIDService: create/delete/status/disk-replace/boot-recovery/retry
│       ├── raid_filesystem.go # ext4/btrfs formatting and mounting (btrfs subvolumes @/@snapshots)
│       ├── raid_usage.go    # btrfs filesystem usage query (10-minute cache)
│       ├── raid_resize.go   # Filesystem expansion (resize2fs / btrfs resize)
│       └── raid_pathcheck.go # Pre-delete guard: system path conflicts, busy/sub-mount diagnostics
├── route/
│   ├── v1/                  # V1 legacy routes (disk, storage, USB, cloud storage)
│   ├── v2/                  # V2 OpenAPI routes (mount) + RAID handlers and async tasks
│   └── raid.go              # /v2/raid standalone echo route (JWT, not OpenAPI-generated)
├── pkg/
│   ├── cache/               # In-memory cache (lsblk 100s, SMART 5min)
│   ├── diskid/              # Persistent disk identity (/dev/disk/by-id with serial-number fallback)
│   ├── hwmon/               # Kernel hwmon live disk temperature reads (drivetemp/nvme)
│   ├── mdadm/               # mdadm CLI wrapper and --detail / /proc/mdstat parsing
│   ├── mount/                # System mount operation wrapper
│   ├── partition/           # Partition operations (fdisk, mkfs)
│   └── fstab/               # fstab file parsing and modification
├── drivers/                 # Cloud storage drivers (dropbox, google_drive)
└── build/                   # systemd service files, sample configs
```

---

## API Versions

### V1 (legacy, JWT-verified, `route/v1.go`)

**Disk management**
```
GET    /v1/disks                   List all disks (SMART + hwmon temperature + power_on_time)
GET    /v1/disks/usb               List USB devices
DELETE /v1/disks/usb               Eject a USB device
DELETE /v1/disks                   Unmount a disk
GET    /v1/disks/size              Disk space info
```

**Local storage (partitions)**
```
GET    /v1/storage                 List disks/partitions (with system-disk detection and capacity normalization)
POST   /v1/storage                 Add storage (optionally repartition and format first)
PUT    /v1/storage                 Format storage
DELETE /v1/storage                 Remove storage (unmount)
```

**USB auto-mount**
```
GET    /v1/usb/usb-auto-mount      Query auto-mount status
PUT    /v1/usb/usb-auto-mount      Toggle auto-mount
```

**Cloud storage (rclone)**
```
GET    /v1/cloud                   List mounted remote storage
DELETE /v1/cloud                   Remove remote storage
GET    /v1/driver                  List cloud storage driver info
```

> The gateway only registers `/v1/usb`, `/v1/disks`, `/v1/storage`, and the v2 paths (see `apiPaths` in `main.go`); the `/v1/cloud` and `/v1/driver` routes exist but are not registered with the gateway.

### V2 Mount (OpenAPI 3.0, `api/local_storage/openapi.yaml`)

```
GET    /v2/local_storage/mount             List all mount points
POST   /v2/local_storage/mount             Create a new mount
PUT    /v2/local_storage/mount/{point}     Update a mount
DELETE /v2/local_storage/mount             Unmount
```

### V2 RAID (hand-written echo routes, `route/raid.go`)

```
GET    /v2/raid                    List all RAID arrays
POST   /v2/raid                    Create an array asynchronously (202 + task_id)
DELETE /v2/raid/:id                Delete an array (with multiple guards)
GET    /v2/raid/:id/status         Live status (rebuild progress/ETA/speed/capacity)
GET    /v2/raid/:id/usage          Capacity usage (btrfs filesystem usage)
POST   /v2/raid/:id/disk           Replace a disk (old → new, triggers rebuild)
POST   /v2/raid/:id/recover        Manually trigger reassembly + mount
GET    /v2/raid/tasks              List creation tasks
GET    /v2/raid/tasks/:task_id     Query creation task progress
```

---

## Core Business Logic

### Disk Info Collection

```
GET /v1/disks
  → lsblk --json (list endpoint cached for 100 seconds, see LSBLK in service/disk.go)
  → runs smartctl per disk (result cached for 5 minutes; uses -n standby to avoid waking sleeping disks)
  → hwmon (drivetemp/nvme) reads live sysfs temperature, overriding the cached SMART value (pkg/hwmon)
  → returns disk info (including temperature and power-on hours: power_on_time)
```

- SMART cache was shortened from 24 hours to 5 minutes: temperature is part of the SMART data and must stay fresh (`service/disk.go` `SmartCTL`).
- hwmon is a plain file read — instant, needs no cache, and never wakes a sleeping disk (`route/v1/disk.go`).

### System Disk Detection and Capacity Accounting (`route/v1/storage.go` GetStorageList)

- A root mount point of `/`, `/mnt/overlay`, or `/media/root-ro` (overlayroot scenario) is recognized as the system disk.
- The system disk emits only a single root-partition entry, with `mount_point` normalized to `/` (the frontend matches by path prefix).
- Capacity is taken from the whole-disk physical size; used space is the sum of each partition's `FSUsed` (to avoid double-counting reserved blocks).

### Mount Flow (V2)

```
POST /v2/local_storage/mount
  → PreMount hook (filesystem extension point, service/v2/fs/)
  → verify the mount point is an empty directory
  → run the system mount call
  → verify the mount succeeded
  → PostMount hook
  → optionally persist to fstab
```

### Software RAID (`service/v2/raid.go` + `pkg/mdadm`)

Supports RAID 0/1/5/6/10 (minimum 2/2/3/4/4 disks; RAID 10 requires an even number of disks, an odd count returns 400 immediately). Supports ext4 / btrfs filesystems, defaulting to btrfs.

**Async creation** (`route/v2/raid.go` + `raid_task.go`):

```
POST /v2/raid → 202 + task_id (only one creation task allowed at a time)
  A background goroutine reports progress in 5 steps (poll GET /v2/raid/tasks/:task_id):
  1. Load kernel modules (raid0/1/456/10), validate name and device paths
  2. mdadm --zero-superblock to clear stale metadata on member disks
  3. mdadm --create /dev/mdN, wait for the device to appear (10s)
  4. wipefs to clear signatures → format (btrfs creates @ / @snapshots subvolumes)
  5. mount to /media/RAID_<name>, persist member-disk identities to the DB
```

- btrfs mount options are `space_cache=v2,noatime,compress=zstd:1,subvol=@`, falling back to `subvolid=5` if the subvolume is missing (`raid_filesystem.go`).
- Member disks are recorded with a persistent identity via `pkg/diskid` (`/dev/disk/by-id` preferred, serial-number fallback); `/dev/sdX` is only a cache — device-letter drift after reboot doesn't affect identification (`o_raid_member` table).

**Status query**: merges `mdadm --detail` output with `/proc/mdstat` parsing (`pkg/mdadm/parse.go`), exposing rebuild percentage, ETA, rebuild speed, member-disk status, and df capacity; btrfs usage goes through `btrfs filesystem usage`, cached for 10 minutes (`raid_usage.go`).

**Boot recovery** (`RecoverOnBoot`, invoked at `main.go` startup):

- After `mdadm --assemble --scan`, arrays are matched to DB records by UUID and mounted (independent of device letters);
- Arrays present on the system but missing from the DB are auto-registered (reading detail, probing the filesystem);
- Arrays that fail to assemble go into a retry worker (5 attempts × 30 seconds, auto-recovers after hotplug); if all attempts fail the array is marked `failed`; `POST /v2/raid/:id/recover` can be triggered manually at any time and cancels any in-progress retry.

**Disk replacement** (`ReplaceDisk`): if the old disk has already been physically removed, `--fail --remove` is skipped and the new disk goes straight to `--add`; the member identity is updated and state is set to `rebuilding`.

**Delete guards** (`DeleteRAIDArray` + `raid_pathcheck.go`):

1. Deletion is refused if the array still holds system data paths (Docker images, AppData, database — read from `/var/lib/nimoos/path_config.json`);
2. Sub-mounts (e.g. leftover Docker/containerd overlays) are unmounted one at a time, depth-first, without lazy umount (which would mask a busy state and cause a later stop to fail); a busy state returns an error with diagnostic info;
3. Before stopping the array, `vgchange -an` deactivates any LVM volume group that uses the device as a PV;
4. After `mdadm --stop`, superblocks are zeroed on any member disks that can still be resolved, and finally the DB record is deleted.

**mdadm binary**: uses the system-installed mdadm (resolved via PATH), overridable with the `MDADM_PATH` environment variable (`pkg/mdadm/mdadm.go`). At startup, `mdadm.CheckSupport()` probes once; if not installed, it logs that RAID functionality is unavailable — handled the same way as the btrfs toolchain.

### USB / New-Partition Auto-Mount (`misc.go` + `service/disk.go`)

- udev `partition add` event → `AutoMountPartition`: an unmounted partition with a UUID is auto-mounted to `/mnt/Disk-<first-8-chars-of-UUID>` and persisted to the DB; `CheckSerialDiskMount` restores it after reboot.
- udev events for RAID devices (`/dev/md*`) are not published to MessageBus.

### Persistence Strategy (three layers)

1. **Memory**: runtime mount info
2. **Database**: mount and array records managed by NimoOS (`o_disk`, `o_raid`, `o_raid_member` tables)
3. **fstab**: system-level persistence (`/etc/fstab`, optional)

---

## Event Publishing

Storage status is reported every 5 seconds (widget data is deduplicated by mount point to avoid double-counting RAID etc.), while udev events are forwarded in real time:

| Event | Trigger |
|---|---|
| `local-storage:disk:added` | Disk/USB inserted |
| `local-storage:disk:removed` | Disk/USB removed |
| `local-storage:storage_status` | Periodic report every 5 seconds |

---

## Database Models

```sql
-- Disk mount record (model Volume, legacy table name)
CREATE TABLE o_disk (
    id INTEGER PRIMARY KEY,
    uuid TEXT,
    mount_point TEXT,
    created_at INTEGER
);

-- RAID array
CREATE TABLE o_raid (
    id INTEGER PRIMARY KEY,
    name TEXT,
    level INTEGER,              -- 0/1/5/6/10
    filesystem TEXT,            -- ext4 / btrfs
    device_path TEXT,           -- /dev/mdN (cached, can change across reboots)
    mount_point TEXT,           -- /media/RAID_<name>
    uuid TEXT UNIQUE,           -- mdadm array UUID (persistent identity)
    state TEXT,                 -- active / degraded / rebuilding / failed
    chunk_kb INTEGER
);

-- RAID member disks (persistent identity)
CREATE TABLE o_raid_member (
    id INTEGER PRIMARY KEY,
    raid_array_id INTEGER,
    disk_by_id TEXT,            -- /dev/disk/by-id name
    disk_serial TEXT,           -- serial-number fallback
    device_path_cache TEXT      -- /dev/sdX, cache only
);

-- Merged volumes (kept, feature dormant)
CREATE TABLE o_merge (...);      -- plus the o_merge_disk many-to-many association
```

---

## Configuration

```ini
[common]
RuntimePath = /var/run/nimoos

[app]
LogPath = /var/log/nimoos
DBPath = /var/lib/nimoos/db
ShellPath = /usr/share/nimoos/shell

[server]
USBAutoMount = True
EnableMergerFS = false
```

---

## System Dependencies

| Tool | Purpose |
|---|---|
| lsblk / blkid | Block device info collection, filesystem detection |
| smartctl | Disk health monitoring (`-n standby` avoids waking sleeping disks) |
| hwmon (sysfs) | Live disk temperature reads (drivetemp / nvme drivers) |
| mount/umount | Filesystem mount operations |
| fdisk/parted | Partition management |
| mkfs / wipefs | Filesystem formatting, signature clearing |
| mdadm | Software RAID (installed by the distro, `mdadm` package) |
| btrfs-progs / resize2fs | btrfs subvolumes and usage, filesystem resizing |
| pvs / vgchange (LVM) | Deactivates associated volume groups before deleting a RAID array |
| rclone | Remote storage mounting |
| udev | Hotplug event listening |
| mergerfs | Virtual merged volume (code kept, currently not enabled) |
