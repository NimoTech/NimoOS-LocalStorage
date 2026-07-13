package snapshot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// volumeBox is a concurrency-safe mutable slot for the volumes a test's
// VolumeListerFunc returns, letting a test simulate hot-plug (change what's
// "currently enumerated" between RunOnce calls) without any caching on the
// Scheduler's part.
type volumeBox struct {
	mu   sync.Mutex
	vols []VolumeInfo
}

func (b *volumeBox) Set(vols ...VolumeInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.vols = vols
}

func (b *volumeBox) Get() []VolumeInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.vols
}

func newTestScheduler(t *testing.T, clock *FakeClock) (*Scheduler, *FakeRunner, *FakeStore, *FakeUsageProvider, *FakePublisher, *volumeBox) {
	t.Helper()
	runner := NewFakeRunner()
	store := NewFakeStore()
	usage := NewFakeUsageProvider()
	publisher := NewFakePublisher()
	box := &volumeBox{}

	lister := func(_ context.Context) ([]VolumeInfo, error) {
		return box.Get(), nil
	}

	sched := NewScheduler(SchedulerConfig{
		Runner:      runner,
		Store:       store,
		Persister:   NewFakeFstabPersister(),
		Pause:       NewPauseState(),
		ListVolumes: lister,
		Usage:       usage,
		Publisher:   publisher,
		Clock:       clock,
	})
	return sched, runner, store, usage, publisher, box
}

func schedTestVolume(t *testing.T) VolumeInfo {
	t.Helper()
	return VolumeInfo{
		UUID:       "vol-1",
		DevicePath: "/dev/md0",
		MountPoint: t.TempDir(),
		Filesystem: "btrfs",
	}
}

// enableAndMount seeds runner/store so vol is eligible for the scheduler:
// mounted, btrfs, @snapshots mounted, policy enabled with the given keep
// values/threshold.
func enableAndMount(t *testing.T, runner *FakeRunner, store *FakeStore, vol VolumeInfo, hourlyKeep, dailyKeep, weeklyKeep, pauseThreshold int) {
	t.Helper()
	runner.SeedMounted(vol.DevicePath, vol.MountPoint)
	runner.SeedTopLevelSubvolume(vol.DevicePath, SnapshotsSubvolumeName)
	runner.SeedMounted(vol.DevicePath, SnapshotsDir(vol))

	policy := model.SnapshotPolicy{
		VolumeUUID:        vol.UUID,
		Enabled:           true,
		HourlyKeep:        hourlyKeep,
		DailyKeep:         dailyKeep,
		WeeklyKeep:        weeklyKeep,
		PauseThresholdPct: pauseThreshold,
	}
	if err := store.SavePolicy(policy); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
}

func mustName(t *testing.T, ts time.Time, typ string) string {
	t.Helper()
	name, err := FormatName(ts, typ, "")
	if err != nil {
		t.Fatalf("FormatName: %v", err)
	}
	return name
}

func mkSnapNamed(volumeUUID, name, typ string, createdAt time.Time) model.Snapshot {
	return model.Snapshot{VolumeUUID: volumeUUID, Name: name, Type: typ, CreatedAt: createdAt}
}

// seedSnapshot records rec both in store (DB) and runner's disk listing for
// vol, so reconciliation sees them as already-agreeing (no diff to apply).
func seedSnapshot(t *testing.T, runner *FakeRunner, store *FakeStore, vol VolumeInfo, rec model.Snapshot) {
	t.Helper()
	if err := store.InsertSnapshot(rec); err != nil {
		t.Fatalf("seed snapshot %s: %v", rec.Name, err)
	}
	runner.ListResult[vol.MountPoint] = append(runner.ListResult[vol.MountPoint], SubvolumeEntry{Path: "@snapshots/" + rec.Name})
}

func TestSchedulerCreatesDueCadenceOnFirstEligibleTick(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, publisher, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)

	sched.RunOnce(context.Background())

	if len(runner.CreatedSnapshots) != 3 {
		t.Fatalf("expected 3 snapshots created (hourly+daily+weekly bootstrap), got %d: %+v", len(runner.CreatedSnapshots), runner.CreatedSnapshots)
	}
	if publisher.CountByName(EventSnapshotCreated) != 3 {
		t.Fatalf("expected 3 created events, got %d", publisher.CountByName(EventSnapshotCreated))
	}
	recs, _ := store.ListSnapshots(vol.UUID)
	if len(recs) != 3 {
		t.Fatalf("expected 3 db records, got %d", len(recs))
	}
}

func TestSchedulerDoesNotCreateBeforeCadenceDue(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)

	for _, typ := range []string{TypeAutoHourly, TypeAutoDaily, TypeAutoWeekly} {
		name := mustName(t, now.Add(-time.Minute), typ)
		seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, name, typ, now.Add(-time.Minute)))
	}

	sched.RunOnce(context.Background())

	if len(runner.CreatedSnapshots) != 0 {
		t.Fatalf("expected no creations before cadence due, got %+v", runner.CreatedSnapshots)
	}
}

func TestSchedulerRetentionCleansOverKeepCountForOwnTypeOnly(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	// hourly_keep=2, and 3 hourly snapshots already exist; the newest is
	// recent enough that hourly isn't due, so this tick is cleanup-only.
	enableAndMount(t, runner, store, vol, 2, 7, 4, 90)
	box.Set(vol)

	h1Name := mustName(t, now.Add(-3*time.Hour), TypeAutoHourly)
	h2Name := mustName(t, now.Add(-2*time.Hour), TypeAutoHourly)
	h3Name := mustName(t, now.Add(-time.Minute), TypeAutoHourly)
	dName := mustName(t, now.Add(-time.Minute), TypeAutoDaily)
	wName := mustName(t, now.Add(-time.Minute), TypeAutoWeekly)

	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, h1Name, TypeAutoHourly, now.Add(-3*time.Hour)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, h2Name, TypeAutoHourly, now.Add(-2*time.Hour)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, h3Name, TypeAutoHourly, now.Add(-time.Minute)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, dName, TypeAutoDaily, now.Add(-time.Minute)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, wName, TypeAutoWeekly, now.Add(-time.Minute)))

	sched.RunOnce(context.Background())

	recs, _ := store.ListSnapshots(vol.UUID)
	hourlyCount, dailyCount, weeklyCount := 0, 0, 0
	for _, r := range recs {
		switch r.Type {
		case TypeAutoHourly:
			hourlyCount++
			if r.Name == h1Name {
				t.Fatalf("expected oldest hourly snapshot (h1) to be cleaned up, found it still present")
			}
		case TypeAutoDaily:
			dailyCount++
		case TypeAutoWeekly:
			weeklyCount++
		}
	}
	if hourlyCount != 2 {
		t.Fatalf("expected 2 hourly snapshots to remain (keep=2), got %d", hourlyCount)
	}
	if dailyCount != 1 || weeklyCount != 1 {
		t.Fatalf("expected daily/weekly untouched (only 1 each, well within keep), got daily=%d weekly=%d", dailyCount, weeklyCount)
	}
	if len(runner.DeletedPaths) != 1 {
		t.Fatalf("expected exactly 1 subvolume deleted, got %d: %v", len(runner.DeletedPaths), runner.DeletedPaths)
	}
}

func TestSchedulerManualSnapshotNeverAutoDeleted(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	// Even clamped-degenerate keep counts (0) must never touch manual.
	enableAndMount(t, runner, store, vol, 0, 0, 0, 90)
	box.Set(vol)

	manualName := mustName(t, now.Add(-10000*time.Hour), TypeManual)
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, manualName, TypeManual, now.Add(-10000*time.Hour)))

	sched.RunOnce(context.Background())

	recs, _ := store.ListSnapshots(vol.UUID)
	found := false
	for _, r := range recs {
		if r.Name == manualName {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected manual snapshot to survive, but it was deleted")
	}
	if len(runner.DeletedPaths) != 0 {
		t.Fatalf("expected no deletions (manual is the only snapshot present), got %v", runner.DeletedPaths)
	}
}

func TestSchedulerPreopCleanedOnceProtectUntilExpires(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)

	preopName := mustName(t, now.Add(-3*time.Hour), TypePreop)
	expired := now.Add(-1 * time.Minute)
	rec := mkSnapNamed(vol.UUID, preopName, TypePreop, now.Add(-3*time.Hour))
	rec.ProtectUntil = &expired
	seedSnapshot(t, runner, store, vol, rec)

	sched.RunOnce(context.Background())

	recs, _ := store.ListSnapshots(vol.UUID)
	for _, r := range recs {
		if r.Name == preopName {
			t.Fatalf("expected expired preop snapshot to be cleaned up")
		}
	}
	if len(runner.DeletedPaths) != 1 {
		t.Fatalf("expected exactly 1 delete (the expired preop), got %v", runner.DeletedPaths)
	}
}

func TestSchedulerPreopNotYetExpiredIsKept(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)

	preopName := mustName(t, now.Add(-3*time.Hour), TypePreop)
	notYet := now.Add(1 * time.Hour)
	rec := mkSnapNamed(vol.UUID, preopName, TypePreop, now.Add(-3*time.Hour))
	rec.ProtectUntil = &notYet
	seedSnapshot(t, runner, store, vol, rec)

	sched.RunOnce(context.Background())

	if len(runner.DeletedPaths) != 0 {
		t.Fatalf("expected preop not yet past protect_until to survive, got deletes: %v", runner.DeletedPaths)
	}
}

func TestSchedulerCleansBeforeCreatingSameTick(t *testing.T) {
	// hourly_keep=2; 3 pre-existing hourly snapshots, the oldest due for
	// cleanup, and the newest old enough that a new hourly is *also* due
	// this same tick. "先清后建" means: cleanup trims to 2 (the pre-tick
	// newest two) THEN a new one is created — final count is 3, and the
	// survivor set proves cleanup ran against the pre-creation set, not a
	// set that already includes the new snapshot (which would instead
	// leave only 2 survivors).
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 2, 7, 4, 90)
	box.Set(vol)

	h1Name := mustName(t, now.Add(-3*time.Hour), TypeAutoHourly)
	h2Name := mustName(t, now.Add(-2*time.Hour), TypeAutoHourly)
	h3Name := mustName(t, now.Add(-90*time.Minute), TypeAutoHourly) // newest, and >=1h old => hourly due
	dName := mustName(t, now.Add(-time.Minute), TypeAutoDaily)      // recent: daily not due
	wName := mustName(t, now.Add(-time.Minute), TypeAutoWeekly)     // recent: weekly not due

	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, h1Name, TypeAutoHourly, now.Add(-3*time.Hour)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, h2Name, TypeAutoHourly, now.Add(-2*time.Hour)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, h3Name, TypeAutoHourly, now.Add(-90*time.Minute)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, dName, TypeAutoDaily, now.Add(-time.Minute)))
	seedSnapshot(t, runner, store, vol, mkSnapNamed(vol.UUID, wName, TypeAutoWeekly, now.Add(-time.Minute)))

	sched.RunOnce(context.Background())

	recs, _ := store.ListSnapshots(vol.UUID)
	hourlyRecs := 0
	for _, r := range recs {
		if r.Type == TypeAutoHourly {
			hourlyRecs++
		}
	}
	if hourlyRecs != 3 {
		var names []string
		for _, r := range recs {
			names = append(names, r.Name)
		}
		t.Fatalf("expected 3 hourly snapshots after clean-then-create (h2, h3, new), got %d: %v", hourlyRecs, names)
	}
	for _, r := range recs {
		if r.Name == h1Name {
			t.Fatalf("expected oldest hourly (h1) to have been cleaned up, but it survived")
		}
	}
	if len(runner.CreatedSnapshots) != 1 {
		t.Fatalf("expected exactly 1 new hourly snapshot created, got %d", len(runner.CreatedSnapshots))
	}
	if len(runner.DeletedPaths) != 1 {
		t.Fatalf("expected exactly 1 delete (h1), got %d: %v", len(runner.DeletedPaths), runner.DeletedPaths)
	}
}

func TestSchedulerSpaceGuardPausesSkipsAndPublishesOnce(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, usage, publisher, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)
	usage.SetPercent(vol.UUID, 95.0) // over the 90% threshold

	sched.RunOnce(context.Background())

	if len(runner.CreatedSnapshots) != 0 {
		t.Fatalf("expected creation to be skipped while paused, got %+v", runner.CreatedSnapshots)
	}
	if reason := sched.Pause.Reason(vol.UUID); reason == "" {
		t.Fatalf("expected PauseState to record a reason for %s", vol.UUID)
	}
	if publisher.CountByName(EventSnapshotPaused) != 1 {
		t.Fatalf("expected exactly 1 paused event, got %d", publisher.CountByName(EventSnapshotPaused))
	}

	// A second tick shortly after: still paused, still over threshold —
	// must NOT re-publish (24h throttle).
	clock.Advance(time.Minute)
	sched.RunOnce(context.Background())
	if publisher.CountByName(EventSnapshotPaused) != 1 {
		t.Fatalf("expected paused event to stay throttled at 1 within 24h, got %d", publisher.CountByName(EventSnapshotPaused))
	}
}

func TestSchedulerSpaceGuardResumesAutomaticallyAfterRecovery(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, usage, publisher, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)
	usage.SetPercent(vol.UUID, 95.0)

	sched.RunOnce(context.Background())
	if len(runner.CreatedSnapshots) != 0 {
		t.Fatalf("expected no creation while over threshold")
	}
	if sched.Pause.Reason(vol.UUID) == "" {
		t.Fatalf("expected paused")
	}

	// Usage drops back under threshold on the next tick.
	usage.SetPercent(vol.UUID, 40.0)
	clock.Advance(time.Minute)
	sched.RunOnce(context.Background())

	if sched.Pause.Reason(vol.UUID) != "" {
		t.Fatalf("expected pause to auto-clear on recovery, got reason=%q", sched.Pause.Reason(vol.UUID))
	}
	if len(runner.CreatedSnapshots) != 3 {
		t.Fatalf("expected creation to resume automatically after recovery (all 3 still due), got %d", len(runner.CreatedSnapshots))
	}

	// Pausing again later must publish a fresh event (the throttle window
	// is reset by the recovery in between).
	usage.SetPercent(vol.UUID, 95.0)
	clock.Advance(time.Minute)
	sched.RunOnce(context.Background())
	if publisher.CountByName(EventSnapshotPaused) != 2 {
		t.Fatalf("expected a fresh paused event after recovery reset the throttle, got %d", publisher.CountByName(EventSnapshotPaused))
	}
}

func TestSchedulerPauseThresholdClampedWhenPolicyCorrupt(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, usage, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	// Corrupt/out-of-range threshold (e.g. left over from a disable-then
	// partial-body PUT, per B2's review note): must clamp to
	// DefaultPauseThresholdPct (90), not be taken literally as "always
	// over threshold" (0).
	enableAndMount(t, runner, store, vol, 24, 7, 4, 0)
	box.Set(vol)
	usage.SetPercent(vol.UUID, 85.0) // below the clamped default of 90

	sched.RunOnce(context.Background())

	if sched.Pause.Reason(vol.UUID) != "" {
		t.Fatalf("expected no pause: 85%% is below the clamped default threshold of 90%%, got reason=%q", sched.Pause.Reason(vol.UUID))
	}
	if len(runner.CreatedSnapshots) != 3 {
		t.Fatalf("expected creation to proceed normally, got %d", len(runner.CreatedSnapshots))
	}
}

func TestSchedulerConsecutiveFailuresPublishAfterThreshold(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, publisher, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)

	armFailure := func(at time.Time) {
		destPath := SnapshotsDir(vol) + "/" + mustName(t, at, TypeAutoHourly)
		runner.SnapshotErr[destPath] = errors.New("boom")
	}

	armFailure(clock.Now())
	sched.RunOnce(context.Background()) // failure 1
	if publisher.CountByName(EventSnapshotFailed) != 0 {
		t.Fatalf("expected no failed event after 1 failure, got %d", publisher.CountByName(EventSnapshotFailed))
	}

	clock.Advance(time.Hour)
	armFailure(clock.Now())
	sched.RunOnce(context.Background()) // failure 2
	if publisher.CountByName(EventSnapshotFailed) != 0 {
		t.Fatalf("expected no failed event after 2 failures, got %d", publisher.CountByName(EventSnapshotFailed))
	}

	clock.Advance(time.Hour)
	armFailure(clock.Now())
	sched.RunOnce(context.Background()) // failure 3 -> threshold reached
	if publisher.CountByName(EventSnapshotFailed) != 1 {
		t.Fatalf("expected exactly 1 failed event after reaching the 3-failure threshold, got %d", publisher.CountByName(EventSnapshotFailed))
	}

	// A success resets the streak.
	clock.Advance(time.Hour) // hourly due again, succeeds this time (no injected error)
	sched.RunOnce(context.Background())
	if publisher.CountByName(EventSnapshotFailed) != 1 {
		t.Fatalf("expected failed event count to stay at 1 after a success resets the streak, got %d", publisher.CountByName(EventSnapshotFailed))
	}
}

func TestSchedulerDoesNotCacheVolumeListAcrossTicks(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	volA := schedTestVolume(t)
	volA.UUID = "vol-a"
	enableAndMount(t, runner, store, volA, 24, 7, 4, 90)

	volB := schedTestVolume(t)
	volB.UUID = "vol-b"
	enableAndMount(t, runner, store, volB, 24, 7, 4, 90)

	// Tick 1: only volA is "plugged in".
	box.Set(volA)
	sched.RunOnce(context.Background())
	recsA, _ := store.ListSnapshots(volA.UUID)
	if len(recsA) == 0 {
		t.Fatalf("expected volA to get its bootstrap snapshots on tick 1")
	}
	recsB, _ := store.ListSnapshots(volB.UUID)
	if len(recsB) != 0 {
		t.Fatalf("expected volB untouched on tick 1 (not yet enumerated), got %d records", len(recsB))
	}

	// Tick 2: volB hot-plugged in, volA hot-unplugged (simulating removal).
	box.Set(volB)
	sched.RunOnce(context.Background())
	recsB, _ = store.ListSnapshots(volB.UUID)
	if len(recsB) == 0 {
		t.Fatalf("expected volB to get its bootstrap snapshots on tick 2 (list must not be cached from tick 1)")
	}
}

func TestSchedulerRunRespectsContextCancellationAndTicks(t *testing.T) {
	now := mustTime(t, "2026-07-12T10:00:00Z")
	clock := NewFakeClock(now)
	sched, runner, store, _, _, box := newTestScheduler(t, clock)

	vol := schedTestVolume(t)
	enableAndMount(t, runner, store, vol, 24, 7, 4, 90)
	box.Set(vol)

	ticker := NewFakeTicker()
	sched.NewTicker = func(time.Duration) Ticker { return ticker }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(done)
	}()

	ticker.Tick(now)

	// Poll until the tick has been processed (creation happened), bounded
	// so a bug can't hang the test suite.
	deadline := time.After(2 * time.Second)
	for {
		recs, _ := store.ListSnapshots(vol.UUID)
		if len(recs) == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for scheduler to process the simulated tick")
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Run to return after context cancellation")
	}
	if !ticker.Stopped() {
		t.Fatalf("expected Run to Stop() the ticker on exit")
	}
}
