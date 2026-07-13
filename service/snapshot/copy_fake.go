package snapshot

import (
	"context"
	"sync"
)

// CopyCall records one successful Copy invocation for test assertions.
type CopyCall struct {
	Src, Dest string
	// Reflink is true if this call succeeded via the (simulated) reflink
	// path, false if it landed via the plain-copy fallback.
	Reflink bool
}

// FakeCopier is an in-memory Copier for tests: it records calls instead of
// touching disk, and lets tests script a reflink failure (falling back to a
// plain copy, exactly like ExecCopier) or a total failure (both attempts
// fail) per destination path.
type FakeCopier struct {
	mu sync.Mutex

	// ReflinkErr, keyed by dest, simulates a reflink attempt failing for
	// that destination (e.g. "invalid cross-device link" or "operation not
	// supported") — Copy then falls back to a plain copy, exactly as
	// ExecCopier does.
	ReflinkErr map[string]error
	// FallbackErr, keyed by dest, simulates the plain-copy fallback itself
	// also failing (only consulted when ReflinkErr[dest] is set).
	FallbackErr map[string]error

	Calls []CopyCall
}

var _ Copier = (*FakeCopier)(nil)

// NewFakeCopier returns a FakeCopier with no injected failures.
func NewFakeCopier() *FakeCopier {
	return &FakeCopier{
		ReflinkErr:  map[string]error{},
		FallbackErr: map[string]error{},
	}
}

func (f *FakeCopier) Copy(_ context.Context, src, dest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.ReflinkErr[dest]; err != nil {
		if fbErr := f.FallbackErr[dest]; fbErr != nil {
			return fbErr
		}
		f.Calls = append(f.Calls, CopyCall{Src: src, Dest: dest, Reflink: false})
		return nil
	}
	f.Calls = append(f.Calls, CopyCall{Src: src, Dest: dest, Reflink: true})
	return nil
}
