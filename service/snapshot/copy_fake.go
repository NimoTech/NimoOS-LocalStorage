package snapshot

import (
	"context"
	"os"
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
	// RealCopyFiles, when true, makes a "successful" Copy actually read src
	// and write its bytes to dest (os.ReadFile/os.WriteFile — files only,
	// not directories) instead of just recording the call. Most of this
	// package's tests leave this false (the whole point of FakeCopier is
	// not touching disk), but restore's on_conflict=overwrite path
	// (restore.go's restoreOverwrite) relies on its temporary file
	// genuinely existing on disk afterward — its very next step is
	// PathChecker.Rename(tempPath, dest), a real os.Rename when Paths is
	// OSPathChecker — so tests exercising that rename-into-place step (and
	// tests that want to assert the final destination's actual content)
	// need this set to true.
	RealCopyFiles bool

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
		if f.RealCopyFiles {
			if err := realCopyFile(src, dest); err != nil {
				return err
			}
		}
		f.Calls = append(f.Calls, CopyCall{Src: src, Dest: dest, Reflink: false})
		return nil
	}
	if f.RealCopyFiles {
		if err := realCopyFile(src, dest); err != nil {
			return err
		}
	}
	f.Calls = append(f.Calls, CopyCall{Src: src, Dest: dest, Reflink: true})
	return nil
}

// realCopyFile is FakeCopier's RealCopyFiles backing: a plain read-then-write
// of a regular file's content, deliberately not supporting directories —
// FakeCopier's tests only ever need this for the single-file
// on_conflict=overwrite path.
func realCopyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}
