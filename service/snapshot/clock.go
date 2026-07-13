package snapshot

import "time"

// Clock abstracts "now" so the scheduler's due/retention decisions can be
// driven by controlled, test-injected time instead of real wall-clock waits
// (handoff §3.3 / task-B3 brief: "可注入时钟...以便单测").
type Clock interface {
	Now() time.Time
}

// RealClock is the production Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Ticker abstracts time.Ticker so Scheduler.Run's wakeup loop can be driven
// by a fake, test-controlled channel instead of waiting on TickInterval.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realTicker struct {
	t *time.Ticker
}

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// NewRealTicker is the production Ticker constructor, used as Scheduler's
// default NewTicker.
func NewRealTicker(d time.Duration) Ticker {
	return realTicker{t: time.NewTicker(d)}
}
