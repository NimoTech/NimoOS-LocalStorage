package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"go.uber.org/zap"
)

// TickInterval is how often the scheduler wakes to evaluate every known
// volume (handoff §3.3: "an in-process goroutine ticker, waking once a minute
// to evaluate, without depending on the system cron").
const TickInterval = time.Minute

// Cadence periods for the three automatic snapshot types (handoff §3.3: "if
// time since that volume's most recent snapshot at that tier ≥ the period → create one").
const (
	HourlyPeriod = time.Hour
	DailyPeriod  = 24 * time.Hour
	WeeklyPeriod = 7 * 24 * time.Hour
)

var cadencePeriods = map[string]time.Duration{
	TypeAutoHourly: HourlyPeriod,
	TypeAutoDaily:  DailyPeriod,
	TypeAutoWeekly: WeeklyPeriod,
}

// cadenceOrder fixes iteration order for deterministic due-list output
// (map iteration order in Go is randomized).
var cadenceOrder = []string{TypeAutoHourly, TypeAutoDaily, TypeAutoWeekly}

// FailureEventThreshold is how many consecutive creation failures for a
// volume trigger nimoos:snapshot:failed (handoff §3.5: "only send after ≥3 consecutive failures").
const FailureEventThreshold = 3

// FailureEventThrottle and PausedEventThrottle bound how often the
// failed/paused MessageBus events repeat for the same volume while the
// underlying condition persists, so a stuck volume doesn't spam the
// bus/UI once a minute forever (handoff §3.5: "throttled" / "the same alert
// once per 24h"). The handoff only states an explicit window for the paused alert
// (24h); FailureEventThrottle reuses the same window for consistency —
// there's no stated reason a persistent creation failure deserves a
// different alert cadence than a persistent space shortage.
const (
	FailureEventThrottle = 24 * time.Hour
	PausedEventThrottle  = 24 * time.Hour
)

// VolumeListerFunc enumerates every volume the scheduler should consider
// this tick. It must not cache: handoff §3.3 requires every tick to
// re-enumerate "mounted btrfs volumes with the policy enabled" freshly, since volumes can be
// hot-plugged/removed between ticks. Scheduler never caches its result
// itself, either — it calls this on every RunOnce.
type VolumeListerFunc func(ctx context.Context) ([]VolumeInfo, error)

// SchedulerConfig is Scheduler's construction parameters. Runner/Store/
// Persister/Pause should be the same instances Service (service.go) uses,
// so a manual snapshot created through the API and an automatic one
// created by the scheduler observe/mutate identical state.
type SchedulerConfig struct {
	Runner      Runner
	Store       FullStore
	Persister   FstabPersister
	Pause       *PauseState
	ListVolumes VolumeListerFunc
	Usage       UsageProvider
	Publisher   EventPublisher

	// Clock and NewTicker are test injection points; both default to real
	// wall-clock behavior when left nil.
	Clock     Clock
	NewTicker func(d time.Duration) Ticker
}

// Scheduler is the autonomous layer (handoff §3.3): a goroutine that wakes
// every TickInterval and, for every currently-mounted, policy-enabled
// btrfs volume, cleans up over-retention/expired snapshots, then creates
// any snapshot cadence that's due — guarded by a free-space check and
// consecutive-failure/space-guard MessageBus alerts (handoff §3.5).
type Scheduler struct {
	Runner      Runner
	Store       FullStore
	Persister   FstabPersister
	Pause       *PauseState
	ListVolumes VolumeListerFunc
	Usage       UsageProvider
	Publisher   EventPublisher
	Clock       Clock
	NewTicker   func(d time.Duration) Ticker

	mu             sync.Mutex
	failures       map[string]*failureState
	pausedEventsAt map[string]time.Time
}

type failureState struct {
	count       int
	lastErr     string
	lastEventAt time.Time
}

// NewScheduler builds a Scheduler from cfg, defaulting Clock/NewTicker to
// real implementations when left unset.
func NewScheduler(cfg SchedulerConfig) *Scheduler {
	s := &Scheduler{
		Runner:         cfg.Runner,
		Store:          cfg.Store,
		Persister:      cfg.Persister,
		Pause:          cfg.Pause,
		ListVolumes:    cfg.ListVolumes,
		Usage:          cfg.Usage,
		Publisher:      cfg.Publisher,
		Clock:          cfg.Clock,
		NewTicker:      cfg.NewTicker,
		failures:       map[string]*failureState{},
		pausedEventsAt: map[string]time.Time{},
	}
	if s.Clock == nil {
		s.Clock = RealClock{}
	}
	if s.NewTicker == nil {
		s.NewTicker = NewRealTicker
	}
	return s
}

// Run blocks, waking every TickInterval (or whatever Scheduler.NewTicker
// produces) until ctx is done. Its lifecycle follows the owning service's:
// callers start it with `go scheduler.Run(ctx)` using the same ctx the
// service cancels on shutdown (handoff/B3 brief: "follows the service's start/stop").
func (s *Scheduler) Run(ctx context.Context) {
	ticker := s.NewTicker(TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			s.RunOnce(ctx)
		}
	}
}

// RunOnce performs one full tick: enumerate every currently-known volume
// (no caching — hot-plug safe) and evaluate each independently.
func (s *Scheduler) RunOnce(ctx context.Context) {
	if s.ListVolumes == nil {
		return
	}
	volumes, err := s.ListVolumes(ctx)
	if err != nil {
		logger.Error("snapshot scheduler: failed to enumerate volumes", zap.Error(err))
		return
	}

	now := s.Clock.Now()
	for _, v := range volumes {
		s.tickVolume(ctx, v, now)
	}
}

// tickVolume evaluates one volume for one tick: eligibility gates, then
// retention cleanup, then (space-guard-gated) creation of due cadences —
// in that order ("clean before create", handoff §3.3).
func (s *Scheduler) tickVolume(ctx context.Context, v VolumeInfo, now time.Time) {
	if !strings.EqualFold(v.Filesystem, "btrfs") {
		return
	}

	mounted, err := s.Runner.IsMounted(v.DevicePath, v.MountPoint)
	if err != nil {
		logger.Error("snapshot scheduler: failed to check mount state",
			zap.String("volume_uuid", v.UUID), zap.Error(err))
		return
	}
	if !mounted {
		return
	}

	policy, err := s.Store.GetOrCreatePolicy(v.UUID)
	if err != nil {
		logger.Error("snapshot scheduler: failed to load policy",
			zap.String("volume_uuid", v.UUID), zap.Error(err))
		return
	}
	if !policy.Enabled {
		return
	}

	status, err := EnsureSnapshotsMount(ctx, s.Runner, s.Persister, v)
	if err != nil {
		logger.Error("snapshot scheduler: failed to ensure @snapshots mount",
			zap.String("volume_uuid", v.UUID), zap.Error(err))
		return
	}
	if !status.Supported {
		logger.Info("snapshot scheduler: volume does not support snapshots",
			zap.String("volume_uuid", v.UUID), zap.String("reason", status.Reason))
		return
	}

	if _, err := ReconcileVolume(ctx, s.Runner, s.Store, v); err != nil {
		logger.Error("snapshot scheduler: reconcile failed",
			zap.String("volume_uuid", v.UUID), zap.Error(err))
		return
	}

	snaps, err := s.Store.ListSnapshots(v.UUID)
	if err != nil {
		logger.Error("snapshot scheduler: failed to list snapshots",
			zap.String("volume_uuid", v.UUID), zap.Error(err))
		return
	}

	// Clean before create ("clean before create"): retention/preop-expiry deletes are
	// applied against the pre-tick snapshot set before any new snapshot
	// this tick is considered, so a just-created snapshot can never be
	// mistaken for one of the N-1 that should have already been trimmed.
	s.cleanup(ctx, v, snaps, *policy, now)

	due := computeDueTypes(now, lastByType(snaps))
	if len(due) == 0 {
		return
	}

	s.createDue(ctx, v, *policy, due, now)
}

// cleanup deletes every snapshot selectRetentionVictims flags for v's
// current snapshot set.
func (s *Scheduler) cleanup(ctx context.Context, v VolumeInfo, snaps []model.Snapshot, policy model.SnapshotPolicy, now time.Time) {
	victims := selectRetentionVictims(snaps, policy, now)
	for _, victim := range victims {
		path, err := ResolveSnapshotPath(v, victim.Name)
		if err != nil {
			logger.Error("snapshot scheduler: retention skip - invalid name",
				zap.String("volume_uuid", v.UUID), zap.String("name", victim.Name), zap.Error(err))
			continue
		}
		if err := s.Runner.DeleteSubvolume(ctx, path); err != nil {
			logger.Error("snapshot scheduler: retention delete failed",
				zap.String("volume_uuid", v.UUID), zap.String("name", victim.Name), zap.Error(err))
			continue
		}
		if err := s.Store.DeleteSnapshot(victim.ID); err != nil {
			logger.Error("snapshot scheduler: retention db delete failed",
				zap.String("volume_uuid", v.UUID), zap.String("name", victim.Name), zap.Error(err))
		}
	}
}

// createDue applies the space guard once per volume per tick (handoff
// §3.3: "check volume usage before creating"), then attempts every due cadence if the volume
// isn't over threshold.
func (s *Scheduler) createDue(ctx context.Context, v VolumeInfo, policy model.SnapshotPolicy, due []string, now time.Time) {
	threshold := clampPauseThreshold(policy.PauseThresholdPct)

	if s.Usage != nil {
		pct, err := s.Usage.UsagePercent(ctx, v)
		if err != nil {
			// Fail open: an inability to determine usage isn't grounds to
			// skip protecting the volume with a snapshot — the space guard
			// exists to protect free space, not to gate the feature's
			// primary purpose on a secondary signal.
			//
			// Deliberately NOT calling resolvePause here: a usage query is
			// most likely to error exactly when the filesystem is
			// critically full/damaged — the moment an existing,
			// data-backed pause verdict matters most. Wiping that pause
			// (and resetting its 24h alert throttle window) on a mere
			// query error, rather than a confirmed recovery, would let a
			// transient query hiccup erase a real "this volume is full"
			// signal. So on error we leave PauseState exactly as it was
			// and still fall through to attempt creation (fail-open).
			logger.Info("snapshot scheduler: usage check failed, proceeding without space guard",
				zap.String("volume_uuid", v.UUID), zap.Error(err))
		} else if pct > float64(threshold) {
			s.pause(v.UUID, fmt.Sprintf("volume usage %.1f%% exceeds pause threshold %d%%", pct, threshold), now)
			return
		} else {
			s.resolvePause(v.UUID)
		}
	}

	for _, snapType := range due {
		s.createOne(ctx, v, snapType, now)
	}
}

func (s *Scheduler) pause(volumeUUID, reason string, now time.Time) {
	s.Pause.Set(volumeUUID, reason)

	s.mu.Lock()
	last, seen := s.pausedEventsAt[volumeUUID]
	shouldPublish := !seen || now.Sub(last) >= PausedEventThrottle
	if shouldPublish {
		s.pausedEventsAt[volumeUUID] = now
	}
	s.mu.Unlock()

	if !shouldPublish || s.Publisher == nil {
		return
	}
	if err := s.Publisher.Publish(context.Background(), EventSnapshotPaused, map[string]string{
		"volume": volumeUUID,
		"reason": reason,
	}); err != nil {
		logger.Error("snapshot scheduler: failed to publish paused event",
			zap.String("volume_uuid", volumeUUID), zap.Error(err))
	}
}

// resolvePause clears any pause state for volumeUUID and resets its
// throttle window, so a fresh 24h window starts the next time this volume
// pauses (handoff: "resumes automatically once recovered" — recovery isn't just "stop skipping
// creation", it's a clean slate for future alerts too).
func (s *Scheduler) resolvePause(volumeUUID string) {
	s.Pause.Clear(volumeUUID)
	s.mu.Lock()
	delete(s.pausedEventsAt, volumeUUID)
	s.mu.Unlock()
}

func (s *Scheduler) createOne(ctx context.Context, v VolumeInfo, snapType string, now time.Time) {
	name, err := FormatName(now, snapType, "")
	if err != nil {
		s.recordFailure(v.UUID, snapType, err, now)
		return
	}
	destPath := filepath.Join(SnapshotsDir(v), name)

	if err := s.Runner.CreateReadOnlySnapshot(ctx, v.MountPoint, destPath); err != nil {
		s.recordFailure(v.UUID, snapType, fmt.Errorf("create snapshot: %w", err), now)
		return
	}

	rec := model.Snapshot{
		VolumeUUID: v.UUID,
		Name:       name,
		Type:       snapType,
		CreatedAt:  now,
		CreatedBy:  "system",
	}
	if err := s.Store.InsertSnapshot(rec); err != nil {
		// The subvolume now exists on disk without a DB row; the next
		// reconcile pass (this scheduler's own next tick, or any API call
		// that reconciles) backfills a record from the name itself
		// (reconcile.go's buildUnknownSnapshot recognizes our own naming
		// convention), so this isn't a permanent leak — just deferred
		// bookkeeping. Still counts as a failure for alerting purposes.
		logger.Error("snapshot scheduler: created snapshot but failed to record it",
			zap.String("volume_uuid", v.UUID), zap.String("name", name), zap.Error(err))
		s.recordFailure(v.UUID, snapType, err, now)
		return
	}

	s.recordSuccess(v.UUID, snapType)
	if s.Publisher == nil {
		return
	}
	if err := s.Publisher.Publish(context.Background(), EventSnapshotCreated, map[string]string{
		"volume": v.UUID,
		"name":   name,
		"type":   snapType,
	}); err != nil {
		logger.Error("snapshot scheduler: failed to publish created event",
			zap.String("volume_uuid", v.UUID), zap.Error(err))
	}
}

// failureKey scopes the consecutive-failure streak to (volume, cadence
// type): hourly/daily/weekly creations for the same volume are independent
// attempts, so a daily creation succeeding must not silently reset a
// separately-persisting hourly failure streak (discovered via TDD —
// TestSchedulerConsecutiveFailuresPublishAfterThreshold originally failed
// against a per-volume-only counter, because the very first tick that
// creates the bootstrap hourly/daily/weekly trio has hourly fail while
// daily/weekly succeed, and the daily/weekly successes were wiping the
// hourly failure count within the same tick). The published event still
// only carries {volume, error} (handoff §3.5's payload shape) — type is
// tracking-internal only.
func failureKey(volumeUUID, snapType string) string {
	return volumeUUID + "\x00" + snapType
}

// recordFailure tracks a consecutive-failure streak per (volume, cadence
// type) and publishes nimoos:snapshot:failed once the streak reaches
// FailureEventThreshold, throttled to at most once per FailureEventThrottle
// while the streak continues (handoff §3.5: "only send after ≥3 consecutive failures, throttled").
func (s *Scheduler) recordFailure(volumeUUID, snapType string, cause error, now time.Time) {
	key := failureKey(volumeUUID, snapType)

	s.mu.Lock()
	fs, ok := s.failures[key]
	if !ok {
		fs = &failureState{}
		s.failures[key] = fs
	}
	fs.count++
	fs.lastErr = cause.Error()
	shouldPublish := fs.count >= FailureEventThreshold &&
		(fs.lastEventAt.IsZero() || now.Sub(fs.lastEventAt) >= FailureEventThrottle)
	if shouldPublish {
		fs.lastEventAt = now
	}
	errMsg := fs.lastErr
	s.mu.Unlock()

	if !shouldPublish || s.Publisher == nil {
		return
	}
	if err := s.Publisher.Publish(context.Background(), EventSnapshotFailed, map[string]string{
		"volume": volumeUUID,
		"error":  errMsg,
	}); err != nil {
		logger.Error("snapshot scheduler: failed to publish failed event",
			zap.String("volume_uuid", volumeUUID), zap.Error(err))
	}
}

func (s *Scheduler) recordSuccess(volumeUUID, snapType string) {
	s.mu.Lock()
	delete(s.failures, failureKey(volumeUUID, snapType))
	s.mu.Unlock()
}

// --- pure time-wheel logic (handoff §3.3 / task-B3 brief TDD target) -------

// lastByType returns, for each type present in snaps, the most recent
// CreatedAt.
func lastByType(snaps []model.Snapshot) map[string]time.Time {
	out := map[string]time.Time{}
	for _, s := range snaps {
		if cur, ok := out[s.Type]; !ok || s.CreatedAt.After(cur) {
			out[s.Type] = s.CreatedAt
		}
	}
	return out
}

// computeDueTypes decides which of the three automatic cadences are due to
// create a new snapshot right now: due if no snapshot of that type exists
// yet (bootstrap — a freshly-enabled volume gets all three cadences on its
// first eligible tick), or if the most recent one is at least a full period
// old (handoff §3.3: "if time since the most recent snapshot ≥ the period → create one"). Pure function, no
// I/O — the RED/GREEN target for the time-wheel's "which cadences fire"
// half.
func computeDueTypes(now time.Time, last map[string]time.Time) []string {
	var due []string
	for _, t := range cadenceOrder {
		period := cadencePeriods[t]
		lastAt, ok := last[t]
		if !ok || now.Sub(lastAt) >= period {
			due = append(due, t)
		}
	}
	return due
}

// clampKeep defensively clamps a keep-count read from policy: a
// non-positive value (which should never legitimately occur, but a
// full-replace PUT with a stale/zero-valued body — see B2's review note on
// pause_threshold_pct — could in principle also leave HourlyKeep/DailyKeep/
// WeeklyKeep at 0) falls back to def rather than being taken literally,
// which for keep=0 would mean "delete every snapshot of this type every
// tick".
func clampKeep(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// clampPauseThreshold defensively clamps PauseThresholdPct read from a
// stored policy to DefaultPauseThresholdPct when it's <= 0 or > 100
// (task-B3 brief / B2 review note: "PUT policy is full-replace semantics;
// pause_threshold_pct can end up stored out-of-range when enabled=false").
func clampPauseThreshold(v int) int {
	if v <= 0 || v > 100 {
		return DefaultPauseThresholdPct
	}
	return v
}

// selectRetentionVictims is the pure retention-cleanup decision: given a
// volume's current snapshot set, which ones should be deleted right now.
//
//   - auto-hourly/auto-daily/auto-weekly: keep the newest
//     HourlyKeep/DailyKeep/WeeklyKeep (clamped — see clampKeep) per type;
//     everything older within that same type is a victim. Types are
//     never mixed (handoff: "only delete matching type").
//   - preop: a victim once its ProtectUntil has passed, regardless of any
//     keep count (handoff: "preop is cleaned up once protect_until expires").
//   - manual (and unknown): never a victim — not present in the keep-N
//     map at all, and TypeUnknown/TypePreop-without-ProtectUntil are
//     likewise left alone.
//
// Pure function, no I/O; Scheduler.cleanup performs the actual deletes for
// whatever this returns.
func selectRetentionVictims(snaps []model.Snapshot, policy model.SnapshotPolicy, now time.Time) []model.Snapshot {
	var victims []model.Snapshot

	keepFor := map[string]int{
		TypeAutoHourly: clampKeep(policy.HourlyKeep, DefaultHourlyKeep),
		TypeAutoDaily:  clampKeep(policy.DailyKeep, DefaultDailyKeep),
		TypeAutoWeekly: clampKeep(policy.WeeklyKeep, DefaultWeeklyKeep),
	}

	for _, snapType := range cadenceOrder {
		keep := keepFor[snapType]
		var group []model.Snapshot
		for _, snap := range snaps {
			if snap.Type == snapType {
				group = append(group, snap)
			}
		}
		if len(group) <= keep {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].CreatedAt.After(group[j].CreatedAt) })
		victims = append(victims, group[keep:]...)
	}

	for _, snap := range snaps {
		if snap.Type == TypePreop && snap.ProtectUntil != nil && !snap.ProtectUntil.After(now) {
			victims = append(victims, snap)
		}
	}

	return victims
}
