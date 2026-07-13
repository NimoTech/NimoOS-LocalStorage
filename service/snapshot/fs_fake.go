package snapshot

import (
	"sync"
)

// FakePathChecker is an in-memory PathChecker for tests: no real disk, just
// a set of paths that "exist" plus their metadata.
type FakePathChecker struct {
	mu    sync.Mutex
	files map[string]PathInfo

	// MkdirAllCalls records every directory MkdirAll was asked to create.
	MkdirAllCalls []string
	// MkdirAllErr, if non-nil, is returned by MkdirAll unconditionally.
	MkdirAllErr error
	// ExistsErr, keyed by path, lets tests inject an Exists failure.
	ExistsErr map[string]error
	// StatErr, keyed by path, lets tests inject a Stat failure.
	StatErr map[string]error
}

var _ PathChecker = (*FakePathChecker)(nil)

// NewFakePathChecker returns an empty FakePathChecker.
func NewFakePathChecker() *FakePathChecker {
	return &FakePathChecker{
		files:     map[string]PathInfo{},
		ExistsErr: map[string]error{},
		StatErr:   map[string]error{},
	}
}

// Seed marks path as existing with the given metadata.
func (f *FakePathChecker) Seed(path string, info PathInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = info
}

func (f *FakePathChecker) Exists(path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ExistsErr[path]; err != nil {
		return false, err
	}
	_, ok := f.files[path]
	return ok, nil
}

func (f *FakePathChecker) Stat(path string) (PathInfo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.StatErr[path]; err != nil {
		return PathInfo{}, false, err
	}
	info, ok := f.files[path]
	return info, ok, nil
}

func (f *FakePathChecker) MkdirAll(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MkdirAllCalls = append(f.MkdirAllCalls, dir)
	if f.MkdirAllErr != nil {
		return f.MkdirAllErr
	}
	if _, ok := f.files[dir]; !ok {
		f.files[dir] = PathInfo{IsDir: true}
	}
	return nil
}
