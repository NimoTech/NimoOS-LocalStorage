package service

import (
	"context"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-LocalStorage/codegen/message_bus"
	"github.com/NimoTech/NimoOS-LocalStorage/common"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/config"
	"github.com/NimoTech/NimoOS-LocalStorage/service/snapshot"
	v2 "github.com/NimoTech/NimoOS-LocalStorage/service/v2"
	"github.com/NimoTech/NimoOS-LocalStorage/service/v2/wrapper"
	"github.com/patrickmn/go-cache"
	"gorm.io/gorm"
)

var Cache *cache.Cache

var MyService Services

type Services interface {
	Disk() DiskService
	USB() USBService
	LocalStorage() *v2.LocalStorageService
	Gateway() external.ManagementService
	Notify() NotifyServer
	NotifySystem() external.NotifyService
	Shares() external.ShareService
	MessageBus() *message_bus.ClientWithResponses
	RAID() v2.RAIDService
	Snapshot() *snapshot.Service
	// SnapshotScheduler returns the autonomous scheduler layer (handoff
	// §3.3/task-B3): a goroutine, started by main.go, that ticks every
	// minute to create/retire automatic snapshots per volume policy.
	SnapshotScheduler() *snapshot.Scheduler
}

func NewService(db *gorm.DB) Services {
	gatewayManagement, err := external.NewManagementService(config.CommonInfo.RuntimePath)
	if err != nil {
		panic(err)
	}
	return newService(db, gatewayManagement)
}

// NewInitService builds the service set for `nimoos-local-storage -init`, which
// nimoos-local-storage-first.service runs Before=docker.service -- long before
// the Gateway has written management.url. Nothing on that path talks to the
// Gateway (it only re-mounts previously persisted disks), so the management
// client is left nil instead of panicking on the missing address file, which
// is what failed the unit on every boot.
func NewInitService(db *gorm.DB) Services {
	return newService(db, nil)
}

func newService(db *gorm.DB, gatewayManagement external.ManagementService) Services {
	notifySystem := external.NewNotifyService(config.CommonInfo.RuntimePath)
	sharesService := external.NewShareService(config.CommonInfo.RuntimePath)
	raidService := v2.NewRAIDService(db)
	snapshotService := snapshot.NewService(db)

	s := &store{
		usb:          NewUSBService(),
		disk:         NewDiskService(db),
		localStorage: v2.NewLocalStorageService(db, wrapper.NewMountInfo()),
		gateway:      gatewayManagement,
		notify:       NewNotifyService(),
		notifySystem: notifySystem,
		shares:       sharesService,
		raid:         raidService,
		snapshot:     snapshotService,
	}

	// listSnapshotVolumes mirrors route/snapshot.go's currentVolumes(): a
	// RAID array is today's only "volume" this service knows about (see
	// snapshot.VolumesFromRAIDArrays' doc comment). The scheduler must
	// re-enumerate this fresh every tick (handoff §3.3: volumes are
	// hot-pluggable, so this is never cached).
	listSnapshotVolumes := func(_ context.Context) ([]snapshot.VolumeInfo, error) {
		raids, err := raidService.ListRAIDArrays()
		if err != nil {
			return nil, err
		}
		return snapshot.VolumesFromRAIDArrays(raids), nil
	}

	s.snapshotScheduler = snapshot.NewScheduler(snapshot.SchedulerConfig{
		Runner:      snapshotService.Runner,
		Store:       snapshotService.Store,
		Persister:   snapshotService.Persister,
		Pause:       snapshotService.Pause,
		ListVolumes: listSnapshotVolumes,
		Usage:       snapshot.NewRAIDUsageProvider(raidService),
		Publisher:   snapshot.NewMessageBusPublisher(s.MessageBus, common.ServiceName),
	})

	return s
}

type store struct {
	usb               USBService
	disk              DiskService
	localStorage      *v2.LocalStorageService
	gateway           external.ManagementService
	notify            NotifyServer
	notifySystem      external.NotifyService
	shares            external.ShareService
	raid              v2.RAIDService
	snapshot          *snapshot.Service
	snapshotScheduler *snapshot.Scheduler
}

func (c *store) NotifySystem() external.NotifyService {
	return c.notifySystem
}

func (c *store) Gateway() external.ManagementService {
	return c.gateway
}

func (c *store) USB() USBService {
	return c.usb
}

func (c *store) Disk() DiskService {
	return c.disk
}

func (c *store) LocalStorage() *v2.LocalStorageService {
	return c.localStorage
}

func (c *store) Notify() NotifyServer {
	return c.notify
}

func (c *store) Shares() external.ShareService {
	return c.shares
}

func (c *store) RAID() v2.RAIDService {
	return c.raid
}

func (c *store) Snapshot() *snapshot.Service {
	return c.snapshot
}

func (c *store) SnapshotScheduler() *snapshot.Scheduler {
	return c.snapshotScheduler
}

func (c *store) MessageBus() *message_bus.ClientWithResponses {
	client, _ := message_bus.NewClientWithResponses("", func(c *message_bus.Client) error {
		// error will never be returned, as we always want to return a client, even with wrong address,
		// in order to avoid panic.
		//
		// If we don't avoid panic, message bus becomes a hard dependency, which is not what we want.

		messageBusAddress, err := external.GetMessageBusAddress(config.CommonInfo.RuntimePath)
		if err != nil {
			c.Server = "message bus address not found"
			return nil
		}

		c.Server = messageBusAddress
		return nil
	})

	return client
}
