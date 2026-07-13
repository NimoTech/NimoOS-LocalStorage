package snapshot

import "github.com/NimoTech/NimoOS-LocalStorage/pkg/fstab"

// FstabPersister persists (or removes) a mount entry so the @snapshots
// mount survives a reboot without needing the ensure-mount logic to rerun
// (handoff §3.1). It mirrors the intent of service/v2/fstab.go's
// SaveToFStab/RemoveFromFStab — both of which are thin wrappers over
// pkg/fstab — but is expressed over plain strings so this package doesn't
// need to depend on service/v2's codegen mount types just to persist a
// mount entry.
type FstabPersister interface {
	Persist(mountPoint, source, fsType, options string) error
	Remove(mountPoint string) error
}

// RealFstabPersister is the production FstabPersister, backed directly by
// pkg/fstab — the same persistence layer service/v2/fstab.go wraps.
type RealFstabPersister struct{}

var _ FstabPersister = RealFstabPersister{}

func (RealFstabPersister) Persist(mountPoint, source, fsType, options string) error {
	return fstab.Get().Add(fstab.Entry{
		MountPoint: mountPoint,
		Source:     source,
		FSType:     fsType,
		Options:    options,
		Dump:       0,
		Pass:       fstab.PassDoNotCheck,
	}, true)
}

func (RealFstabPersister) Remove(mountPoint string) error {
	return fstab.Get().RemoveByMountPoint(mountPoint, false)
}
