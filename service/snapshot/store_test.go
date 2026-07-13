package snapshot

import (
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/pkg/sqlite"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"gotest.tools/v3/assert"
)

func newTestGormStore(t *testing.T) *GormStore {
	t.Helper()
	// A dedicated in-memory sqlite DB per test (unique DSN via t.Name())
	// avoids cross-test interference, mirroring service/v2/merge_test.go's
	// use of sqlite.GetDBByFile for the shared-cache in-memory pattern.
	db := sqlite.GetDBByFile("file:" + t.Name() + "?mode=memory&cache=shared")
	return NewGormStore(db)
}

func TestGormStoreListInsertDeleteSnapshot(t *testing.T) {
	store := newTestGormStore(t)

	if err := store.InsertSnapshot(model.Snapshot{
		VolumeUUID: "vol-1",
		Name:       "20260712T030000Z_auto-hourly",
		Type:       TypeAutoHourly,
	}); err != nil {
		t.Fatalf("InsertSnapshot error: %v", err)
	}

	recs, err := store.ListSnapshots("vol-1")
	if err != nil {
		t.Fatalf("ListSnapshots error: %v", err)
	}
	assert.Equal(t, len(recs), 1)
	assert.Equal(t, recs[0].Name, "20260712T030000Z_auto-hourly")

	if err := store.DeleteSnapshot(recs[0].ID); err != nil {
		t.Fatalf("DeleteSnapshot error: %v", err)
	}

	recs, err = store.ListSnapshots("vol-1")
	if err != nil {
		t.Fatalf("ListSnapshots error: %v", err)
	}
	assert.Equal(t, len(recs), 0)
}

func TestGormStoreListSnapshotsIsolatesByVolume(t *testing.T) {
	store := newTestGormStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	must(store.InsertSnapshot(model.Snapshot{VolumeUUID: "vol-1", Name: "a"}))
	must(store.InsertSnapshot(model.Snapshot{VolumeUUID: "vol-2", Name: "b"}))

	recs, err := store.ListSnapshots("vol-1")
	if err != nil {
		t.Fatalf("ListSnapshots error: %v", err)
	}
	assert.Equal(t, len(recs), 1)
	assert.Equal(t, recs[0].Name, "a")
}

func TestGormStoreGetOrCreatePolicyCreatesDefault(t *testing.T) {
	store := newTestGormStore(t)

	policy, err := store.GetOrCreatePolicy("vol-1")
	if err != nil {
		t.Fatalf("GetOrCreatePolicy error: %v", err)
	}
	assert.Equal(t, policy.Enabled, false)
	assert.Equal(t, policy.HourlyKeep, DefaultHourlyKeep)
	assert.Equal(t, policy.DailyKeep, DefaultDailyKeep)
	assert.Equal(t, policy.WeeklyKeep, DefaultWeeklyKeep)
	assert.Equal(t, policy.PauseThresholdPct, DefaultPauseThresholdPct)

	// Calling again must not create a second row.
	again, err := store.GetOrCreatePolicy("vol-1")
	if err != nil {
		t.Fatalf("GetOrCreatePolicy (2nd call) error: %v", err)
	}
	assert.Equal(t, again.ID, policy.ID)
}

func TestGormStoreSavePolicyUpserts(t *testing.T) {
	store := newTestGormStore(t)

	created, err := store.GetOrCreatePolicy("vol-1")
	if err != nil {
		t.Fatalf("GetOrCreatePolicy error: %v", err)
	}

	updated := *created
	updated.Enabled = true
	updated.HourlyKeep = 12
	if err := store.SavePolicy(updated); err != nil {
		t.Fatalf("SavePolicy error: %v", err)
	}

	got, err := store.GetOrCreatePolicy("vol-1")
	if err != nil {
		t.Fatalf("GetOrCreatePolicy error: %v", err)
	}
	assert.Equal(t, got.ID, created.ID) // same row, not a duplicate
	assert.Equal(t, got.Enabled, true)
	assert.Equal(t, got.HourlyKeep, 12)
}

// TestGormStoreSavePolicyPersistsZeroValues guards against a real bug found
// during self-review: GORM's Updates(struct) silently skips zero-value
// fields, so a naive Model(&existing).Updates(&policy) would never actually
// persist Enabled=false (the API's "turn snapshots off" case) or, e.g.,
// HourlyKeep=0. SavePolicy must use Save (full-record write) instead.
func TestGormStoreSavePolicyPersistsZeroValues(t *testing.T) {
	store := newTestGormStore(t)

	enabled, err := store.GetOrCreatePolicy("vol-1")
	if err != nil {
		t.Fatalf("GetOrCreatePolicy error: %v", err)
	}
	enabled.Enabled = true
	if err := store.SavePolicy(*enabled); err != nil {
		t.Fatalf("SavePolicy (enable) error: %v", err)
	}

	disabled := *enabled
	disabled.Enabled = false
	disabled.HourlyKeep = 0
	if err := store.SavePolicy(disabled); err != nil {
		t.Fatalf("SavePolicy (disable) error: %v", err)
	}

	got, err := store.GetOrCreatePolicy("vol-1")
	if err != nil {
		t.Fatalf("GetOrCreatePolicy error: %v", err)
	}
	assert.Equal(t, got.Enabled, false)
	assert.Equal(t, got.HourlyKeep, 0)
}
