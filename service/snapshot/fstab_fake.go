package snapshot

import "sync"

// fstabEntryArgs is what FakeFstabPersister recorded for one mount point.
type fstabEntryArgs struct {
	Source, FSType, Options string
}

// FakeFstabPersister is an in-memory FstabPersister for tests.
type FakeFstabPersister struct {
	mu sync.Mutex

	Persisted  map[string]fstabEntryArgs
	PersistErr map[string]error
	RemoveErr  map[string]error
}

var _ FstabPersister = (*FakeFstabPersister)(nil)

// NewFakeFstabPersister returns an empty FakeFstabPersister.
func NewFakeFstabPersister() *FakeFstabPersister {
	return &FakeFstabPersister{
		Persisted:  map[string]fstabEntryArgs{},
		PersistErr: map[string]error{},
		RemoveErr:  map[string]error{},
	}
}

func (f *FakeFstabPersister) Persist(mountPoint, source, fsType, options string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.PersistErr[mountPoint]; err != nil {
		return err
	}
	f.Persisted[mountPoint] = fstabEntryArgs{Source: source, FSType: fsType, Options: options}
	return nil
}

func (f *FakeFstabPersister) Remove(mountPoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.RemoveErr[mountPoint]; err != nil {
		return err
	}
	delete(f.Persisted, mountPoint)
	return nil
}
