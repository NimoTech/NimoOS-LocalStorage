# NimoOS-LocalStorage 详解

NimoOS-LocalStorage 是负责本地磁盘和存储管理的微服务，提供磁盘发现、挂载管理、软件 RAID（mdadm）和 USB/新分区自动挂载等能力。

---

## 核心职责

- 磁盘与分区的发现和信息采集（lsblk + S.M.A.R.T. + hwmon 实时温度）
- 挂载点管理（创建、挂载、卸载，支持持久化到数据库和 fstab）
- 软件 RAID：基于 mdadm 的阵列创建/删除/状态/换盘/开机恢复（`/v2/raid`）
- USB 设备与新分区自动挂载（监听 udev 热插拔事件）
- 文件系统格式化（fdisk、mkfs）
- 远程/云存储挂载（通过 rclone，`/v1/cloud`）
- 向 MessageBus 发布磁盘事件（添加/移除/状态）
- 系统盘（含 overlayroot 场景）识别与容量统计

> MergerFS 合并卷的代码仍保留（`service/v2/merge.go`、`service/v2/fs/mergerfs.go`、`o_merge` 表），但 v2 API 已不再暴露 `/merge` 端点，`main.go` 中 `EnsureDefaultMergePoint` 被注释、配置默认 `EnableMergerFS=false`，当前处于休眠状态。

---

## 目录结构

```
NimoOS-LocalStorage/
├── main.go                  # 启动入口：RAID 开机恢复、路由注册、5s 定时状态上报
├── misc.go                  # udev 事件监听/转发、分区自动挂载触发、磁盘状态推送
├── api/local_storage/       # OpenAPI 规范（V2 挂载 API）
├── model/                   # V1 数据模型（磁盘、USB、S.M.A.R.T.、power_on_time）
├── service/
│   ├── disk.go              # DiskService（lsblk、SMART 缓存、自动挂载、InitCheck）
│   ├── storage.go           # rclone 远程存储
│   ├── usb.go               # USB 自动挂载配置
│   ├── model/
│   │   ├── o_volume.go      # Volume 模型（磁盘挂载记录，表名 o_disk）
│   │   ├── o_merge.go       # Merge 模型（合并卷，休眠）
│   │   ├── o_raid.go        # RAIDArray 模型（o_raid 表）
│   │   └── o_raid_member.go # RAIDMember 模型（成员盘持久标识）
│   └── v2/
│       ├── mount.go         # V2 挂载/卸载核心逻辑
│       ├── merge.go         # 合并卷管理（保留代码，未暴露 API）
│       ├── fstab.go         # fstab 持久化层
│       ├── fs/mergerfs.go   # MergerFS 文件系统扩展（PreMount 钩子）
│       ├── raid.go          # RAIDService：创建/删除/状态/换盘/开机恢复/重试
│       ├── raid_filesystem.go # ext4/btrfs 格式化与挂载（btrfs 子卷 @/@snapshots）
│       ├── raid_usage.go    # btrfs filesystem usage 查询（10 分钟缓存）
│       ├── raid_resize.go   # 文件系统扩容（resize2fs / btrfs resize）
│       └── raid_pathcheck.go # 删除前守卫：系统路径冲突、busy/子挂载诊断
├── route/
│   ├── v1/                  # V1 传统路由（磁盘、存储、USB、云存储）
│   ├── v2/                  # V2 OpenAPI 路由（挂载）+ RAID handler 与异步任务
│   └── raid.go              # /v2/raid 独立 echo 路由（JWT，非 OpenAPI 生成）
├── pkg/
│   ├── cache/               # 内存缓存（lsblk 100 秒、SMART 5 分钟）
│   ├── diskid/              # 磁盘持久标识（/dev/disk/by-id + 序列号回退）
│   ├── hwmon/               # 内核 hwmon 硬盘温度实时读取（drivetemp/nvme）
│   ├── mdadm/               # mdadm CLI 封装与 --detail、/proc/mdstat 解析
│   ├── mount/               # 系统挂载操作封装
│   ├── partition/           # 分区操作（fdisk、mkfs）
│   └── fstab/               # fstab 文件解析与修改
├── drivers/                 # 云存储驱动（dropbox、google_drive）
└── build/                   # systemd 服务文件、配置样例
```

---

## API 版本

### V1（传统，JWT 验证，`route/v1.go`）

**磁盘管理**
```
GET    /v1/disks                   列出所有磁盘（SMART + hwmon 温度 + power_on_time）
GET    /v1/disks/usb               列出 USB 设备
DELETE /v1/disks/usb               弹出 USB 设备
DELETE /v1/disks                   卸载磁盘
GET    /v1/disks/size              磁盘空间信息
```

**本地存储（分区）**
```
GET    /v1/storage                 列出磁盘/分区（含系统盘识别与容量归一化）
POST   /v1/storage                 添加存储（可选先重建分区并格式化）
PUT    /v1/storage                 格式化存储
DELETE /v1/storage                 移除存储（卸载）
```

**USB 自动挂载**
```
GET    /v1/usb/usb-auto-mount      查询自动挂载状态
PUT    /v1/usb/usb-auto-mount      切换自动挂载开关
```

**云存储（rclone）**
```
GET    /v1/cloud                   列出已挂载的远程存储
DELETE /v1/cloud                   移除远程存储
GET    /v1/driver                  列出云存储驱动信息
```

> 网关只注册 `/v1/usb`、`/v1/disks`、`/v1/storage` 与 v2 路径（见 `main.go` 的 `apiPaths`），`/v1/cloud`、`/v1/driver` 路由存在但未在网关注册。

### V2 挂载（OpenAPI 3.0，`api/local_storage/openapi.yaml`）

```
GET    /v2/local_storage/mount             列出所有挂载点
POST   /v2/local_storage/mount             创建新挂载
PUT    /v2/local_storage/mount/{point}     更新挂载
DELETE /v2/local_storage/mount             卸载
```

### V2 RAID（手写 echo 路由，`route/raid.go`）

```
GET    /v2/raid                    列出所有 RAID 阵列
POST   /v2/raid                    异步创建阵列（202 + task_id）
DELETE /v2/raid/:id                删除阵列（带多重守卫）
GET    /v2/raid/:id/status         实时状态（重建进度/完成时间/速度/容量）
GET    /v2/raid/:id/usage          容量用量（btrfs filesystem usage）
POST   /v2/raid/:id/disk           换盘（旧盘 → 新盘，触发重建）
POST   /v2/raid/:id/recover        手动触发重组装 + 挂载
GET    /v2/raid/tasks              列出创建任务
GET    /v2/raid/tasks/:task_id     查询创建任务进度
```

---

## 核心业务逻辑

### 磁盘信息采集

```
GET /v1/disks
  → lsblk --json（列表接口带 100 秒缓存，见 service/disk.go LSBLK）
  → 每块磁盘执行 smartctl（结果缓存 5 分钟；带 -n standby 不唤醒休眠盘）
  → hwmon（drivetemp/nvme）实时读 sysfs 温度覆盖 SMART 缓存值（pkg/hwmon）
  → 返回磁盘信息（含温度、通电小时数 power_on_time）
```

- SMART 缓存从 24 小时缩短为 5 分钟：温度在 SMART 数据里，必须保持新鲜（`service/disk.go` `SmartCTL`）。
- hwmon 是纯文件读取，即时、无需缓存、不会唤醒休眠盘（`route/v1/disk.go`）。

### 系统盘识别与容量统计（`route/v1/storage.go` GetStorageList）

- 根挂载点为 `/`、`/mnt/overlay` 或 `/media/root-ro`（overlayroot 场景）均识别为系统盘。
- 系统盘只输出一个根分区条目，`mount_point` 归一化为 `/`（前端按路径前缀匹配）。
- 容量取整盘物理大小；已用空间为各分区 `FSUsed` 之和（避免保留块重复计算）。

### 挂载流程（V2）

```
POST /v2/local_storage/mount
  → PreMount 钩子（filesystem 扩展点，service/v2/fs/）
  → 校验挂载点为空目录
  → 执行系统 mount 调用
  → 验证挂载成功
  → PostMount 钩子
  → 可选写入 fstab 持久化
```

### 软件 RAID（`service/v2/raid.go` + `pkg/mdadm`）

支持 RAID 0/1/5/6/10（最少 2/2/3/4/4 盘；RAID 10 要求偶数盘，奇数盘直接返回 400）。文件系统支持 ext4 / btrfs，默认 btrfs。

**异步创建**（`route/v2/raid.go` + `raid_task.go`）：

```
POST /v2/raid → 202 + task_id（同一时间只允许一个创建任务）
  后台 goroutine 按 5 步上报进度（GET /v2/raid/tasks/:task_id 轮询）：
  1. 加载内核模块（raid0/1/456/10）、校验名称与设备路径
  2. mdadm --zero-superblock 清除成员盘旧元数据
  3. mdadm --create /dev/mdN，等待设备出现（10s）
  4. wipefs 清除签名 → 格式化（btrfs 建 @ / @snapshots 子卷）
  5. 挂载到 /media/RAID_<name>，成员盘持久标识写库
```

- btrfs 挂载参数 `space_cache=v2,noatime,compress=zstd:1,subvol=@`，子卷缺失时回退 `subvolid=5`（`raid_filesystem.go`）。
- 成员盘用 `pkg/diskid` 记录持久标识（`/dev/disk/by-id` 优先，序列号回退），`/dev/sdX` 仅作缓存 —— 重启后盘符漂移不影响识别（`o_raid_member` 表）。

**状态查询**：合并 `mdadm --detail` 与 `/proc/mdstat` 解析（`pkg/mdadm/parse.go`），暴露重建百分比、预计完成时间、重建速度、成员盘状态与 df 容量；btrfs 用量走 `btrfs filesystem usage`，缓存 10 分钟（`raid_usage.go`）。

**开机恢复**（`RecoverOnBoot`，`main.go` 启动时调用）：

- `mdadm --assemble --scan` 后按 UUID 匹配 DB 记录并挂载（不依赖盘符）；
- 系统上存在但 DB 没有的阵列自动注册（读 detail、探测文件系统）；
- 未能组装的阵列进入重试 worker（30 秒 × 5 次，热插拔后自动恢复），全部失败标记 `failed`；`POST /v2/raid/:id/recover` 可随时手动触发并取消进行中的重试。

**换盘**（`ReplaceDisk`）：旧盘已物理拔出时跳过 `--fail --remove`，直接 `--add` 新盘、更新成员标识、状态置 `rebuilding`。

**删除守卫**（`DeleteRAIDArray` + `raid_pathcheck.go`）：

1. 阵列上仍有系统数据路径（Docker 镜像、AppData、数据库，读 `/var/lib/nimoos/path_config.json`）时拒绝删除；
2. 子挂载（如残留的 Docker/containerd overlay）按深度优先逐个卸载，不用 lazy umount（会掩盖 busy 状态导致后续 stop 失败），busy 时返回带诊断信息的错误；
3. 停阵列前先 `vgchange -an` 停用以该设备为 PV 的 LVM 卷组；
4. `mdadm --stop` 后对可解析到的成员盘 zero superblock，最后删 DB 记录。

**mdadm 二进制**：优先系统安装的 mdadm，支持 `MDADM_PATH` 环境变量覆盖，未安装时回退捆绑二进制（`pkg/mdadm/mdadm.go`）。

### USB / 新分区自动挂载（`misc.go` + `service/disk.go`）

- udev `partition add` 事件 → `AutoMountPartition`：未挂载且有 UUID 的分区自动挂到 `/mnt/Disk-<UUID前8位>` 并持久化到 DB，重启后 `CheckSerialDiskMount` 恢复。
- RAID 设备（`/dev/md*`）的 udev 事件不向 MessageBus 发布。

### 持久化策略（三层）

1. **内存**：运行时挂载信息
2. **数据库**：NimoOS 管理的挂载与阵列记录（`o_disk`、`o_raid`、`o_raid_member` 表）
3. **fstab**：系统级持久化（`/etc/fstab`，可选）

---

## 事件发布

每 5 秒上报存储状态（widget 数据按挂载点去重，避免 RAID 等重复计数），udev 事件实时转发：

| 事件 | 触发时机 |
|---|---|
| `local-storage:disk:added` | 磁盘/USB 插入 |
| `local-storage:disk:removed` | 磁盘/USB 拔出 |
| `local-storage:storage_status` | 每 5 秒定时上报 |

---

## 数据库模型

```sql
-- 磁盘挂载记录（模型 Volume，遗留表名）
CREATE TABLE o_disk (
    id INTEGER PRIMARY KEY,
    uuid TEXT,
    mount_point TEXT,
    created_at INTEGER
);

-- RAID 阵列
CREATE TABLE o_raid (
    id INTEGER PRIMARY KEY,
    name TEXT,
    level INTEGER,              -- 0/1/5/6/10
    filesystem TEXT,            -- ext4 / btrfs
    device_path TEXT,           -- /dev/mdN（缓存，重启可变）
    mount_point TEXT,           -- /media/RAID_<name>
    uuid TEXT UNIQUE,           -- mdadm 阵列 UUID（持久标识）
    state TEXT,                 -- active / degraded / rebuilding / failed
    chunk_kb INTEGER
);

-- RAID 成员盘（持久标识）
CREATE TABLE o_raid_member (
    id INTEGER PRIMARY KEY,
    raid_array_id INTEGER,
    disk_by_id TEXT,            -- /dev/disk/by-id 名称
    disk_serial TEXT,           -- 序列号回退
    device_path_cache TEXT      -- /dev/sdX 仅缓存
);

-- 合并卷（保留，功能休眠）
CREATE TABLE o_merge (...);      -- 及 o_merge_disk 多对多关联
```

---

## 配置

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

## 系统依赖

| 工具 | 用途 |
|---|---|
| lsblk / blkid | 块设备信息采集、文件系统探测 |
| smartctl | 磁盘健康监控（`-n standby` 不唤醒休眠盘） |
| hwmon (sysfs) | 硬盘温度实时读取（drivetemp / nvme 驱动） |
| mount/umount | 文件系统挂载操作 |
| fdisk/parted | 分区管理 |
| mkfs / wipefs | 文件系统格式化、签名清除 |
| mdadm | 软件 RAID（未安装时回退捆绑二进制） |
| btrfs-progs / resize2fs | btrfs 子卷与用量、文件系统扩容 |
| pvs / vgchange (LVM) | 删除 RAID 前停用关联卷组 |
| rclone | 远程存储挂载 |
| udev | 热插拔事件监听 |
| mergerfs | 虚拟合并卷（代码保留，当前未启用） |
