package service

import (
	"encoding/json"
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/model"
	"gotest.tools/v3/assert"
)

func TestWalkDisk(t *testing.T) {
	jsonText := `{"name":"sda","kname":"sda","path":"/dev/sda","maj:min":"8:0","fsavail":null,"fssize":null,"fstype":null,"fsused":null,"fsuse%":null,"fsroots":[null],"fsver":null,"mountpoint":null,"mountpoints":[null],"label":null,"uuid":null,"ptuuid":"09f61536-3032-4f7c-915e-a4d78c07da51","pttype":"gpt","parttype":null,"parttypename":null,"partlabel":null,"partuuid":null,"partflags":null,"ra":128,"ro":false,"rm":false,"hotplug":false,"model":"QEMU HARDDISK","serial":"drive-scsi0","size":12884901888,"state":"running","owner":"root","group":"disk","mode":"brw-rw----","alignment":0,"min-io":512,"opt-io":0,"phy-sec":512,"log-sec":512,"rota":true,"sched":"mq-deadline","rq-size":256,"type":"disk","disc-aln":0,"disc-gran":4096,"disc-max":1073741824,"disc-zero":false,"wsame":2147483136,"wwn":null,"rand":true,"pkname":null,"hctl":"2:0:0:0","tran":null,"subsystems":"block:scsi:virtio:pci","rev":"2.5+","vendor":"QEMU    ","zoned":"none","dax":false,"children":[{"name":"sda1","kname":"sda1","path":"/dev/sda1","maj:min":"8:1","fsavail":null,"fssize":null,"fstype":null,"fsused":null,"fsuse%":null,"fsroots":[null],"fsver":null,"mountpoint":null,"mountpoints":[null],"label":null,"uuid":null,"ptuuid":"09f61536-3032-4f7c-915e-a4d78c07da51","pttype":"gpt","parttype":"21686148-6449-6e6f-744e-656564454649","parttypename":"BIOS boot","partlabel":null,"partuuid":"6114973f-adb1-468e-9feb-b49c762ae245","partflags":null,"ra":128,"ro":false,"rm":false,"hotplug":false,"model":null,"serial":null,"size":1048576,"state":null,"owner":"root","group":"disk","mode":"brw-rw----","alignment":0,"min-io":512,"opt-io":0,"phy-sec":512,"log-sec":512,"rota":true,"sched":"mq-deadline","rq-size":256,"type":"part","disc-aln":0,"disc-gran":4096,"disc-max":1073741824,"disc-zero":false,"wsame":2147483136,"wwn":null,"rand":true,"pkname":"sda","hctl":null,"tran":null,"subsystems":"block:scsi:virtio:pci","rev":null,"vendor":null,"zoned":"none","dax":false},{"name":"sda2","kname":"sda2","path":"/dev/sda2","maj:min":"8:2","fsavail":"1441812480","fssize":"1810489344","fstype":"ext4","fsused":"257949696","fsuse%":"14%","fsroots":["/"],"fsver":"1.0","mountpoint":"/boot","mountpoints":["/boot"],"label":null,"uuid":"78db4224-e926-42c4-a899-8f8f00224d22","ptuuid":"09f61536-3032-4f7c-915e-a4d78c07da51","pttype":"gpt","parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4","parttypename":"Linux filesystem","partlabel":null,"partuuid":"a11c27be-8929-49e4-b30a-99dc6bfe2e67","partflags":null,"ra":128,"ro":false,"rm":false,"hotplug":false,"model":null,"serial":null,"size":1879048192,"state":null,"owner":"root","group":"disk","mode":"brw-rw----","alignment":0,"min-io":512,"opt-io":0,"phy-sec":512,"log-sec":512,"rota":true,"sched":"mq-deadline","rq-size":256,"type":"part","disc-aln":0,"disc-gran":4096,"disc-max":1073741824,"disc-zero":false,"wsame":2147483136,"wwn":null,"rand":true,"pkname":"sda","hctl":null,"tran":null,"subsystems":"block:scsi:virtio:pci","rev":null,"vendor":null,"zoned":"none","dax":false},{"name":"sda3","kname":"sda3","path":"/dev/sda3","maj:min":"8:3","fsavail":null,"fssize":null,"fstype":"LVM2_member","fsused":null,"fsuse%":null,"fsroots":[null],"fsver":"LVM2 001","mountpoint":null,"mountpoints":[null],"label":null,"uuid":"O7mLM9-q5mc-qNNe-hemy-6giu-Ocvg-V1MMuZ","ptuuid":"09f61536-3032-4f7c-915e-a4d78c07da51","pttype":"gpt","parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4","parttypename":"Linux filesystem","partlabel":null,"partuuid":"5a9bd7c3-eab1-4511-baf2-7e816d5042a5","partflags":null,"ra":128,"ro":false,"rm":false,"hotplug":false,"model":null,"serial":null,"size":11002707968,"state":null,"owner":"root","group":"disk","mode":"brw-rw----","alignment":0,"min-io":512,"opt-io":0,"phy-sec":512,"log-sec":512,"rota":true,"sched":"mq-deadline","rq-size":256,"type":"part","disc-aln":0,"disc-gran":4096,"disc-max":1073741824,"disc-zero":false,"wsame":2147483136,"wwn":null,"rand":true,"pkname":"sda","hctl":null,"tran":null,"subsystems":"block:scsi:virtio:pci","rev":null,"vendor":null,"zoned":"none","dax":false,"children":[{"name":"ubuntu--vg-ubuntu--lv","kname":"dm-0","path":"/dev/mapper/ubuntu--vg-ubuntu--lv","maj:min":"253:0","fsavail":"2965307392","fssize":"10464022528","fstype":"ext4","fsused":"6945067008","fsuse%":"66%","fsroots":["/"],"fsver":"1.0","mountpoint":"/","mountpoints":["/"],"label":null,"uuid":"19dae839-3805-4240-a05d-288e903719d6","ptuuid":null,"pttype":null,"parttype":null,"parttypename":null,"partlabel":null,"partuuid":null,"partflags":null,"ra":128,"ro":false,"rm":false,"hotplug":false,"model":null,"serial":null,"size":10737418240,"state":"running","owner":"root","group":"disk","mode":"brw-rw----","alignment":0,"min-io":512,"opt-io":0,"phy-sec":512,"log-sec":512,"rota":true,"sched":null,"rq-size":128,"type":"lvm","disc-aln":0,"disc-gran":4096,"disc-max":1073741824,"disc-zero":false,"wsame":2147483136,"wwn":null,"rand":false,"pkname":"sda3","hctl":null,"tran":null,"subsystems":"block","rev":null,"vendor":null,"zoned":"none","dax":false}]}]}`

	blk := model.LSBLKModel{}

	err := json.Unmarshal([]byte(jsonText), &blk)

	assert.NilError(t, err)

	sysBlk := WalkDisk(blk, 5, func(blk model.LSBLKModel) bool { return blk.MountPoint == "/" })

	assert.Equal(t, sysBlk.Name, "ubuntu--vg-ubuntu--lv")

	sysBlk = WalkDisk(blk.Children[2], 5, func(blk model.LSBLKModel) bool { return blk.MountPoint == "/" })

	assert.Equal(t, sysBlk.Name, "ubuntu--vg-ubuntu--lv")
}

func TestParseBlockDevices(t *testing.T) {
	jsonText := `{"blockdevices":[{"alignment":3072,"disc-aln":3072,"dax":false,"disc-gran":4096,"disc-max":2147450880,"disc-zero":false,"fsavail":965102444544,"fsroots":["/"],"fssize":983351103488,"fstype":"ext4","fsused":8229834752,"fsuse%":"1%","fsver":"1.0","group":"disk","hctl":null,"hotplug":false,"kname":"sda1","label":null,"log-sec":512,"maj:min":"8:1","min-io":4096,"mode":"brw-rw----","model":null,"name":"sda1","opt-io":0,"owner":"root","partflags":null,"partlabel":"primary","parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4","parttypename":"Linux filesystem","partuuid":"7c216c4e-19aa-4090-9cf5-f581e061316f","path":"/dev/sda1","phy-sec":4096,"pkname":"sda","pttype":"gpt","ptuuid":"d6e75e46-4baf-4581-8123-0bb46d516a3d","ra":128,"rand":false,"rev":null,"rm":false,"ro":false,"rota":false,"rq-size":64,"sched":"mq-deadline","serial":null,"size":1000204851712,"start":34,"state":null,"subsystems":"block:scsi:pci","mountpoint":"/DATA/Storage_1","mountpoints":["/DATA/Storage_1"],"tran":null,"type":"part","uuid":"dec3bf0a-bf21-4201-92d8-6ecdd4fa1ea8","vendor":null,"wsame":0,"wwn":"0x500a0751e602c4cc","zoned":"none","zone-sz":0,"zone-wgran":0,"zone-app":0,"zone-nr":0,"zone-omax":0,"zone-amax":0}]}`

	blkList, err := ParseBlockDevices([]byte(jsonText))

	assert.NilError(t, err)

	assert.Equal(t, len(blkList), 1)

	assert.Equal(t, blkList[0].FSSize.String(), "983351103488")
	assert.Equal(t, blkList[0].FSAvail.String(), "965102444544")
	assert.Equal(t, blkList[0].FSUsed.String(), "8229834752")
}

// TestParseBlockDevices_MountPointsPlural is the direct repro of the root
// cause behind the missing-RAID-volume bug: lsblk's singular "mountpoint"
// field only reports a device's LAST mount, but when a btrfs volume has
// both its @ subvolume and @snapshots subvolume mounted, the plural
// "mountpoints" array carries every mount. Without parsing it, callers have
// no way to recover the real (non-.snapshots) mountpoint.
func TestParseBlockDevices_MountPointsPlural(t *testing.T) {
	jsonText := `{"blockdevices":[{"name":"md0","path":"/dev/md0","mountpoint":"/media/RAID_0/.snapshots","mountpoints":[null,"/media/RAID_0"]}]}`

	blkList, err := ParseBlockDevices([]byte(jsonText))

	assert.NilError(t, err)
	assert.Equal(t, len(blkList), 1)

	assert.Equal(t, blkList[0].MountPoint, "/media/RAID_0/.snapshots")
	assert.Equal(t, len(blkList[0].MountPoints), 2)
	assert.Equal(t, blkList[0].MountPoints[0], "")
	assert.Equal(t, blkList[0].MountPoints[1], "/media/RAID_0")
}

// IsDiskSupported 是「什么块设备可以被当成用户存储盘(格式化 / 建 RAID)」的
// 安全边界,此前一行覆盖都没有 —— 它放行的设备下游会被 mdadm 清扫。这里钉住
// 白名单的形状:只认列出的传输方式与子系统链,其余一律拒绝。
//
// 尤其钉住 scsi_debug(`block:scsi:pseudo`)被拒:那是内存里的伪盘,重启即消失,
// 当成真存储盘用会让用户建了 RAID 存了数据一重启全没。2026-07-28 的多盘测试台
// 为了造假盘曾临时放行它,当时就明确该改动只能是**测试脚手架、不进产品**;
// 这条断言是那个决定的守卫 —— 若将来有人把 pseudo 加进白名单,此测试即红。
func TestIsDiskSupported(t *testing.T) {
	cases := []struct {
		name       string
		tran       string
		subSystems string
		want       bool
	}{
		{"nvme 真盘", "nvme", "block:nvme:pci", true},
		{"sata 真盘", "sata", "block:scsi:pci", true},
		{"usb 盘", "usb", "block:scsi:usb:pci", true},
		{"virtio 虚拟盘", "", "block:scsi:virtio:pci", true},
		{"Hyper-V", "", "block:scsi:vmbus:acpi", true},
		{"eMMC(pci)", "", "block:mmc:mmc_host:pci", true},
		{"scsi_debug 内存伪盘 —— 必须拒绝", "", "block:scsi:pseudo", false},
		{"zram 之类裸 block", "", "block", false},
		{"未知传输方式", "fibre", "block:unknown", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsDiskSupported(model.LSBLKModel{Tran: c.tran, SubSystems: c.subSystems})
			assert.Equal(t, got, c.want)
		})
	}
}
