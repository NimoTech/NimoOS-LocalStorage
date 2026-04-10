package v2

import (
	"net/http"
	"strconv"

	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/common_err"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

type CreateRAIDRequest struct {
	Name      string   `json:"name"`
	Level     int      `json:"level"`
	DiskPaths []string `json:"disk_paths"`
	ChunkKB   int      `json:"chunk_kb"`
}

type ReplaceDiskRequest struct {
	OldDiskPath string `json:"old_disk_path"`
	NewDiskPath string `json:"new_disk_path"`
}

// ListRAIDArrays handles GET /v2/raid
func ListRAIDArrays(ctx echo.Context) error {
	raids, err := service.MyService.RAID().ListRAIDArrays()
	if err != nil {
		logger.Error("error when listing RAID arrays", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: raids})
}

// CreateRAIDArray handles POST /v2/raid
func CreateRAIDArray(ctx echo.Context) error {
	var req CreateRAIDRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: err.Error()})
	}

	if len(req.Name) == 0 || len(req.DiskPaths) == 0 {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	validLevels := map[int]bool{0: true, 1: true, 5: true, 6: true}
	if !validLevels[req.Level] {
		return ctx.JSON(http.StatusBadRequest, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: "level must be 0, 1, 5, or 6",
		})
	}

	raid, err := service.MyService.RAID().CreateRAIDArray(req.Level, req.DiskPaths, req.Name, req.ChunkKB)
	if err != nil {
		logger.Error("error when creating RAID array", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: raid})
}

// DeleteRAIDArray handles DELETE /v2/raid/:id
func DeleteRAIDArray(ctx echo.Context) error {
	idStr := ctx.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	if err := service.MyService.RAID().DeleteRAIDArray(uint(id)); err != nil {
		logger.Error("error when deleting RAID array", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// GetRAIDStatus handles GET /v2/raid/:id/status
func GetRAIDStatus(ctx echo.Context) error {
	idStr := ctx.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	status, err := service.MyService.RAID().GetRAIDStatus(uint(id))
	if err != nil {
		logger.Error("error when getting RAID status", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: status})
}

// RecoverRAIDArray handles POST /v2/raid/:id/recover
func RecoverRAIDArray(ctx echo.Context) error {
	id, err := strconv.ParseUint(ctx.Param("id"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: common_err.GetMsg(common_err.INVALID_PARAMS),
		})
	}

	state, err := service.MyService.RAID().Recover(uint(id))
	if err != nil {
		logger.Error("error when recovering RAID array", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: err.Error(),
		})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    map[string]string{"state": state},
	})
}

// ReplaceDisk handles POST /v2/raid/:id/disk
func ReplaceDisk(ctx echo.Context) error {
	idStr := ctx.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	var req ReplaceDiskRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: err.Error()})
	}

	if len(req.OldDiskPath) == 0 || len(req.NewDiskPath) == 0 {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	if err := service.MyService.RAID().ReplaceDisk(uint(id), req.OldDiskPath, req.NewDiskPath); err != nil {
		logger.Error("error when replacing disk in RAID array", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}
