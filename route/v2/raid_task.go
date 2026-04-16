package v2

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// CreateTask 存储一次 RAID 创建任务的状态。
// sync.Map 存值拷贝（非指针），每次更新用 Store 整体替换，避免 Data Race。
type CreateTask struct {
	TaskID    string
	Status    string // "creating" | "done" | "failed"
	Step      int    // 0=未开始, 1–6=当前步骤
	Progress  int    // 0–100
	StartTime time.Time
	RaidID    *uint
	Error     string
	// 业务上下文：刷新页面后前端重建 UI 所需
	Name       string
	Level      int
	Filesystem string
	DiskCount  int
}

var (
	taskStore  sync.Map
	createLock sync.Mutex // 保护"查重 + 写入"的原子性
)

func generateTaskID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func storeTask(t CreateTask) {
	taskStore.Store(t.TaskID, t)
}

func loadTask(id string) (CreateTask, bool) {
	v, ok := taskStore.Load(id)
	if !ok {
		return CreateTask{}, false
	}
	return v.(CreateTask), true
}

// hasCreatingTask 检查是否存在进行中的任务（在 createLock 持有期间调用）。
func hasCreatingTask() bool {
	found := false
	taskStore.Range(func(_, v any) bool {
		if v.(CreateTask).Status == "creating" {
			found = true
			return false
		}
		return true
	})
	return found
}

// scheduleTaskCleanup 在任务完成 5 分钟后自动从内存中删除。
func scheduleTaskCleanup(taskID string) {
	go func() {
		time.Sleep(5 * time.Minute)
		taskStore.Delete(taskID)
	}()
}
