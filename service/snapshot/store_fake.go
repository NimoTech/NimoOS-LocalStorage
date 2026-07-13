package snapshot

import (
	"errors"
	"sync"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
)

// FakeStore is an in-memory FullStore for tests (route handler tests and
// service.Service tests), mirroring FakeRunner (btrfs_fake.go): no real DB,
// but the same upsert-by-volume_uuid semantics as GormStore (store.go),
// including the "SavePolicy persists zero values" behavior that store.go's
// TestGormStoreSavePolicyPersistsZeroValues guards against in the real
// implementation.
type FakeStore struct {
	mu sync.Mutex

	nextID    uint
	snapshots map[uint]model.Snapshot
	policies  map[string]model.SnapshotPolicy

	// SavePolicyCalls counts invocations of SavePolicy, for tests asserting
	// it was (or wasn't) called at all — e.g. task B5's non-btrfs skip,
	// where GetOrCreatePolicy's own on-demand default-row creation would
	// otherwise make the policies map non-empty even without a SavePolicy
	// call.
	SavePolicyCalls int
}

var _ FullStore = (*FakeStore)(nil)

// NewFakeStore returns an empty FakeStore.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		nextID:    1,
		snapshots: map[uint]model.Snapshot{},
		policies:  map[string]model.SnapshotPolicy{},
	}
}

func (s *FakeStore) ListSnapshots(volumeUUID string) ([]model.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Snapshot
	for _, rec := range s.snapshots {
		if rec.VolumeUUID == volumeUUID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *FakeStore) InsertSnapshot(rec model.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.snapshots {
		if existing.VolumeUUID == rec.VolumeUUID && existing.Name == rec.Name {
			return errors.New("snapshot already exists for this volume")
		}
	}
	rec.ID = s.nextID
	s.nextID++
	s.snapshots[rec.ID] = rec
	return nil
}

func (s *FakeStore) DeleteSnapshot(id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snapshots[id]; !ok {
		return errors.New("snapshot record not found")
	}
	delete(s.snapshots, id)
	return nil
}

func (s *FakeStore) GetOrCreatePolicy(volumeUUID string) (*model.SnapshotPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.policies[volumeUUID]; ok {
		cp := p
		return &cp, nil
	}
	def := DefaultPolicy(volumeUUID)
	s.policies[volumeUUID] = def
	cp := def
	return &cp, nil
}

// SavePolicy upserts by VolumeUUID, always writing every field (mirroring
// GormStore.SavePolicy's use of Save over Updates) so a caller flipping
// Enabled back to false is never silently dropped.
func (s *FakeStore) SavePolicy(policy model.SnapshotPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SavePolicyCalls++
	s.policies[policy.VolumeUUID] = policy
	return nil
}
