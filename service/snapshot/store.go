package snapshot

import (
	"errors"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"gorm.io/gorm"
)

// FullStore is the persistence surface the API layer (route/snapshot.go,
// service/snapshot's Service) needs: snapshot records (Store) plus
// per-volume policy CRUD. GormStore is the production implementation;
// FakeStore (store_fake.go) is the in-memory test double.
type FullStore interface {
	Store
	GetOrCreatePolicy(volumeUUID string) (*model.SnapshotPolicy, error)
	SavePolicy(policy model.SnapshotPolicy) error
}

// GormStore is the production Store (see reconcile.go), plus the small
// amount of additional policy persistence a later task's API routes will
// need (get-or-create the per-volume policy, and upsert it).
type GormStore struct {
	db *gorm.DB
}

var _ Store = (*GormStore)(nil)
var _ FullStore = (*GormStore)(nil)

// NewGormStore wraps db (as returned by pkg/sqlite.GetGlobalDB/GetDBByFile,
// which already has o_snapshot/o_snapshot_policy AutoMigrated).
func NewGormStore(db *gorm.DB) *GormStore {
	return &GormStore{db: db}
}

func (g *GormStore) ListSnapshots(volumeUUID string) ([]model.Snapshot, error) {
	var recs []model.Snapshot
	err := g.db.Where("volume_uuid = ?", volumeUUID).Order("created_at").Find(&recs).Error
	return recs, err
}

func (g *GormStore) InsertSnapshot(rec model.Snapshot) error {
	return g.db.Create(&rec).Error
}

func (g *GormStore) DeleteSnapshot(id uint) error {
	return g.db.Delete(&model.Snapshot{}, id).Error
}

// GetOrCreatePolicy returns the stored policy for volumeUUID, creating one
// with DefaultPolicy values if none exists yet.
func (g *GormStore) GetOrCreatePolicy(volumeUUID string) (*model.SnapshotPolicy, error) {
	var policy model.SnapshotPolicy
	err := g.db.Where("volume_uuid = ?", volumeUUID).First(&policy).Error
	if err == nil {
		return &policy, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	def := DefaultPolicy(volumeUUID)
	if err := g.db.Create(&def).Error; err != nil {
		return nil, err
	}
	return &def, nil
}

// SavePolicy upserts policy by volume_uuid: updates the existing row for
// policy.VolumeUUID if one exists, otherwise creates it.
//
// This uses Save (full-record UPDATE by primary key), not Updates(struct):
// GORM's Updates skips zero-value fields on a struct argument, which would
// silently no-op a caller flipping Enabled back to false.
func (g *GormStore) SavePolicy(policy model.SnapshotPolicy) error {
	var existing model.SnapshotPolicy
	err := g.db.Where("volume_uuid = ?", policy.VolumeUUID).First(&existing).Error
	if err == nil {
		policy.ID = existing.ID
		return g.db.Save(&policy).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return g.db.Create(&policy).Error
}
