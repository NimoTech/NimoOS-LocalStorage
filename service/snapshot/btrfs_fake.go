package snapshot

import (
	"context"
	"strings"
	"sync"
)

// SnapshotCall records one CreateReadOnlySnapshot invocation for test
// assertions.
type SnapshotCall struct {
	SourcePath string
	DestPath   string
}

// FakeRunner is an in-memory Runner for tests. It is intentionally not a
// full btrfs filesystem emulator — it tracks just enough state (which
// top-level subvolumes "exist" per device, which (device, mountPoint) pairs
// are mounted, and canned/injectable results per operation) for the
// ensure-mount and reconciliation logic in this package — and for later
// snapshot-feature tasks (scheduler/API/restore) — to be driven and
// asserted against without root or a real btrfs filesystem.
type FakeRunner struct {
	mu sync.Mutex

	// ExistingTopLevelSubvolumes models which top-level subvolume names
	// (e.g. "@", "@snapshots") already exist on a device, as pre-existing
	// state before a test action runs. Seed via SeedTopLevelSubvolume.
	ExistingTopLevelSubvolumes map[string]map[string]struct{}

	// mountedPairs tracks currently-mounted (mountPoint -> device) pairs.
	mountedPairs map[string]string

	// Fstypes lets tests script Fstype(mountPoint) results explicitly. If a
	// mountPoint isn't present here but is mounted, Fstype returns "btrfs"
	// (a reasonable default since every fake mount in this package is
	// conceptually btrfs).
	Fstypes map[string]string

	// ListResult lets tests script exactly what ListSubvolumes(mountPoint)
	// returns. MountTopLevel seeds this lazily from
	// ExistingTopLevelSubvolumes the first time a given mountPoint is
	// mounted, so most tests don't need to set it directly.
	ListResult map[string][]SubvolumeEntry
	ListErr    map[string]error

	// Injected errors for the ensure-mount legacy-volume path, keyed by
	// device (not by the randomly-generated temp mount path, which tests
	// can't predict ahead of time).
	CreateSubvolumeErrByDevice map[string]error
	MountTopLevelErr           map[string]error
	MountSubvolumeErr          map[string]error
	UnmountErr                 map[string]error

	// Injected errors for direct snapshot lifecycle calls, keyed by the
	// exact path argument.
	SnapshotErr map[string]error
	DeleteErr   map[string]error

	// Recorded calls for assertions.
	CreatedSubvolumes []string
	CreatedSnapshots  []SnapshotCall
	DeletedPaths      []string
}

var _ Runner = (*FakeRunner)(nil)

// NewFakeRunner returns a FakeRunner with no pre-existing state.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{
		ExistingTopLevelSubvolumes: map[string]map[string]struct{}{},
		mountedPairs:               map[string]string{},
		Fstypes:                    map[string]string{},
		ListResult:                 map[string][]SubvolumeEntry{},
		ListErr:                    map[string]error{},
		CreateSubvolumeErrByDevice: map[string]error{},
		MountTopLevelErr:           map[string]error{},
		MountSubvolumeErr:          map[string]error{},
		UnmountErr:                 map[string]error{},
		SnapshotErr:                map[string]error{},
		DeleteErr:                  map[string]error{},
	}
}

// SeedTopLevelSubvolume marks name (e.g. "@", "@snapshots") as already
// existing on device, modelling a pre-existing/legacy filesystem layout.
func (f *FakeRunner) SeedTopLevelSubvolume(device, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ExistingTopLevelSubvolumes[device] == nil {
		f.ExistingTopLevelSubvolumes[device] = map[string]struct{}{}
	}
	f.ExistingTopLevelSubvolumes[device][name] = struct{}{}
}

// SeedMounted marks device as already mounted at mountPoint, as if a
// previous run (or boot) had already done the work.
func (f *FakeRunner) SeedMounted(device, mountPoint string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mountedPairs[mountPoint] = device
}

// deviceForPath returns the device backing the longest mounted prefix of
// path, so device-keyed error injection (CreateSubvolumeErrByDevice) works
// even though the caller mounts to a randomly-generated temp directory.
func (f *FakeRunner) deviceForPath(path string) (string, bool) {
	var bestMount, bestDevice string
	for mp, dev := range f.mountedPairs {
		if path == mp || strings.HasPrefix(path, mp+"/") {
			if len(mp) > len(bestMount) {
				bestMount, bestDevice = mp, dev
			}
		}
	}
	return bestDevice, bestMount != ""
}

func (f *FakeRunner) CreateReadOnlySnapshot(_ context.Context, sourcePath, destPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.SnapshotErr[destPath]; err != nil {
		return err
	}
	f.CreatedSnapshots = append(f.CreatedSnapshots, SnapshotCall{SourcePath: sourcePath, DestPath: destPath})
	return nil
}

func (f *FakeRunner) DeleteSubvolume(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.DeleteErr[path]; err != nil {
		return err
	}
	f.DeletedPaths = append(f.DeletedPaths, path)
	return nil
}

func (f *FakeRunner) CreateSubvolume(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if device, ok := f.deviceForPath(path); ok {
		if err := f.CreateSubvolumeErrByDevice[device]; err != nil {
			return err
		}
	}
	f.CreatedSubvolumes = append(f.CreatedSubvolumes, path)
	return nil
}

func (f *FakeRunner) ListSubvolumes(_ context.Context, mountPoint string) ([]SubvolumeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ListErr[mountPoint]; err != nil {
		return nil, err
	}
	return f.ListResult[mountPoint], nil
}

func (f *FakeRunner) MountTopLevel(_ context.Context, device, mountPoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.MountTopLevelErr[device]; err != nil {
		return err
	}
	f.mountedPairs[mountPoint] = device
	if _, seeded := f.ListResult[mountPoint]; !seeded {
		var entries []SubvolumeEntry
		for name := range f.ExistingTopLevelSubvolumes[device] {
			entries = append(entries, SubvolumeEntry{Path: name})
		}
		f.ListResult[mountPoint] = entries
	}
	return nil
}

func (f *FakeRunner) MountSubvolume(_ context.Context, device, mountPoint, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.MountSubvolumeErr[device]; err != nil {
		return err
	}
	f.mountedPairs[mountPoint] = device
	return nil
}

func (f *FakeRunner) Unmount(_ context.Context, mountPoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.UnmountErr[mountPoint]; err != nil {
		return err
	}
	delete(f.mountedPairs, mountPoint)
	return nil
}

func (f *FakeRunner) IsMounted(device, mountPoint string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mountedPairs[mountPoint] == device, nil
}

func (f *FakeRunner) Fstype(mountPoint string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.Fstypes[mountPoint]; ok {
		return v, nil
	}
	if _, ok := f.mountedPairs[mountPoint]; ok {
		return "btrfs", nil
	}
	return "", nil
}
