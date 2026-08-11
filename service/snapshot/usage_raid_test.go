package snapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/NimoTech/NimoOS-LocalStorage/service/model"
	v2 "github.com/NimoTech/NimoOS-LocalStorage/service/v2"
)

// stubRAIDService implements v2.RAIDService, exercising only the two
// methods RAIDUsageProvider actually calls (ListRAIDArrays, GetRAIDUsage);
// the rest panic if ever invoked, since nothing under test should reach
// them.
type stubRAIDService struct {
	arrays   []*model.RAIDArray
	usage    map[uint]*v2.RAIDUsage
	usageErr map[uint]error
}

func (s *stubRAIDService) ListRAIDArrays() ([]*model.RAIDArray, error) { return s.arrays, nil }

func (s *stubRAIDService) GetRAIDUsage(id uint) (*v2.RAIDUsage, error) {
	if err := s.usageErr[id]; err != nil {
		return nil, err
	}
	return s.usage[id], nil
}

func (s *stubRAIDService) CreateRAIDArray(int, []string, string, int, string, func(int)) (*model.RAIDArray, error) {
	panic("not used by RAIDUsageProvider")
}
func (s *stubRAIDService) DeleteRAIDArray(uint) error { panic("not used by RAIDUsageProvider") }
func (s *stubRAIDService) GetRAIDStatus(uint) (*v2.RAIDStatus, error) {
	panic("not used by RAIDUsageProvider")
}
func (s *stubRAIDService) EnsureFilesystemResized(uint) error {
	panic("not used by RAIDUsageProvider")
}
func (s *stubRAIDService) ReplaceDisk(uint, string, string, string) error {
	panic("not used by RAIDUsageProvider")
}
func (s *stubRAIDService) RecoverOnBoot() error         { panic("not used by RAIDUsageProvider") }
func (s *stubRAIDService) Recover(uint) (string, error) { panic("not used by RAIDUsageProvider") }

var _ v2.RAIDService = (*stubRAIDService)(nil)

func TestRAIDUsageProviderMatchesVolumeByUUID(t *testing.T) {
	raid := &stubRAIDService{
		arrays: []*model.RAIDArray{{ID: 7, UUID: "vol-1"}},
		usage: map[uint]*v2.RAIDUsage{
			7: {Filesystem: "btrfs", BtrfsUsage: &v2.BtrfsUsage{DeviceSizeBytes: 1000, FreeEstimatedBytes: 100}},
		},
	}
	p := NewRAIDUsageProvider(raid)
	pct, err := p.UsagePercent(context.Background(), VolumeInfo{UUID: "vol-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pct != 90 {
		t.Fatalf("expected 90%%, got %v", pct)
	}
}

func TestRAIDUsageProviderVolumeNotFound(t *testing.T) {
	raid := &stubRAIDService{arrays: []*model.RAIDArray{{ID: 7, UUID: "other"}}}
	p := NewRAIDUsageProvider(raid)
	if _, err := p.UsagePercent(context.Background(), VolumeInfo{UUID: "vol-1"}); err == nil {
		t.Fatalf("expected error for unmatched volume")
	}
}

func TestRAIDUsageProviderPropagatesUsageError(t *testing.T) {
	raid := &stubRAIDService{
		arrays:   []*model.RAIDArray{{ID: 7, UUID: "vol-1"}},
		usageErr: map[uint]error{7: errors.New("btrfs query failed")},
	}
	p := NewRAIDUsageProvider(raid)
	if _, err := p.UsagePercent(context.Background(), VolumeInfo{UUID: "vol-1"}); err == nil {
		t.Fatalf("expected propagated usage error")
	}
}
