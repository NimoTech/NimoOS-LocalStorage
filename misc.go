package main

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/common"
	"github.com/NimoTech/NimoOS-LocalStorage/model"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	"github.com/pilebones/go-udev/netlink"
	"go.uber.org/zap"
)

func sendDiskBySocket() {
	blkList := service.MyService.Disk().LSBLK(true)

	status := model.DiskStatus{}
	healthy := true
	seenMounts := make(map[string]struct{})

	for _, currentDisk := range blkList {
		if !service.IsDiskSupported(currentDisk) {
			continue
		}
		temp := service.MyService.Disk().SmartCTL(currentDisk.Path)
		if reflect.DeepEqual(temp, model.SmartctlA{}) {
			healthy = true
		} else {
		if len(temp.ModelName) > 0 {
				healthy = temp.SmartStatus.Passed
			} else {
				healthy = true
			}
		}
		
		if len(currentDisk.Children) > 0 {
			for _, v := range currentDisk.Children {
				if len(v.MountPoint) == 0 {
					continue
				}
				if _, seen := seenMounts[v.MountPoint]; seen {
					continue
				}
				seenMounts[v.MountPoint] = struct{}{}
				s, _ := strconv.ParseUint(v.FSSize.String(), 10, 64)
				a, _ := strconv.ParseUint(v.FSAvail.String(), 10, 64)
				u, _ := strconv.ParseUint(v.FSUsed.String(), 10, 64)
				
				if s > a {
					u = s - a
				}
				
				status.Size += s
				status.Avail += a
				status.Used += u
			}
		} else {
			if len(currentDisk.MountPoint) == 0 {
				continue
			}
			if _, seen := seenMounts[currentDisk.MountPoint]; seen {
				continue
			}
			seenMounts[currentDisk.MountPoint] = struct{}{}
			s, _ := strconv.ParseUint(currentDisk.FSSize.String(), 10, 64)
			a, _ := strconv.ParseUint(currentDisk.FSAvail.String(), 10, 64)
			u, _ := strconv.ParseUint(currentDisk.FSUsed.String(), 10, 64)
			
			if s > a {
				u = s - a
			}
			
			status.Size += s
			status.Avail += a
			status.Used += u
		}
	}

	status.Health = healthy
	message := make(map[string]interface{})
	message["sys_disk"] = status
	if err := service.MyService.NotifySystem().SendSystemStatusNotify(message); err != nil {
		logger.Error("failed to send notify", zap.Any("message", message), zap.Error(err))
	}
}

func sendUSBBySocket() {
	message := map[string]interface{}{
		"sys_usb": service.MyService.Disk().GetUSBDriveStatusList(),
	}

	if err := service.MyService.NotifySystem().SendSystemStatusNotify(message); err != nil {
		logger.Error("failed to send notify", zap.Any("message", message), zap.Error(err))
	}
}

func monitorUEvent(ctx context.Context) {
	var matcher netlink.Matcher

	conn := new(netlink.UEventConn)
	if err := conn.Connect(netlink.UdevEvent); err != nil {
		logger.Error("udev err", zap.Any("Unable to connect to Netlink Kobject UEvent socket", err))
	}
	defer conn.Close()

	queue := make(chan netlink.UEvent)
	defer close(queue)

	errors := make(chan error)
	defer close(errors)

	quit := conn.Monitor(queue, errors, matcher)
	defer close(quit)

	for {
		select {

		case <-ctx.Done():
			return

		case uevent := <-queue:

			if event := common.EventAdapter(uevent); event != nil {

				// add UI properties to applicable events so that NimoOS UI can render it
				event := common.EventAdapterWithUIProperties(event)

				shouldPublish := true

				if v, ok := event.Properties["local-storage:path"]; ok && strings.Contains(event.Name, "disk") {

					diskModel := service.MyService.Disk().GetDiskInfo(v)
					if !reflect.DeepEqual(diskModel, model.LSBLKModel{}) {

						// Only exclude RAID devices (like /dev/md*) instead of all virtual devices
						if strings.HasPrefix(diskModel.Path, "/dev/md") || strings.HasPrefix(diskModel.Type, "raid") {
							shouldPublish = false
						} else {
							properties := common.AdditionalProperties(diskModel)
							for k, val := range properties {
								event.Properties[k] = val
							}
						}
					}
				}

				if shouldPublish {
					logger.Info("disk model", zap.Any("diskModel", event.Name))
					response, err := service.MyService.MessageBus().PublishEventWithResponse(ctx, event.SourceID, event.Name, event.Properties)
					if err != nil {
						logger.Error("failed to publish event to message bus", zap.Error(err), zap.Any("event", event))
					}

					if response != nil && response.StatusCode() != http.StatusOK {
						logger.Error("failed to publish event to message bus", zap.String("status", response.Status()), zap.Any("response", response))
					}
				}
			}

			switch uevent.Env["DEVTYPE"] {
			case "partition":
				if uevent.Action == "add" {
					go service.MyService.Disk().AutoMountPartition(uevent.Env["DEVNAME"])
				}

				switch uevent.Env["ID_BUS"] {
				case "usb":
					time.Sleep(1 * time.Second)
					sendUSBBySocket()
					continue
				}
			}

		case err := <-errors:
			logger.Error("udev err", zap.Error(err))
		}
	}
}

func sendStorageStats() {
	sendDiskBySocket()
	sendUSBBySocket()
}
