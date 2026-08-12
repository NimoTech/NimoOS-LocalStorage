package v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	svcmodel "github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"github.com/NimoTech/NimoOS-LocalStorage/service/snapshot"
	v2 "github.com/NimoTech/NimoOS-LocalStorage/service/v2"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap/zapcore"
)

// The best-effort-failure test below exercises enableSnapshotsForNewRAID's
// logger.Error call; the package-global zap logger otherwise stays nil
// (never initialized outside main.go) and panics on first use, same reason
// service/snapshot/mount_test.go does this.
func init() {
	logger.LogInitConsoleOnly()
}

// Task B5 (feat/btrfs-snapshots): RAID creation optionally auto-enables
// snapshot protection, default on. These tests drive the real
// CreateRAIDArray HTTP handler with a stubbed RAIDService (so no real
// mdadm/mkfs/mount calls happen) and a real *snapshot.Service backed by
// package snapshot's fakes, to prove the wiring end to end: the
// enable_snapshots binding, the enable path (policy persisted +
// @snapshots mounted), and the best-effort failure rule.

// fakeSnapshotRAIDService implements v2.RAIDService, returning a canned
// (result, err) pair from CreateRAIDArray. Every other method panics: no
// test in this file should reach them.
type fakeSnapshotRAIDService struct {
	result *svcmodel.RAIDArray
	err    error
}

var _ v2.RAIDService = (*fakeSnapshotRAIDService)(nil)

func (f *fakeSnapshotRAIDService) CreateRAIDArray(int, []string, string, int, string, bool, func(int)) (*svcmodel.RAIDArray, error) {
	return f.result, f.err
}
func (f *fakeSnapshotRAIDService) DeleteRAIDArray(uint) error { panic("not implemented") }
func (f *fakeSnapshotRAIDService) GetRAIDStatus(uint) (*v2.RAIDStatus, error) {
	panic("not implemented")
}
func (f *fakeSnapshotRAIDService) GetRAIDUsage(uint) (*v2.RAIDUsage, error) {
	panic("not implemented")
}
func (f *fakeSnapshotRAIDService) EnsureFilesystemResized(uint) error { panic("not implemented") }
func (f *fakeSnapshotRAIDService) ListRAIDArrays() ([]*svcmodel.RAIDArray, error) {
	panic("not implemented")
}
func (f *fakeSnapshotRAIDService) ReplaceDisk(uint, string, string, string, bool) error { panic("not implemented") }
func (f *fakeSnapshotRAIDService) RecoverOnBoot() error                   { panic("not implemented") }
func (f *fakeSnapshotRAIDService) Recover(uint) (string, error)           { panic("not implemented") }

// fakeSnapshotServices implements service.Services. It embeds the (nil)
// interface so any method this file doesn't override panics loudly instead
// of silently returning a zero value.
type fakeSnapshotServices struct {
	service.Services
	raid v2.RAIDService
	snap *snapshot.Service
}

func (f *fakeSnapshotServices) RAID() v2.RAIDService        { return f.raid }
func (f *fakeSnapshotServices) Snapshot() *snapshot.Service { return f.snap }

// installSnapshotFakes points service.MyService at a fake RAID service
// (returning createResult/createErr) and a real *snapshot.Service backed by
// in-memory fakes, restoring the previous service.MyService on cleanup.
func installSnapshotFakes(t *testing.T, createResult *svcmodel.RAIDArray, createErr error) (*snapshot.FakeRunner, *snapshot.FakeStore, *snapshot.FakeFstabPersister) {
	t.Helper()
	runner := snapshot.NewFakeRunner()
	store := snapshot.NewFakeStore()
	persister := snapshot.NewFakeFstabPersister()
	snap := &snapshot.Service{
		Runner:    runner,
		Store:     store,
		Persister: persister,
		Pause:     snapshot.NewPauseState(),
		Paths:     snapshot.OSPathChecker{},
		Clock:     snapshot.RealClock{},
	}

	prev := service.MyService
	service.MyService = &fakeSnapshotServices{
		raid: &fakeSnapshotRAIDService{result: createResult, err: createErr},
		snap: snap,
	}
	t.Cleanup(func() { service.MyService = prev })

	return runner, store, persister
}

// snapshotTestRAID returns a canned successful btrfs RAIDArray, backed by a
// real temp directory as its mount point so EnsureSnapshotsMount's
// os.MkdirAll(".snapshots") (a real filesystem call, not abstracted by the
// fake Runner) succeeds without touching the host filesystem.
func snapshotTestRAID(t *testing.T) *svcmodel.RAIDArray {
	t.Helper()
	return &svcmodel.RAIDArray{
		ID:         42,
		Name:       "snaptest",
		Level:      1,
		Filesystem: "btrfs",
		DevicePath: "/dev/md0",
		MountPoint: t.TempDir(),
		UUID:       "vol-uuid-snap-1",
		State:      "active",
	}
}

// postCreateRAID drives the CreateRAIDArray handler directly (bypassing the
// echo group/JWT middleware, which route/raid.go wires separately) and
// returns the task_id from the 202 response.
func postCreateRAID(t *testing.T, body []byte) string {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v2/raid", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := CreateRAIDArray(c); err != nil {
		t.Fatalf("CreateRAIDArray returned error: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var resp struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TaskID == "" {
		t.Fatalf("empty task_id in response body=%s", rec.Body.String())
	}
	return resp.TaskID
}

// waitForTaskDone polls the in-memory task store until taskID leaves the
// "creating" state (the async goroutine's fake RAID create + best-effort
// snapshot enable both run against in-memory fakes, so this always resolves
// in well under the deadline).
func waitForTaskDone(t *testing.T, taskID string) CreateTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := loadTask(taskID); ok && task.Status != "creating" {
			return task
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("task %s did not leave 'creating' state in time", taskID)
	return CreateTask{}
}

func boolPtr(b bool) *bool { return &b }

// captureLogOutput temporarily redirects the package-global zap logger
// (logger.LogInitWithWriterSyncers, unlike LogInitConsoleOnly, isn't
// sync.Once-guarded — it's safe and intended to be reconfigured) to an
// in-memory pipe, so a test can assert on the exact lines production code
// logs via logger.Info/logger.Error, then restores stdout logging on
// cleanup. The returned func stops capturing and returns everything written
// so far; call it only once, after the code under test has finished
// logging.
func captureLogOutput(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	logger.LogInitWithWriterSyncers(zapcore.AddSync(w))

	read := make(chan string, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		read <- string(buf)
	}()

	t.Cleanup(func() {
		logger.LogInitWithWriterSyncers(zapcore.AddSync(os.Stdout))
	})

	return func() string {
		w.Close()
		out := <-read
		r.Close()
		return out
	}
}

// --- binding: the three states -----------------------------------------

func TestCreateRAIDRequest_EnableSnapshotsBinding(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *bool
	}{
		{"field omitted (old client) binds to nil", `{"name":"r1","level":5,"disk_paths":["/dev/sda"]}`, nil},
		{"explicit true", `{"name":"r1","level":5,"disk_paths":["/dev/sda"],"enable_snapshots":true}`, boolPtr(true)},
		{"explicit false", `{"name":"r1","level":5,"disk_paths":["/dev/sda"],"enable_snapshots":false}`, boolPtr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req CreateRAIDRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if tc.want == nil {
				if req.EnableSnapshots != nil {
					t.Fatalf("EnableSnapshots = %v, want nil", *req.EnableSnapshots)
				}
				return
			}
			if req.EnableSnapshots == nil {
				t.Fatalf("EnableSnapshots = nil, want %v", *tc.want)
			}
			if *req.EnableSnapshots != *tc.want {
				t.Fatalf("EnableSnapshots = %v, want %v", *req.EnableSnapshots, *tc.want)
			}
		})
	}
}

// --- default (nil) enables snapshots ------------------------------------

func TestCreateRAIDArray_DefaultEnablesSnapshots(t *testing.T) {
	clearTaskStore()
	raid := snapshotTestRAID(t)
	_, store, persister := installSnapshotFakes(t, raid, nil)

	taskID := postCreateRAID(t, []byte(`{"name":"snaptest","level":1,"disk_paths":["/dev/sda","/dev/sdb"]}`))
	task := waitForTaskDone(t, taskID)

	if task.Status != "done" {
		t.Fatalf("task status = %q, want done (err=%s)", task.Status, task.Error)
	}

	policy, err := store.GetOrCreatePolicy(raid.UUID)
	if err != nil {
		t.Fatalf("GetOrCreatePolicy: %v", err)
	}
	if !policy.Enabled {
		t.Fatalf("policy.Enabled = false, want true (default/nil enable_snapshots must enable)")
	}

	snapshotsDir := snapshot.SnapshotsDir(snapshot.VolumeInfo{MountPoint: raid.MountPoint})
	if _, ok := persister.Persisted[snapshotsDir]; !ok {
		t.Fatalf("expected @snapshots mount persisted to fstab at %s, got %v", snapshotsDir, persister.Persisted)
	}
}

// --- explicit true enables snapshots ------------------------------------

func TestCreateRAIDArray_ExplicitTrueEnablesSnapshots(t *testing.T) {
	clearTaskStore()
	raid := snapshotTestRAID(t)
	_, store, persister := installSnapshotFakes(t, raid, nil)

	taskID := postCreateRAID(t, []byte(`{"name":"snaptest","level":1,"disk_paths":["/dev/sda","/dev/sdb"],"enable_snapshots":true}`))
	task := waitForTaskDone(t, taskID)

	if task.Status != "done" {
		t.Fatalf("task status = %q, want done (err=%s)", task.Status, task.Error)
	}

	policy, err := store.GetOrCreatePolicy(raid.UUID)
	if err != nil {
		t.Fatalf("GetOrCreatePolicy: %v", err)
	}
	if !policy.Enabled {
		t.Fatalf("policy.Enabled = false, want true (explicit enable_snapshots:true)")
	}

	snapshotsDir := snapshot.SnapshotsDir(snapshot.VolumeInfo{MountPoint: raid.MountPoint})
	if _, ok := persister.Persisted[snapshotsDir]; !ok {
		t.Fatalf("expected @snapshots mount persisted to fstab at %s, got %v", snapshotsDir, persister.Persisted)
	}
}

// --- explicit false skips snapshot enable --------------------------------

func TestCreateRAIDArray_ExplicitFalseSkipsSnapshotEnable(t *testing.T) {
	clearTaskStore()
	raid := snapshotTestRAID(t)
	_, store, persister := installSnapshotFakes(t, raid, nil)

	taskID := postCreateRAID(t, []byte(`{"name":"snaptest","level":1,"disk_paths":["/dev/sda","/dev/sdb"],"enable_snapshots":false}`))
	task := waitForTaskDone(t, taskID)

	if task.Status != "done" {
		t.Fatalf("task status = %q, want done (err=%s)", task.Status, task.Error)
	}

	if len(persister.Persisted) != 0 {
		t.Fatalf("expected no fstab entries persisted when enable_snapshots:false, got %v", persister.Persisted)
	}

	policy, err := store.GetOrCreatePolicy(raid.UUID)
	if err != nil {
		t.Fatalf("GetOrCreatePolicy: %v", err)
	}
	if policy.Enabled {
		t.Fatalf("policy.Enabled = true, want false: enable_snapshots:false must not enable")
	}
}

// --- non-btrfs RAID: skip the enable step entirely ----------------------

// snapshotTestRAIDExt4 mirrors snapshotTestRAID but with an ext4 filesystem
// — snapshots are btrfs-only, so this array must never reach SavePolicy.
func snapshotTestRAIDExt4(t *testing.T) *svcmodel.RAIDArray {
	t.Helper()
	return &svcmodel.RAIDArray{
		ID:         43,
		Name:       "ext4test",
		Level:      1,
		Filesystem: "ext4",
		DevicePath: "/dev/md1",
		MountPoint: t.TempDir(),
		UUID:       "vol-uuid-ext4-1",
		State:      "active",
	}
}

func TestCreateRAIDArray_NonBtrfsSkipsSnapshotEnable(t *testing.T) {
	clearTaskStore()
	raid := snapshotTestRAIDExt4(t)
	_, store, persister := installSnapshotFakes(t, raid, nil)

	getLogs := captureLogOutput(t)

	taskID := postCreateRAID(t, []byte(`{"name":"ext4test","level":1,"disk_paths":["/dev/sda","/dev/sdb"],"filesystem":"ext4"}`))
	task := waitForTaskDone(t, taskID)

	if task.Status != "done" {
		t.Fatalf("task status = %q, want done (err=%s)", task.Status, task.Error)
	}
	if task.RaidID == nil || *task.RaidID != raid.ID {
		t.Fatalf("task.RaidID = %v, want %d", task.RaidID, raid.ID)
	}

	// The real defect this guards: EnsureSnapshotsMount already refuses a
	// non-btrfs volume before any policy row is persisted (see
	// TestSavePolicyPersistsDisablingWithoutMountCheck-adjacent guard in
	// service/snapshot/service.go), so store-level state is identical
	// whether or not the hook skips early. The one place the bug is
	// actually observable is the log: the unfixed hook still calls
	// Service.SavePolicy, which fails and gets logged at error level on
	// every single ext4 RAID creation — that's the reported noise. The fix
	// must skip before attempting the call at all, so no error-level line
	// is emitted (an info-level skip line is fine).
	if logs := getLogs(); strings.Contains(logs, "\terror\t") {
		t.Fatalf("expected no error-level log for a non-btrfs RAID array's snapshot skip, got:\n%s", logs)
	}

	if store.SavePolicyCalls != 0 {
		t.Fatalf("SavePolicy called %d times for a non-btrfs RAID array, want 0", store.SavePolicyCalls)
	}
	if len(persister.Persisted) != 0 {
		t.Fatalf("expected no fstab entries persisted for a non-btrfs RAID array, got %v", persister.Persisted)
	}
}

// --- best-effort: enabling snapshots failing must not fail RAID creation -

func TestCreateRAIDArray_BestEffortSnapshotFailureDoesNotFailCreation(t *testing.T) {
	clearTaskStore()
	raid := snapshotTestRAID(t)
	runner, store, _ := installSnapshotFakes(t, raid, nil)
	runner.MountSubvolumeErr[raid.DevicePath] = errors.New("simulated @snapshots mount failure")

	taskID := postCreateRAID(t, []byte(`{"name":"snaptest","level":1,"disk_paths":["/dev/sda","/dev/sdb"]}`))
	task := waitForTaskDone(t, taskID)

	if task.Status != "done" {
		t.Fatalf("RAID creation must succeed even when enabling snapshots fails; got status=%q err=%q", task.Status, task.Error)
	}
	if task.RaidID == nil || *task.RaidID != raid.ID {
		t.Fatalf("task.RaidID = %v, want %d", task.RaidID, raid.ID)
	}

	policy, err := store.GetOrCreatePolicy(raid.UUID)
	if err != nil {
		t.Fatalf("GetOrCreatePolicy: %v", err)
	}
	if policy.Enabled {
		t.Fatalf("policy.Enabled = true, want false: SavePolicy must not persist as enabled after mount failure")
	}
}
