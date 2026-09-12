package service

import (
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/config"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gotest.tools/v3/assert"
)

// nimoos-local-storage-first.service runs `-init` Before=docker.service, when
// the Gateway has not written management.url yet. NewService panics on that
// missing file; the init-time constructor must come up without it.
func TestNewInitService_WorksWithoutGatewayAddressFile(t *testing.T) {
	logger.LogInitConsoleOnly()

	prevRuntimePath := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = t.TempDir() // empty: no management.url
	t.Cleanup(func() { config.CommonInfo.RuntimePath = prevRuntimePath })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	assert.NilError(t, err)

	s := NewInitService(db)
	assert.Assert(t, s.Disk() != nil, "init service must carry the disk service CheckSerialDiskMount runs on")
	assert.Assert(t, s.Gateway() == nil, "init service must not construct a Gateway client")
}
