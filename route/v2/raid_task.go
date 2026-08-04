package v2

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// CreateTask stores the state of a single RAID creation task.
// sync.Map stores a value copy (not a pointer); every update replaces the
// whole entry via Store to avoid a data race.
type CreateTask struct {
	TaskID    string
	Status    string // "creating" | "done" | "failed"
	Step      int    // 0=not started, 1-6=current step
	Progress  int    // 0-100
	StartTime time.Time
	RaidID    *uint
	Error     string
	// Business context: needed by the frontend to rebuild the UI after a page refresh
	Name       string
	Level      int
	Filesystem string
	DiskCount  int
}

var (
	taskStore  sync.Map
	createLock sync.Mutex // guards the atomicity of "check for duplicate + write"
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

// hasCreatingTask checks whether a task is currently in progress (called while holding createLock).
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

// scheduleTaskCleanup automatically removes the task from memory 5 minutes after it completes.
func scheduleTaskCleanup(taskID string) {
	go func() {
		time.Sleep(5 * time.Minute)
		taskStore.Delete(taskID)
	}()
}
