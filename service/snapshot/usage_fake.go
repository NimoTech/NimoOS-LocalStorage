package snapshot

import (
	"context"
	"sync"
)

// FakeUsageProvider is an in-memory UsageProvider for tests: a settable
// percentage per volume (default 0, i.e. "empty"), or an injectable error.
type FakeUsageProvider struct {
	mu sync.Mutex

	Percent map[string]float64
	Err     map[string]error

	// Calls records every UsagePercent(volume) invocation, in order, for
	// tests asserting how often (or how freshly) the space guard checks
	// usage.
	Calls []string
}

// NewFakeUsageProvider returns a FakeUsageProvider reporting 0% used for any
// volume not otherwise configured.
func NewFakeUsageProvider() *FakeUsageProvider {
	return &FakeUsageProvider{Percent: map[string]float64{}, Err: map[string]error{}}
}

// SetPercent configures UsagePercent's return value for volumeUUID.
func (f *FakeUsageProvider) SetPercent(volumeUUID string, pct float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Percent[volumeUUID] = pct
}

// SetErr configures UsagePercent to fail for volumeUUID.
func (f *FakeUsageProvider) SetErr(volumeUUID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Err[volumeUUID] = err
}

func (f *FakeUsageProvider) UsagePercent(_ context.Context, volume VolumeInfo) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, volume.UUID)
	if err := f.Err[volume.UUID]; err != nil {
		return 0, err
	}
	return f.Percent[volume.UUID], nil
}
