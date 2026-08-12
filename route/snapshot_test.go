package route

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	commonmodel "github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	svcmodel "github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"github.com/NimoTech/NimoOS-LocalStorage/service/snapshot"
	v2 "github.com/NimoTech/NimoOS-LocalStorage/service/v2"
)

// fakeRAIDService implements v2.RAIDService, exposing only ListRAIDArrays —
// the only method route/snapshot.go calls. Every other method panics if
// called, since no snapshot route test should ever reach them.
type fakeRAIDService struct {
	arrays []*svcmodel.RAIDArray
	err    error
}

var _ v2.RAIDService = (*fakeRAIDService)(nil)

func (f *fakeRAIDService) CreateRAIDArray(int, []string, string, int, string, bool, func(int)) (*svcmodel.RAIDArray, error) {
	panic("not implemented")
}
func (f *fakeRAIDService) DeleteRAIDArray(uint) error                 { panic("not implemented") }
func (f *fakeRAIDService) GetRAIDStatus(uint) (*v2.RAIDStatus, error) { panic("not implemented") }
func (f *fakeRAIDService) GetRAIDUsage(uint) (*v2.RAIDUsage, error)   { panic("not implemented") }
func (f *fakeRAIDService) EnsureFilesystemResized(uint) error         { panic("not implemented") }
func (f *fakeRAIDService) ListRAIDArrays() ([]*svcmodel.RAIDArray, error) {
	return f.arrays, f.err
}
func (f *fakeRAIDService) ReplaceDisk(uint, string, string, string, bool) error { panic("not implemented") }
func (f *fakeRAIDService) RecoverOnBoot() error                   { panic("not implemented") }
func (f *fakeRAIDService) Recover(uint) (string, []string, error)           { panic("not implemented") }

// fakeServices implements service.Services. It embeds the interface (nil)
// so every method not explicitly overridden panics on a nil dereference if
// a test accidentally exercises a code path that needs it — a loud failure
// rather than a silently wrong result.
type fakeServices struct {
	service.Services
	raid v2.RAIDService
	snap *snapshot.Service
}

func (f *fakeServices) RAID() v2.RAIDService        { return f.raid }
func (f *fakeServices) Snapshot() *snapshot.Service { return f.snap }

// installFakeServices points service.MyService at a fake for the duration
// of the test, restoring the previous value on cleanup. Paths uses the real
// OSPathChecker (btrfsVolume's MountPoint is a real t.TempDir(), matching
// service/snapshot's own restore/file-versions test convention) and Copier
// is a FakeCopier so no test ever shells out to a real `cp`.
func installFakeServices(t *testing.T, raids []*svcmodel.RAIDArray) (*snapshot.FakeRunner, *snapshot.FakeStore) {
	t.Helper()
	runner, store, _ := installFakeServicesWithCopier(t, raids)
	return runner, store
}

// installFakeServicesWithCopier is installFakeServices's sibling for tests
// that need to assert on the copy calls Restore made (e.g. reflink-fallback
// wiring) or inject a copy failure.
func installFakeServicesWithCopier(t *testing.T, raids []*svcmodel.RAIDArray) (*snapshot.FakeRunner, *snapshot.FakeStore, *snapshot.FakeCopier) {
	t.Helper()
	runner := snapshot.NewFakeRunner()
	store := snapshot.NewFakeStore()
	copier := snapshot.NewFakeCopier()
	svc := &snapshot.Service{
		Runner:    runner,
		Store:     store,
		Persister: snapshot.NewFakeFstabPersister(),
		Pause:     snapshot.NewPauseState(),
		Paths:     snapshot.OSPathChecker{},
		Copier:    copier,
		Clock:     snapshot.RealClock{},
	}

	prev := service.MyService
	fake := &fakeServices{raid: &fakeRAIDService{arrays: raids}, snap: svc}
	service.MyService = fake
	t.Cleanup(func() { service.MyService = prev })

	return runner, store, copier
}

// btrfsVolume returns a RAIDArray backed by a real temp directory (so
// EnsureSnapshotsMount's os.MkdirAll(".snapshots") — a real filesystem call
// not abstracted by the fake Runner — succeeds without touching the actual
// host filesystem or needing root), per test.
func btrfsVolume(t *testing.T) *svcmodel.RAIDArray {
	t.Helper()
	return &svcmodel.RAIDArray{
		UUID:       "vol-uuid-1",
		DevicePath: "/dev/md0",
		MountPoint: t.TempDir(),
		Filesystem: "btrfs",
	}
}

func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) commonmodel.Result {
	t.Helper()
	var result commonmodel.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
	return result
}

func doRequest(e http.Handler, method, target string, body []byte, loopback bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if loopback {
		req.RemoteAddr = "127.0.0.1:12345"
	} else {
		req.RemoteAddr = "203.0.113.5:12345"
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestSnapshotRoutesRequireAuthForNonLoopback(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/volumes", nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestListSnapshotVolumes(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/volumes", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	data, ok := result.Data.([]interface{})
	if !ok || len(data) != 1 {
		t.Fatalf("expected 1 volume status, got %#v", result.Data)
	}
	row := data[0].(map[string]interface{})
	if row["volume_uuid"] != "vol-uuid-1" {
		t.Errorf("got volume_uuid %v, want vol-uuid-1", row["volume_uuid"])
	}
	if row["supported"] != true {
		t.Errorf("got supported %v, want true", row["supported"])
	}
}

func TestListSnapshotsRequiresVolumeUUID(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListSnapshotsUnknownVolumeIs404(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot?volume_uuid=nope", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListSnapshotsReturnsReconciledList(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.ListResult[vol.MountPoint] = []snapshot.SubvolumeEntry{
		{ID: "300", Path: "@snapshots/20260712T030000Z_manual_x"},
	}
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot?volume_uuid=vol-uuid-1", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	data, ok := result.Data.([]interface{})
	if !ok || len(data) != 1 {
		t.Fatalf("expected 1 snapshot, got %#v", result.Data)
	}
}

func TestCreateSnapshotRequiresVolumeUUID(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodPost, "/v2/snapshot", []byte(`{}`), true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateSnapshotRejectsUnsupportedVolume(t *testing.T) {
	vol := btrfsVolume(t)
	vol.Filesystem = "ext4"
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (not btrfs); body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateSnapshotSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, store := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.SeedTopLevelSubvolume(vol.DevicePath, "@snapshots")
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","label":"before-move"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	recs, _ := store.ListSnapshots("vol-uuid-1")
	if len(recs) != 1 {
		t.Fatalf("expected 1 stored snapshot, got %d", len(recs))
	}
	if recs[0].Type != snapshot.TypeManual {
		t.Errorf("got type %q, want manual", recs[0].Type)
	}
}

func TestDeleteSnapshotRejectsPathTraversal(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	// A %2F-encoded name is decoded to a literal "/" by net/url before it
	// reaches echo's router, so this may never even reach our handler (the
	// router itself can 404 on the now-multi-segment path) — either way,
	// the only thing that matters for this security property is that
	// nothing gets deleted and the request never succeeds.
	target := "/v2/snapshot/" + "20260712T030000Z_manual_..%2F..%2F..%2Fetc%2Fpasswd" + "?volume_uuid=vol-uuid-1"
	rec := doRequest(router, http.MethodDelete, target, nil, true)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the traversal attempt to be rejected, got 200; body=%s", rec.Body.String())
	}
	if len(runner.DeletedPaths) != 0 {
		t.Fatalf("expected no disk delete for a traversal attempt, got %v", runner.DeletedPaths)
	}
}

func TestDeleteSnapshotRejectsUnrecognizedName(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodDelete, "/v2/snapshot/not-one-of-ours?volume_uuid=vol-uuid-1", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSnapshotNotFoundOnDisk(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodDelete, "/v2/snapshot/20260712T030000Z_manual_x?volume_uuid=vol-uuid-1", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSnapshotSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, store := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := "20260712T030000Z_manual_x"
	runner.ListResult[vol.MountPoint] = []snapshot.SubvolumeEntry{{ID: "300", Path: "@snapshots/" + name}}
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodDelete, "/v2/snapshot/"+name+"?volume_uuid=vol-uuid-1", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.DeletedPaths) != 1 {
		t.Fatalf("expected 1 disk delete, got %v", runner.DeletedPaths)
	}
	remaining, _ := store.ListSnapshots("vol-uuid-1")
	if len(remaining) != 0 {
		t.Fatalf("expected db record removed, got %+v", remaining)
	}
}

func TestGetSnapshotPolicyReturnsDefault(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/policy?volume_uuid=vol-uuid-1", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	if row["enabled"] != false {
		t.Errorf("got enabled %v, want false", row["enabled"])
	}
}

func TestPutSnapshotPolicyRejectsEnablingUnsupportedVolume(t *testing.T) {
	vol := btrfsVolume(t)
	vol.Filesystem = "ext4"
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","enabled":true,"hourly_keep":24,"daily_keep":7,"weekly_keep":4,"pause_threshold_pct":90}`)
	rec := doRequest(router, http.MethodPut, "/v2/snapshot/policy", body, true)
	if rec.Code != http.StatusConflict && rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 409 or 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutSnapshotPolicyDisableSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","enabled":false}`)
	rec := doRequest(router, http.MethodPut, "/v2/snapshot/policy", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// The next three tests cover B2 Fix Round 1: a known btrfs volume that is
// currently unmounted (RAID member offline, cold-boot enumeration race)
// must still allow reading its saved policy, and disabling it — only
// *enabling* automatic snapshots legitimately needs the volume mounted
// (SavePolicy itself only calls EnsureSnapshotsMount when Enabled==true).

func TestGetSnapshotPolicyWorksWhenVolumeUnmounted(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	// Deliberately not seeded as mounted.
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/policy?volume_uuid=vol-uuid-1", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (policy read must not require mount); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutSnapshotPolicyDisableSucceedsWhenUnmounted(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	// Deliberately not seeded as mounted.
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","enabled":false}`)
	rec := doRequest(router, http.MethodPut, "/v2/snapshot/policy", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (disabling must not require mount); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutSnapshotPolicyEnableStillRequiresMountWhenUnmounted(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	// Deliberately not seeded as mounted.
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","enabled":true,"hourly_keep":24,"daily_keep":7,"weekly_keep":4,"pause_threshold_pct":90}`)
	rec := doRequest(router, http.MethodPut, "/v2/snapshot/policy", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (enabling still requires mount); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutSnapshotPolicyEnableSucceedsForSupportedVolume(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _ := installFakeServices(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.SeedTopLevelSubvolume(vol.DevicePath, "@snapshots")
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","enabled":true,"hourly_keep":24,"daily_keep":7,"weekly_keep":4,"pause_threshold_pct":90}`)
	rec := doRequest(router, http.MethodPut, "/v2/snapshot/policy", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// --- POST /v2/snapshot/restore ---

// seedRestorableSnapshot creates a real snapshot subdirectory containing
// "report.docx" under vol's .snapshots, and registers it with both the fake
// runner (so ListSubvolumes/reconciliation see it) and returns its name.
func seedRestorableSnapshot(t *testing.T, runner *snapshot.FakeRunner, vol *svcmodel.RAIDArray) string {
	t.Helper()
	name := "20260712T030000Z_manual_before-move"
	dir := vol.MountPoint + "/.snapshots/" + name
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/report.docx", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.ListResult[vol.MountPoint] = []snapshot.SubvolumeEntry{
		{ID: "300", Path: "@snapshots/" + name},
	}
	return name
}

func TestRestoreSnapshotFileSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	restoredPath, _ := row["restored_path"].(string)
	if restoredPath == "" || !strings.Contains(restoredPath, ".restored-") {
		t.Fatalf("expected a non-empty .restored-<ts> path, got %v", row)
	}
	if len(copier.Calls) != 1 {
		t.Fatalf("expected 1 copy call, got %+v", copier.Calls)
	}
}

func TestRestoreSnapshotFileRequiresSnapshotAndPath(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, _ := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRestoreSnapshotFileRejectsPathTraversal(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"../../../etc/passwd"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt for a traversal attempt, got %+v", copier.Calls)
	}
}

func TestRestoreSnapshotFileRejectsUnknownSnapshotName(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, _ := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"not-one-of-ours","path":"report.docx"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRestoreSnapshotFileNotFoundOnDiskIs404(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, _ := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"20260101T000000Z_manual_ghost","path":"report.docx"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRestoreSnapshotFileWithDestDirSucceeds proves the dest_dir request
// field is threaded through to snapshot.Service.Restore end-to-end: the
// restored_path in the response lands under the caller-chosen directory
// instead of the snapshot's original relative location.
func TestRestoreSnapshotFileWithDestDirSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	destDir := vol.MountPoint + "/chosen-destination"
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","dest_dir":"` + destDir + `"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	restoredPath, _ := row["restored_path"].(string)
	if !strings.HasPrefix(restoredPath, destDir+"/") {
		t.Fatalf("expected restored_path under %q, got %v", destDir, row)
	}
	if len(copier.Calls) != 1 {
		t.Fatalf("expected 1 copy call, got %+v", copier.Calls)
	}
}

// TestRestoreSnapshotFileWithMarkerFalseSucceeds proves the with_marker
// request field, when false, restores the file under its original name
// (no ".restored-<ts>" anywhere in restored_path).
func TestRestoreSnapshotFileWithMarkerFalseSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, _ := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","with_marker":false}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	restoredPath, _ := row["restored_path"].(string)
	want := vol.MountPoint + "/report.docx"
	if restoredPath != want {
		t.Fatalf("got restored_path %q, want %q", restoredPath, want)
	}
}

// TestRestoreSnapshotFileRejectsRelativeDestDir proves an invalid dest_dir
// (relative, so it can never be validated against a volume's mount point)
// is rejected with 400 rather than silently falling back to the default
// location or attempting a copy.
func TestRestoreSnapshotFileRejectsRelativeDestDir(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","dest_dir":"relative/dir"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", copier.Calls)
	}
}

// TestRestoreSnapshotFileRejectsNonexistentDestDir proves dest_dir is never
// auto-created at the HTTP layer either: a syntactically valid path under
// the known volume that just doesn't exist yet is a 400, not a silent
// mkdir.
func TestRestoreSnapshotFileRejectsNonexistentDestDir(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	missing := vol.MountPoint + "/does-not-exist-yet"
	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","dest_dir":"` + missing + `"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", copier.Calls)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("expected dest_dir to remain uncreated, got stat error %v", statErr)
	}
}

// TestRestoreSnapshotFileRejectsDestDirOutsideAnyVolume proves a dest_dir
// that's an absolute, existing directory but not under ANY currently known
// volume's mount point is still a 400 (not silently accepted as "some
// arbitrary path on disk").
func TestRestoreSnapshotFileRejectsDestDirOutsideAnyVolume(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	outside := t.TempDir()
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","dest_dir":"` + outside + `"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", copier.Calls)
	}
}

func TestRestoreSnapshotFileSourceNotFoundIs404(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, _ := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"missing.txt"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// --- POST /v2/snapshot/restore: on_conflict ---

// TestRestoreSnapshotFileOnConflictOverwriteSucceedsWithNoExistingTarget
// proves the on_conflict request field is threaded through end-to-end: with
// nothing occupying the original-name destination, overwrite lands there
// directly (no ".restored-<ts>" marker anywhere in restored_path).
func TestRestoreSnapshotFileOnConflictOverwriteSucceedsWithNoExistingTarget(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","on_conflict":"overwrite"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	restoredPath, _ := row["restored_path"].(string)
	want := vol.MountPoint + "/report.docx"
	if restoredPath != want {
		t.Fatalf("got restored_path %q, want %q", restoredPath, want)
	}
	if len(copier.Calls) != 1 || copier.Calls[0].Dest != want {
		t.Fatalf("expected 1 direct copy call to %q, got %+v", want, copier.Calls)
	}
}

// TestRestoreSnapshotFileOnConflictOverwriteReplacesExistingTargetContent
// proves the full atomic-replace path works end-to-end through the HTTP
// handler, including the real os.Rename this feature's safety depends on:
// FakeCopier.RealCopyFiles is turned on so the temporary file it copies to
// genuinely exists on disk for the rename-into-place step to find.
func TestRestoreSnapshotFileOnConflictOverwriteReplacesExistingTargetContent(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	copier.RealCopyFiles = true
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	liveFile := vol.MountPoint + "/report.docx"
	if err := os.WriteFile(liveFile, []byte("stale live content"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","on_conflict":"overwrite"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	restoredPath, _ := row["restored_path"].(string)
	if restoredPath != liveFile {
		t.Fatalf("got restored_path %q, want %q", restoredPath, liveFile)
	}
	got, err := os.ReadFile(liveFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("got restored content %q, want %q (the snapshot's content)", got, "data")
	}
}

// TestRestoreSnapshotFileOnConflictOverwriteRejectsDirectoryTarget proves a
// FILE source whose original-name destination is already occupied by a
// DIRECTORY is rejected with 400, not silently accepted or nested.
func TestRestoreSnapshotFileOnConflictOverwriteRejectsDirectoryTarget(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	if err := os.MkdirAll(vol.MountPoint+"/report.docx", 0o755); err != nil {
		t.Fatal(err)
	}
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","on_conflict":"overwrite"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", copier.Calls)
	}
}

// TestRestoreSnapshotFileOnConflictOverwriteRejectsDirectorySource proves a
// DIRECTORY source whose original-name destination is already occupied (by
// anything) is rejected with 400 — overwrite is file-only regardless of what
// currently sits at the destination.
func TestRestoreSnapshotFileOnConflictOverwriteRejectsDirectorySource(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := "20260712T030000Z_manual_before-move"
	snapDir := vol.MountPoint + "/.snapshots/" + name + "/Projects"
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner.ListResult[vol.MountPoint] = []snapshot.SubvolumeEntry{
		{ID: "300", Path: "@snapshots/" + name},
	}
	if err := os.MkdirAll(vol.MountPoint+"/Projects", 0o755); err != nil {
		t.Fatal(err)
	}
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"Projects","on_conflict":"overwrite"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", copier.Calls)
	}
}

// TestRestoreSnapshotFileRejectsInvalidOnConflictValue proves an
// unrecognized on_conflict value is a 400, not silently treated as
// keep_both or passed through to the service layer unchecked.
func TestRestoreSnapshotFileRejectsInvalidOnConflictValue(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","on_conflict":"replace_please"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(copier.Calls) != 0 {
		t.Fatalf("expected no copy attempt, got %+v", copier.Calls)
	}
}

// TestRestoreSnapshotFileOnConflictKeepBothExplicitMatchesDefault proves
// passing on_conflict="keep_both" explicitly reproduces the pre-existing
// default (omitting on_conflict) unchanged.
func TestRestoreSnapshotFileOnConflictKeepBothExplicitMatchesDefault(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, copier := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	name := seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	body := []byte(`{"volume_uuid":"vol-uuid-1","snapshot":"` + name + `","path":"report.docx","on_conflict":"keep_both"}`)
	rec := doRequest(router, http.MethodPost, "/v2/snapshot/restore", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	row := result.Data.(map[string]interface{})
	restoredPath, _ := row["restored_path"].(string)
	if restoredPath == "" || !strings.Contains(restoredPath, ".restored-") {
		t.Fatalf("expected a non-empty .restored-<ts> path, got %v", row)
	}
	if len(copier.Calls) != 1 {
		t.Fatalf("expected 1 copy call, got %+v", copier.Calls)
	}
}

// --- GET /v2/snapshot/file-versions ---

func TestListFileVersionsSucceeds(t *testing.T) {
	vol := btrfsVolume(t)
	runner, _, _ := installFakeServicesWithCopier(t, []*svcmodel.RAIDArray{vol})
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	seedRestorableSnapshot(t, runner, vol)
	router := InitSnapshotRouter()

	target := "/v2/snapshot/file-versions?path=" + vol.MountPoint + "/report.docx"
	rec := doRequest(router, http.MethodGet, target, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	data, ok := result.Data.([]interface{})
	if !ok || len(data) != 1 {
		t.Fatalf("expected 1 file version, got %#v", result.Data)
	}
}

func TestListFileVersionsRequiresPath(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/file-versions", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListFileVersionsRequiresAbsolutePath(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/file-versions?path=relative/report.docx", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListFileVersionsUnknownPathIs404(t *testing.T) {
	vol := btrfsVolume(t)
	installFakeServices(t, []*svcmodel.RAIDArray{vol})
	router := InitSnapshotRouter()

	rec := doRequest(router, http.MethodGet, "/v2/snapshot/file-versions?path=/no/such/volume/file.txt", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
