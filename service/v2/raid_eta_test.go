package v2

import (
	"testing"
	"time"
)

func TestEtaFromSamples(t *testing.T) {
	t0 := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	mk := func(offsetSec int, pos int64) etaSample {
		return etaSample{at: t0.Add(time.Duration(offsetSec) * time.Second), pos: pos}
	}

	// The live incident: delta resync raced +11% of a 976630272-unit device
	// in ~20min while the kernel's copied-bytes ETA said 60970min. Position
	// rate: (300818560-187553152)/1200s ≈ 94388/s → remaining ≈ 2h, not 42d.
	samples := []etaSample{mk(0, 187553152), mk(1200, 300818560)}
	eta := etaFromSamples(samples, 976630272)
	if eta < 6*3600 && eta > 0 {
		// ~7159s expected
	} else {
		t.Errorf("eta = %d, want ≈7159s", eta)
	}
	if eta < 7000 || eta > 7400 {
		t.Errorf("eta = %d, want ≈7159s", eta)
	}

	// Not enough span → unknown.
	if got := etaFromSamples([]etaSample{mk(0, 100), mk(5, 200)}, 1000); got != -1 {
		t.Errorf("short span: got %d, want -1", got)
	}
	// No progress → unknown (recovery pending / stalled), never ∞.
	if got := etaFromSamples([]etaSample{mk(0, 100), mk(60, 100)}, 1000); got != -1 {
		t.Errorf("stalled: got %d, want -1", got)
	}
	if got := etaFromSamples(nil, 1000); got != -1 {
		t.Errorf("empty: got %d, want -1", got)
	}
}

func TestPruneEtaSamples(t *testing.T) {
	t0 := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	mk := func(offsetSec int, pos int64) etaSample {
		return etaSample{at: t0.Add(time.Duration(offsetSec) * time.Second), pos: pos}
	}
	// Window keeps ~5min of history.
	s := []etaSample{mk(0, 1), mk(60, 2), mk(400, 3)}
	got := pruneEtaSamples(s, mk(700, 4), 1000, 1000)
	if len(got) != 2 || got[0].pos != 3 {
		t.Errorf("window prune: got %+v", got)
	}
	// New rebuild (position went backwards) resets history.
	got = pruneEtaSamples([]etaSample{mk(0, 900)}, mk(60, 10), 1000, 1000)
	if len(got) != 1 || got[0].pos != 10 {
		t.Errorf("backward reset: got %+v", got)
	}
	// Total changed (different rebuild target) resets history.
	got = pruneEtaSamples([]etaSample{mk(0, 900)}, mk(60, 950), 1000, 2000)
	if len(got) != 1 {
		t.Errorf("total change reset: got %+v", got)
	}
}
