package snapshot

import "sync"

// PauseState tracks, per volume, the live "why automatic snapshots are
// currently paused" reason the space guard (Scheduler) recomputes every
// tick. It backs VolumeStatus.PausedReason (handoff §3.4's GET
// /v2/snapshot/volumes field) — a placeholder B2 left permanently empty
// pending this task ("PausedReason is permanently empty, B3 takes it over").
//
// A nil *PauseState behaves as "nothing is ever paused" (all methods are
// nil-receiver safe), so existing callers/tests built before this task
// (e.g. service_test.go's newTestService, which doesn't set Pause) keep
// working unchanged.
type PauseState struct {
	mu      sync.RWMutex
	reasons map[string]string
}

// NewPauseState returns an empty PauseState (nothing paused).
func NewPauseState() *PauseState {
	return &PauseState{reasons: map[string]string{}}
}

// Reason returns volumeUUID's current pause reason, or "" if it isn't
// currently paused.
func (p *PauseState) Reason(volumeUUID string) string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.reasons[volumeUUID]
}

// Set records volumeUUID as currently paused, with reason.
func (p *PauseState) Set(volumeUUID, reason string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reasons[volumeUUID] = reason
}

// Clear marks volumeUUID as no longer paused (automatic resume — handoff
// §3.3: "resumes automatically once recovered").
func (p *PauseState) Clear(volumeUUID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.reasons, volumeUUID)
}
