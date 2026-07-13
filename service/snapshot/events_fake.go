package snapshot

import (
	"context"
	"sync"
)

// FakePublishedEvent records one Publish call, for test assertions.
type FakePublishedEvent struct {
	Name       string
	Properties map[string]string
}

// FakePublisher is an in-memory EventPublisher for tests.
type FakePublisher struct {
	mu sync.Mutex

	// Err, if set, is returned by every Publish call (and the call is still
	// recorded in Events, mirroring how a real publish failure doesn't
	// un-happen the attempt).
	Err error

	Events []FakePublishedEvent
}

// NewFakePublisher returns an empty FakePublisher.
func NewFakePublisher() *FakePublisher {
	return &FakePublisher{}
}

func (f *FakePublisher) Publish(_ context.Context, name string, properties map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	cp := make(map[string]string, len(properties))
	for k, v := range properties {
		cp[k] = v
	}
	f.Events = append(f.Events, FakePublishedEvent{Name: name, Properties: cp})
	return f.Err
}

// CountByName returns how many recorded events have the given name.
func (f *FakePublisher) CountByName(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.Events {
		if e.Name == name {
			n++
		}
	}
	return n
}
