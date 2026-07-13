package service

import (
	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-LocalStorage/codegen/message_bus"
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
	Storage() StorageService
	RAID() v2.RAIDService
	Snapshot() *snapshot.Service
}

func NewService(db *gorm.DB) Services {
	gatewayManagement, err := external.NewManagementService(config.CommonInfo.RuntimePath)
	if err != nil {
		panic(err)
	}

	notifySystem := external.NewNotifyService(config.CommonInfo.RuntimePath)
	sharesService := external.NewShareService(config.CommonInfo.RuntimePath)
	raidService := v2.NewRAIDService(db)
	snapshotService := snapshot.NewService(db)

	return &store{
		usb:          NewUSBService(),
		disk:         NewDiskService(db),
		localStorage: v2.NewLocalStorageService(db, wrapper.NewMountInfo()),
		gateway:      gatewayManagement,
		notify:       NewNotifyService(),
		notifySystem: notifySystem,
		shares:       sharesService,
		storage:      NewStorageService(),
		raid:         raidService,
		snapshot:     snapshotService,
	}
}

type store struct {
	usb          USBService
	disk         DiskService
	localStorage *v2.LocalStorageService
	gateway      external.ManagementService
	notify       NotifyServer
	notifySystem external.NotifyService
	shares       external.ShareService
	storage      StorageService
	raid         v2.RAIDService
	snapshot     *snapshot.Service
}

func (c *store) NotifySystem() external.NotifyService {
	return c.notifySystem
}

func (c *store) Storage() StorageService {
	return c.storage
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
