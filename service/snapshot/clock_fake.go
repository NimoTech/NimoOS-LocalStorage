package snapshot

import (
	"sync"
	"time"
)

// FakeClock is a settable, concurrency-safe Clock for tests.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a FakeClock initialized to now.
func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set pins the fake clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// FakeTicker is a manually-driven Ticker for tests exercising Scheduler.Run's
// goroutine loop without waiting on a real TickInterval: call Tick to
// simulate one wakeup.
type FakeTicker struct {
	ch chan time.Time

	mu      sync.Mutex
	stopped bool
}

// NewFakeTicker returns a FakeTicker with a buffered channel (capacity 1) so
// a test can call Tick without the scheduler having already reached its
// select statement.
func NewFakeTicker() *FakeTicker {
	return &FakeTicker{ch: make(chan time.Time, 1)}
}

func (f *FakeTicker) C() <-chan time.Time { return f.ch }

func (f *FakeTicker) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

// Stopped reports whether Stop has been called (test assertion helper).
func (f *FakeTicker) Stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// Tick simulates one ticker wakeup at t.
func (f *FakeTicker) Tick(t time.Time) {
	f.ch <- t
}
