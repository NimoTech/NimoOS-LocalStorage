# CasaOS-LocalStorage 详解

CasaOS-LocalStorage 是负责本地磁盘和存储管理的微服务，提供磁盘发现、挂载管理、虚拟合并卷（MergerFS）和 USB 自动挂载等能力。

---

## 核心职责

- 磁盘与分区的发现和信息采集（lsblk + S.M.A.R.T.）
- 挂载点管理（创建、挂载、卸载，支持持久化到数据库和 fstab）
- 虚拟合并卷（MergerFS）：将多个磁盘合并为一个虚拟目录
- USB 设备自动挂载（监听 udev 热插拔事件）
- 文件系统格式化（fdisk、mkfs）
- 远程/云存储挂载（通过 rclone）
- 向 MessageBus 发布磁盘事件（添加/移除/状态）

---

## 目录结构

```
CasaOS-LocalStorage/
├── main.go                  # 启动入口，udev 事件监听，定时状态上报
├── misc.go                  # 事件发布辅助函数
├── api/                     # OpenAPI 规范（V2）
├── model/                   # V1 数据模型（磁盘、USB、S.M.A.R.T.）
├── service/
│   ├── disk.go              # DiskService（V1，lsblk 调用）
│   ├── storage.go           # rclone 远程存储
│   ├── usb.go               # USB 自动挂载配置
│   ├── model/
│   │   ├── o_volume.go      # Volume 数据库模型（磁盘挂载记录）
│   │   └── o_merge.go       # Merge 数据库模型（合并卷）
│   └── v2/
│       ├── mount.go         # V2 挂载/卸载核心逻辑
│       ├── merge.go         # V2 合并卷管理
│       ├── fstab.go         # fstab 持久化层
│       └── fs/mergerfs.go   # MergerFS 扩展实现
├── route/
│   ├── v1/                  # V1 传统路由（磁盘、USB、云存储）
│   └── v2/                  # V2 OpenAPI 路由（挂载、合并卷）
├── pkg/
│   ├── cache/               # 磁盘信息缓存（100 秒 TTL）
│   ├── mount/               # 系统挂载操作封装
│   ├── partition/           # 分区操作（fdisk、mkfs）
│   └── fstab/               # fstab 文件解析与修改
└── build/                   # systemd 服务文件
```

---

## API 版本

### V1（传统，JWT 验证）

**磁盘管理**
```
GET    /v1/disks                   列出所有磁盘（含 S.M.A.R.T. 数据）
GET    /v1/disks/usb               列出 USB 设备
DELETE /v1/disks/usb               弹出 USB 设备
DELETE /v1/disks                   卸载磁盘
GET    /v1/disks/size              磁盘空间信息
PUT    /v1/disks                   格式化磁盘
```

**USB 自动挂载**
```
GET    /v1/usb/usb-auto-mount      查询自动挂载状态
PUT    /v1/usb/usb-auto-mount      切换自动挂载开关
```

**远程存储**
```
GET    /v1/storage                 列出已挂载的远程存储
POST   /v1/storage                 添加远程存储
DELETE /v1/storage                 移除远程存储
```

### V2（OpenAPI 3.0）

**挂载管理**
```
GET    /v2/local_storage/mount             列出所有挂载点
POST   /v2/local_storage/mount            创建新挂载
PUT    /v2/local_storage/mount/{point}    更新挂载
DELETE /v2/local_storage/mount            卸载
```

**合并卷管理**
```
GET    /v2/local_storage/merge            列出合并卷
POST   /v2/local_storage/merge            创建/更新合并卷
DELETE /v2/local_storage/merge            删除合并卷
GET    /v2/local_storage/merge/init       查询初始化状态
```

---

## 核心业务逻辑

### 磁盘信息采集

```
GET /v1/disks
  → 检查缓存（100 秒 TTL）
  → 缓存未命中 → 执行 lsblk --json
  → 解析块设备树
  → 每块磁盘执行 smartctl（SMART 数据，24 小时缓存）
  → 返回丰富的磁盘信息
```

### 挂载流程（V2）

```
POST /v2/local_storage/mount
  → PreMount 钩子（filesystem 扩展点，如添加 mergerfs 参数）
  → 校验挂载点为空目录
  → 执行系统 mount 调用
  → 验证挂载成功
  → PostMount 钩子
  → 可选写入 fstab 持久化
```

### MergerFS 合并卷

将多个磁盘挂载点合并为一个虚拟目录，用户看到的是所有磁盘的合并视图：

```
/mnt/disk1 + /mnt/disk2 + /mnt/disk3 → /DATA/合并卷
```

- 使用 `category.create=mfs,moveonenospc=true,minfreespace=1M` 挂载选项
- 合并卷与 Volume 的多对多关系持久化到数据库
- 删除 Volume 时自动从所有关联的合并卷中移除

### 持久化策略（三层）

1. **内存**：运行时挂载信息
2. **数据库**：CasaOS 管理的挂载记录（`o_disk`、`o_merge` 表）
3. **fstab**：系统级持久化（`/etc/fstab`，可选）

---

## 事件发布

每 5 秒上报存储状态，udev 事件实时转发：

| 事件 | 触发时机 |
|---|---|
| `local-storage:disk:added` | 磁盘/USB 插入 |
| `local-storage:disk:removed` | 磁盘/USB 拔出 |
| `local-storage:storage_status` | 每 5 秒定时上报 |

---

## 数据库模型

```sql
-- 磁盘挂载记录
CREATE TABLE o_disk (
    id INTEGER PRIMARY KEY,
    uuid TEXT,
    mount_point TEXT,
    created_at INTEGER
);

-- 合并卷记录
CREATE TABLE o_merge (
    id INTEGER PRIMARY KEY,
    fstype TEXT,
    mount_point TEXT UNIQUE,
    source_base_path TEXT,
    created_at TIMESTAMP
);

-- 合并卷与磁盘的多对多关联
CREATE TABLE o_merge_disk (
    merge_id INTEGER,
    volume_id INTEGER
);
```

---

## 配置

```ini
[common]
RuntimePath = /var/run/casaos

[app]
LogPath = /var/log/casaos
DBPath = /var/lib/casaos/db
ShellPath = /usr/share/casaos/shell

[server]
USBAutoMount = True
EnableMergerFS = false
```

---

## 系统依赖

| 工具 | 用途 |
|---|---|
| lsblk | 块设备信息采集 |
| smartctl | 磁盘健康监控 |
| mount/umount | 文件系统挂载操作 |
| fdisk/parted | 分区管理 |
| mkfs | 文件系统格式化 |
| mergerfs | 虚拟合并卷 |
| rclone | 远程存储挂载 |
| udev | 热插拔事件监听 |
