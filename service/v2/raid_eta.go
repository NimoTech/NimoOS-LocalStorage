package v2

import (
	"sync"
	"time"
)

// The kernel's finish=/speed= only count *copied* blocks. During a bitmap
// delta resync (re-added member) clean chunks are skipped without IO, so the
// copied-bytes rate collapses to ~0 and the kernel ETA balloons to weeks
// while the position races through the disk (seen live 2026-08-12:
// position +11%/20min with finish=60970min). The honest ETA basis is the
// *position* advance rate, sampled here across status polls.

type etaSample struct {
	at  time.Time
	pos int64
}

const (
	// etaWindow bounds how much history feeds the rate: long enough to
	// smooth throttling dips, short enough to react to phase changes
	// (delta-skip stretches vs real copying).
	etaWindow = 5 * time.Minute
	// etaMinSpan is the minimum age of the oldest sample before a rate is
	// trusted — two samples milliseconds apart produce garbage.
	etaMinSpan = 15 * time.Second
)

// etaFromSamples estimates remaining seconds from position samples of one
// rebuild. Returns -1 while there is no trustworthy rate yet. Pure.
func etaFromSamples(samples []etaSample, total int64) int64 {
	if len(samples) < 2 || total <= 0 {
		return -1
	}
	first, last := samples[0], samples[len(samples)-1]
	span := last.at.Sub(first.at)
	if span < etaMinSpan || last.pos <= first.pos {
		return -1
	}
	rate := float64(last.pos-first.pos) / span.Seconds() // units/sec
	remaining := float64(total - last.pos)
	if remaining <= 0 {
		return 0
	}
	return int64(remaining / rate)
}

// pruneEtaSamples drops samples outside the window and resets history when a
// new rebuild started (position moved backwards or the total changed). Pure.
func pruneEtaSamples(samples []etaSample, next etaSample, prevTotal, total int64) []etaSample {
	if prevTotal != total || (len(samples) > 0 && next.pos < samples[len(samples)-1].pos) {
		samples = nil
	}
	samples = append(samples, next)
	cutoff := next.at.Add(-etaWindow)
	i := 0
	for i < len(samples)-1 && samples[i].at.Before(cutoff) {
		i++
	}
	return samples[i:]
}

type etaTracker struct {
	mu      sync.Mutex
	samples map[string][]etaSample // md name → history
	totals  map[string]int64
}

func newEtaTracker() *etaTracker {
	return &etaTracker{samples: map[string][]etaSample{}, totals: map[string]int64{}}
}

// Observe records a rebuild position and returns the current ETA estimate in
// seconds (-1 = not enough data yet).
func (t *etaTracker) Observe(md string, pos, total int64) int64 {
	if pos <= 0 || total <= 0 {
		return -1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	next := etaSample{at: time.Now(), pos: pos}
	t.samples[md] = pruneEtaSamples(t.samples[md], next, t.totals[md], total)
	t.totals[md] = total
	return etaFromSamples(t.samples[md], total)
}

// Forget clears history once a rebuild is over.
func (t *etaTracker) Forget(md string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.samples, md)
	delete(t.totals, md)
}
