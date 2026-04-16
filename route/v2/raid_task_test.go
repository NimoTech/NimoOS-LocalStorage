package v2

import (
	"testing"
	"time"
)

func clearTaskStore() {
	taskStore.Range(func(k, _ any) bool {
		taskStore.Delete(k)
		return true
	})
}

func TestStoreAndLoadTask(t *testing.T) {
	clearTaskStore()
	task := CreateTask{
		TaskID:     "test-001",
		Status:     "creating",
		Step:       1,
		Progress:   0,
		StartTime:  time.Now(),
		Name:       "my-array",
		Level:      5,
		Filesystem: "ext4",
		DiskCount:  3,
	}
	storeTask(task)

	got, ok := loadTask("test-001")
	if !ok {
		t.Fatal("expected task to exist")
	}
	if got.Name != "my-array" {
		t.Errorf("got Name=%q, want %q", got.Name, "my-array")
	}
	if got.DiskCount != 3 {
		t.Errorf("got DiskCount=%d, want 3", got.DiskCount)
	}
}

func TestLoadTaskNotFound(t *testing.T) {
	clearTaskStore()
	_, ok := loadTask("nonexistent")
	if ok {
		t.Fatal("expected task to not exist")
	}
}

func TestStoreTaskUpdatesExisting(t *testing.T) {
	clearTaskStore()
	task := CreateTask{TaskID: "t1", Status: "creating", Step: 1, Progress: 0, StartTime: time.Now()}
	storeTask(task)

	storeTask(CreateTask{TaskID: "t1", Status: "creating", Step: 3, Progress: 15, StartTime: time.Now()})

	got, _ := loadTask("t1")
	if got.Step != 3 || got.Progress != 15 {
		t.Errorf("update not persisted: Step=%d Progress=%d", got.Step, got.Progress)
	}
}

func TestConflictCheck(t *testing.T) {
	clearTaskStore()
	storeTask(CreateTask{TaskID: "a", Status: "creating", StartTime: time.Now()})

	conflict := hasCreatingTask()
	if !conflict {
		t.Fatal("expected conflict detected")
	}

	// after done, no conflict
	storeTask(CreateTask{TaskID: "a", Status: "done", StartTime: time.Now()})
	if hasCreatingTask() {
		t.Fatal("expected no conflict after done")
	}
}
