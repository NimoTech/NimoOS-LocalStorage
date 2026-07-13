package snapshot

import "testing"

func TestPauseStateSetReasonClear(t *testing.T) {
	p := NewPauseState()
	if got := p.Reason("vol-1"); got != "" {
		t.Fatalf("expected empty reason initially, got %q", got)
	}

	p.Set("vol-1", "usage 95% exceeds threshold 90%")
	if got := p.Reason("vol-1"); got != "usage 95% exceeds threshold 90%" {
		t.Fatalf("unexpected reason: %q", got)
	}
	// Unrelated volume unaffected.
	if got := p.Reason("vol-2"); got != "" {
		t.Fatalf("expected vol-2 unaffected, got %q", got)
	}

	p.Clear("vol-1")
	if got := p.Reason("vol-1"); got != "" {
		t.Fatalf("expected reason cleared, got %q", got)
	}
}

func TestPauseStateNilReceiverSafe(t *testing.T) {
	var p *PauseState
	if got := p.Reason("vol-1"); got != "" {
		t.Fatalf("expected nil PauseState to report empty reason, got %q", got)
	}
	// Must not panic.
	p.Set("vol-1", "whatever")
	p.Clear("vol-1")
}
