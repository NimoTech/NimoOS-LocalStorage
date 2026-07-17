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
	// RenameErr, keyed by oldPath, lets tests inject a Rename failure (e.g.
	// simulating restoreOverwrite's rename-into-place step failing after a
	// successful temp copy).
	RenameErr map[string]error
	// RemoveErr, keyed by path, lets tests inject a Remove failure.
	RemoveErr map[string]error
	// RenameCalls/RemoveCalls record every Rename/Remove invocation (in
	// "old -> new" / "path" form) for assertions.
	RenameCalls []string
	RemoveCalls []string
}

var _ PathChecker = (*FakePathChecker)(nil)

// NewFakePathChecker returns an empty FakePathChecker.
func NewFakePathChecker() *FakePathChecker {
	return &FakePathChecker{
		files:     map[string]PathInfo{},
		ExistsErr: map[string]error{},
		StatErr:   map[string]error{},
		RenameErr: map[string]error{},
		RemoveErr: map[string]error{},
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

// Rename does not require oldPath to have been previously Seed()ed or
// otherwise tracked: FakeCopier (the seam restoreOverwrite copies its
// temporary file through) and FakePathChecker are deliberately independent
// fakes that don't share any in-memory filesystem model, so a successful
// FakeCopier.Copy to oldPath is invisible to this FakePathChecker. Treating
// an untracked oldPath as "exists with zero-value metadata" instead of
// erroring keeps this fake usable for what these tests actually check —
// that Rename was called with the right old/new paths, in the right order
// relative to Copy — without requiring every test to separately Seed the
// temporary path Copy "created".
func (f *FakePathChecker) Rename(oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RenameCalls = append(f.RenameCalls, oldPath+" -> "+newPath)
	if err := f.RenameErr[oldPath]; err != nil {
		return err
	}
	info := f.files[oldPath]
	delete(f.files, oldPath)
	f.files[newPath] = info
	return nil
}

func (f *FakePathChecker) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemoveCalls = append(f.RemoveCalls, path)
	if err := f.RemoveErr[path]; err != nil {
		return err
	}
	delete(f.files, path)
	return nil
}
