package v2

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/common_err"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

type CreateRAIDRequest struct {
	Name       string   `json:"name"`
	Level      int      `json:"level"`
	DiskPaths  []string `json:"disk_paths"`
	ChunkKB    int      `json:"chunk_kb"`
	Filesystem string   `json:"filesystem"`
	// EnableSnapshots optionally auto-enables btrfs snapshot protection for
	// the newly created array once it's formatted and mounted (task B5).
	// nil (the field omitted, e.g. by an older client) and true both enable
	// it — this is the default; only an explicit false skips it. Best
	// effort: failure to enable never fails RAID creation itself, see
	// enableSnapshotsForNewRAID.
	EnableSnapshots *bool `json:"enable_snapshots"`
	// WipeRaidResidue confirms erasing foreign arrays' leftover superblocks
	// on the chosen disks (see ReplaceDiskRequest.WipeRaidResidue).
	WipeRaidResidue bool `json:"wipe_raid_residue"`
}

type ReplaceDiskRequest struct {
	// OldDiskPath alone is unreliable after a hot swap (device letters get
	// reused); clients that know the pulled disk's serial send it instead.
	OldDiskPath   string `json:"old_disk_path"`
	OldDiskSerial string `json:"old_disk_serial"`
	NewDiskPath   string `json:"new_disk_path"`
	// WipeRaidResidue confirms erasing a foreign array's leftover superblock
	// on the new disk. The UI sets it after its confirmation dialog; without
	// it the backend refuses residue-carrying disks.
	WipeRaidResidue bool `json:"wipe_raid_residue"`
}

// createStepNames maps step numbers to UI display text.
// Defined here (route layer) so the service layer stays UI-agnostic.
//
// Each value is verbatim an i18n key that NimoOS-UI already ships translations
// for (see its src/assets/lang/*.json), so the panel can render
// $t(step_name) and get the user's language. Changing the wording here without
// changing the key there would fall back to showing this English text.
var createStepNames = map[int]string{
	0: "",
	1: "Load kernel modules",
	2: "Clean disk superblocks",
	3: "Create RAID Array",
	4: "Initialize filesystem",
	5: "Mount array",
	6: "Save configuration",
}

// createStepProgress is the cumulative progress percentage when a step begins
// (i.e. after all prior steps have completed).
var createStepProgress = map[int]int{
	1: 0, 2: 5, 3: 15, 4: 30, 5: 85, 6: 95,
}

func buildTaskResponse(t CreateTask) map[string]any {
	stepName := createStepNames[t.Step]
	if t.Status == "done" {
		stepName = "Done"
	}
	return map[string]any{
		"task_id":         t.TaskID,
		"status":          t.Status,
		"step":            t.Step,
		"step_name":       stepName,
		"progress":        t.Progress,
		"elapsed_seconds": int(time.Since(t.StartTime).Seconds()),
		"raid_id":         t.RaidID,
		"error":           t.Error,
		"name":            t.Name,
		"level":           t.Level,
		"filesystem":      t.Filesystem,
		"disk_count":      t.DiskCount,
	}
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
// Returns 202 immediately with a task_id; creation runs in a goroutine.
func CreateRAIDArray(ctx echo.Context) error {
	var req CreateRAIDRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: err.Error()})
	}

	if len(req.Name) == 0 || len(req.DiskPaths) == 0 {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	validLevels := map[int]bool{0: true, 1: true, 5: true, 6: true, 10: true}
	if !validLevels[req.Level] {
		return ctx.JSON(http.StatusBadRequest, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: "level must be 0, 1, 5, 6, or 10",
		})
	}

	req.Filesystem = strings.TrimSpace(strings.ToLower(req.Filesystem))
	if req.Filesystem != "" {
		validFilesystems := map[string]bool{"ext4": true, "btrfs": true}
		if !validFilesystems[req.Filesystem] {
			return ctx.JSON(http.StatusBadRequest, model.Result{
				Success: common_err.INVALID_PARAMS,
				Message: "filesystem must be ext4 or btrfs",
			})
		}
	}

	// Normalise empty filesystem to default.
	fs := req.Filesystem
	if fs == "" {
		fs = "btrfs"
	}

	if req.Level == 10 && len(req.DiskPaths)%2 != 0 {
		return ctx.JSON(http.StatusBadRequest, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: "RAID 10 requires an even number of disks",
		})
	}

	// Atomic check-and-store: createLock ensures no TOCTOU race between
	// checking for an existing task and writing the new one.
	taskID := generateTaskID()
	createLock.Lock()
	if hasCreatingTask() {
		createLock.Unlock()
		return ctx.JSON(http.StatusConflict, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: "another array is being created",
		})
	}
	storeTask(CreateTask{
		TaskID:     taskID,
		Status:     "creating",
		StartTime:  time.Now(),
		Name:       req.Name,
		Level:      req.Level,
		Filesystem: fs,
		DiskCount:  len(req.DiskPaths),
	})
	createLock.Unlock()

	onStep := func(step int) {
		t, ok := loadTask(taskID)
		if !ok {
			return
		}
		t.Step = step
		t.Progress = createStepProgress[step]
		storeTask(t)
	}

	go func() {
		result, err := service.MyService.RAID().CreateRAIDArray(
			req.Level, req.DiskPaths, req.Name, req.ChunkKB, fs, req.WipeRaidResidue, onStep,
		)
		t, ok := loadTask(taskID)
		if !ok {
			return
		}
		if err != nil {
			logger.Error("async RAID create failed", zap.String("task_id", taskID), zap.Error(err))
			t.Status = "failed"
			t.Error = err.Error()
		} else {
			t.Status = "done"
			t.Progress = 100
			t.RaidID = &result.ID
			enableSnapshotsForNewRAID(req.EnableSnapshots, result)
		}
		storeTask(t)
		scheduleTaskCleanup(taskID)
	}()

	return ctx.JSON(http.StatusAccepted, map[string]string{
		"task_id": taskID,
		"status":  "creating",
	})
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

// GetRAIDUsage handles GET /v2/raid/:id/usage
func GetRAIDUsage(ctx echo.Context) error {
	idStr := ctx.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	usage, err := service.MyService.RAID().GetRAIDUsage(uint(id))
	if err != nil {
		logger.Error("error when getting RAID usage", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: usage})
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

	state, readded, err := service.MyService.RAID().Recover(uint(id))
	if err != nil {
		logger.Error("error when recovering RAID array", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: err.Error(),
		})
	}
	if readded == nil {
		readded = []string{}
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    map[string]any{"state": state, "readded": readded},
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

	if len(req.NewDiskPath) == 0 || (len(req.OldDiskPath) == 0 && len(req.OldDiskSerial) == 0) {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	if err := service.MyService.RAID().ReplaceDisk(uint(id), req.OldDiskPath, req.OldDiskSerial, req.NewDiskPath, req.WipeRaidResidue); err != nil {
		logger.Error("error when replacing disk in RAID array", zap.Error(err), zap.Uint64("id", id))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// ListCreateTasks handles GET /v2/raid/tasks
func ListCreateTasks(ctx echo.Context) error {
	var tasks []map[string]any
	taskStore.Range(func(_, v any) bool {
		tasks = append(tasks, buildTaskResponse(v.(CreateTask)))
		return true
	})
	if tasks == nil {
		tasks = []map[string]any{}
	}
	return ctx.JSON(http.StatusOK, tasks)
}

// GetCreateTask handles GET /v2/raid/tasks/:task_id
func GetCreateTask(ctx echo.Context) error {
	taskID := ctx.Param("task_id")
	t, ok := loadTask(taskID)
	if !ok {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
	}
	return ctx.JSON(http.StatusOK, buildTaskResponse(t))
}
