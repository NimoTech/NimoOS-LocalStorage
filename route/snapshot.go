package route

import (
	"crypto/ecdsa"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/utils/common_err"
	"github.com/NimoTech/NimoOS-Common/utils/jwt"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-LocalStorage/pkg/config"
	"github.com/NimoTech/NimoOS-LocalStorage/service"
	svcmodel "github.com/NimoTech/NimoOS-LocalStorage/service/model"
	"github.com/NimoTech/NimoOS-LocalStorage/service/snapshot"
	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"
)

// V2SnapshotPath is the Gateway-registered route prefix for the btrfs
// snapshot API (handoff §3.4 / task-B2 brief).
const V2SnapshotPath = "/v2/snapshot"

// InitSnapshotRouter builds the standalone echo router for the snapshot API,
// following the same hand-written-route + admin-JWT pattern as
// InitRAIDRouter (route/raid.go): CORS, JWT (skipped for localhost/Gateway
// callers, required otherwise), and one route group under V2SnapshotPath.
func InitSnapshotRouter() http.Handler {
	e := echo.New()
	e.Use(echo_middleware.CORSWithConfig(echo_middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{echo.POST, echo.GET, echo.OPTIONS, echo.PUT, echo.DELETE},
		AllowHeaders:     []string{echo.HeaderAuthorization, echo.HeaderContentLength, echo.HeaderXCSRFToken, echo.HeaderContentType, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders, echo.HeaderAccessControlAllowMethods, echo.HeaderConnection, echo.HeaderOrigin, echo.HeaderXRequestedWith},
		ExposeHeaders:    []string{echo.HeaderContentLength, echo.HeaderAccessControlAllowOrigin, echo.HeaderAccessControlAllowHeaders},
		MaxAge:           172800,
		AllowCredentials: true,
	}))

	e.Use(echo_middleware.Gzip())
	e.Use(echo_middleware.Recover())
	e.Use(echo_middleware.Logger())

	snapshotGroup := e.Group(V2SnapshotPath)

	snapshotGroup.Use(echo_middleware.JWTWithConfig(echo_middleware.JWTConfig{
		Skipper: func(c echo.Context) bool {
			return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
		},
		ParseTokenFunc: func(token string, c echo.Context) (interface{}, error) {
			valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
			if err != nil || !valid {
				return nil, echo.ErrUnauthorized
			}
			c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))
			return claims, nil
		},
		TokenLookupFuncs: []echo_middleware.ValuesExtractor{
			func(c echo.Context) ([]string, error) {
				if len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
					return []string{strings.TrimPrefix(c.Request().Header.Get(echo.HeaderAuthorization), "Bearer ")}, nil
				}
				return []string{c.QueryParam("token")}, nil
			},
		},
	}))

	snapshotGroup.GET("/volumes", listSnapshotVolumes)
	snapshotGroup.GET("", listSnapshots)
	snapshotGroup.POST("", createSnapshot)
	snapshotGroup.DELETE("/:name", deleteSnapshot)
	snapshotGroup.GET("/policy", getSnapshotPolicy)
	snapshotGroup.PUT("/policy", putSnapshotPolicy)
	snapshotGroup.POST("/restore", restoreSnapshotFile)
	snapshotGroup.GET("/file-versions", listFileVersions)

	return e
}

type createSnapshotRequest struct {
	VolumeUUID string `json:"volume_uuid"`
	Label      string `json:"label"`
}

// restoreRequest is the body for POST /v2/snapshot/restore (handoff §3.4:
// "{volume_uuid, snapshot, path}; path=path relative to the volume"). Two optional fields
// extend it while staying backward compatible (both default to the
// original behavior when omitted):
//
//   - DestDir: an absolute path to an existing directory to restore into,
//     overriding the default of restoring back to path's original location
//     under the source volume's mount point. Validated by
//     snapshot.resolveRestoreDestDir: must already exist, and be located
//     under some currently mounted, snapshot-supported (btrfs) volume's
//     mount point (not necessarily the source volume — cross-volume
//     restore is allowed).
//   - WithMarker: nil or true (the default) keeps inserting the
//     ".restored-<ts>" marker into the restored name; false restores under
//     the original name instead (collisions still never overwrite — see
//     snapshot.RestoreOptions).
//   - OnConflict: "" or "keep_both" (the default) reproduces the original,
//     never-overwrite (numbered-suffix) behavior unchanged. "overwrite" is
//     for a caller (the file browser) that already ran its own pre-restore
//     conflict check and is telling Restore the user explicitly chose to
//     replace the existing file — see snapshot.RestoreOptions.OnConflict
//     for the full contract, including why it only supports files, never
//     directories.
type restoreRequest struct {
	VolumeUUID string `json:"volume_uuid"`
	Snapshot   string `json:"snapshot"`
	Path       string `json:"path"`
	DestDir    string `json:"dest_dir"`
	WithMarker *bool  `json:"with_marker"`
	OnConflict string `json:"on_conflict"`
}

type snapshotPolicyRequest struct {
	VolumeUUID        string `json:"volume_uuid"`
	Enabled           bool   `json:"enabled"`
	HourlyKeep        int    `json:"hourly_keep"`
	DailyKeep         int    `json:"daily_keep"`
	WeeklyKeep        int    `json:"weekly_keep"`
	PauseThresholdPct int    `json:"pause_threshold_pct"`
}

// currentVolumes enumerates every volume this service currently knows
// about. Today that's the RAID array inventory — see
// snapshot.VolumesFromRAIDArrays's doc comment on why "volume" and "RAID
// array" are the same thing in this codebase for now.
func currentVolumes() ([]snapshot.VolumeInfo, error) {
	raids, err := service.MyService.RAID().ListRAIDArrays()
	if err != nil {
		return nil, err
	}
	return snapshot.VolumesFromRAIDArrays(raids), nil
}

// resolveVolumeParam resolves volumeUUID (from a query param or request
// body) into a usable snapshot.VolumeInfo. On failure it writes the
// appropriate error response itself and returns ok=false — callers should
// just `return nil` in that case (the response was already written).
func resolveVolumeParam(ctx echo.Context, volumeUUID string) (snapshot.VolumeInfo, bool) {
	if volumeUUID == "" {
		writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "volume_uuid is required", nil)
		return snapshot.VolumeInfo{}, false
	}
	volumes, err := currentVolumes()
	if err != nil {
		logger.Error("snapshot: failed to list volumes", zap.Error(err))
		writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
		return snapshot.VolumeInfo{}, false
	}
	vol, err := service.MyService.Snapshot().ResolveVolume(volumes, volumeUUID)
	if err != nil {
		writeSnapshotError(ctx, err)
		return snapshot.VolumeInfo{}, false
	}
	return vol, true
}

// resolveVolumeIdentityParam is resolveVolumeParam's mount-agnostic sibling:
// it only checks that volumeUUID is a known btrfs volume, not that it's
// currently mounted. Use it for operations that don't touch the
// filesystem — a pure DB read (GET policy), or disabling an already-saved
// policy (PUT policy, enabled:false) — so a volume that is temporarily
// offline (RAID member unplugged, cold-boot enumeration race) can still be
// read/disabled (Fix Round 1; see Service.ResolveVolumeIdentity's doc
// comment).
func resolveVolumeIdentityParam(ctx echo.Context, volumeUUID string) (snapshot.VolumeInfo, bool) {
	if volumeUUID == "" {
		writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "volume_uuid is required", nil)
		return snapshot.VolumeInfo{}, false
	}
	volumes, err := currentVolumes()
	if err != nil {
		logger.Error("snapshot: failed to list volumes", zap.Error(err))
		writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
		return snapshot.VolumeInfo{}, false
	}
	vol, err := service.MyService.Snapshot().ResolveVolumeIdentity(volumes, volumeUUID)
	if err != nil {
		writeSnapshotError(ctx, err)
		return snapshot.VolumeInfo{}, false
	}
	return vol, true
}

func writeSnapshotResult(ctx echo.Context, status, code int, message string, data interface{}) error {
	return ctx.JSON(status, model.Result{Success: code, Message: message, Data: data})
}

// writeSnapshotError maps a service/snapshot sentinel error (see
// service/snapshot/errors.go) to an HTTP response; anything unrecognized is
// treated as an opaque backend failure.
func writeSnapshotError(ctx echo.Context, err error) error {
	switch {
	case errors.Is(err, snapshot.ErrVolumeNotFound), errors.Is(err, snapshot.ErrSnapshotNotFound), errors.Is(err, snapshot.ErrRestoreSourceNotFound):
		return writeSnapshotResult(ctx, http.StatusNotFound, common_err.INVALID_PARAMS, err.Error(), nil)
	case errors.Is(err, snapshot.ErrVolumeNotBtrfs), errors.Is(err, snapshot.ErrVolumeNotMounted), errors.Is(err, snapshot.ErrInvalidSnapshotName), errors.Is(err, snapshot.ErrInvalidRestorePath), errors.Is(err, snapshot.ErrInvalidRestoreDestDir), errors.Is(err, snapshot.ErrInvalidRestoreOnConflict), errors.Is(err, snapshot.ErrRestoreOverwriteUnsupported):
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, err.Error(), nil)
	case errors.Is(err, snapshot.ErrVolumeNotSupported), errors.Is(err, snapshot.ErrRestoreDestinationExists):
		return writeSnapshotResult(ctx, http.StatusConflict, common_err.SERVICE_ERROR, err.Error(), nil)
	default:
		logger.Error("snapshot: request failed", zap.Error(err))
		return writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
	}
}

// listSnapshotVolumes handles GET /v2/snapshot/volumes.
func listSnapshotVolumes(ctx echo.Context) error {
	volumes, err := currentVolumes()
	if err != nil {
		logger.Error("snapshot: failed to list volumes", zap.Error(err))
		return writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
	}
	statuses, err := service.MyService.Snapshot().ListVolumeStatuses(ctx.Request().Context(), volumes)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), statuses)
}

// listSnapshots handles GET /v2/snapshot?volume_uuid=.
func listSnapshots(ctx echo.Context) error {
	vol, ok := resolveVolumeParam(ctx, ctx.QueryParam("volume_uuid"))
	if !ok {
		return nil
	}
	recs, err := service.MyService.Snapshot().ListSnapshots(ctx.Request().Context(), vol)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), recs)
}

// createSnapshot handles POST /v2/snapshot ({volume_uuid, label?}, type is
// always "manual" — the only type a caller may request through this API).
func createSnapshot(ctx echo.Context) error {
	var req createSnapshotRequest
	if err := ctx.Bind(&req); err != nil {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, err.Error(), nil)
	}
	vol, ok := resolveVolumeParam(ctx, req.VolumeUUID)
	if !ok {
		return nil
	}

	createdBy := ctx.Request().Header.Get("user_id")
	rec, err := service.MyService.Snapshot().CreateManual(ctx.Request().Context(), vol, req.Label, createdBy)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusCreated, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), rec)
}

// deleteSnapshot handles DELETE /v2/snapshot/:name?volume_uuid=.
func deleteSnapshot(ctx echo.Context) error {
	vol, ok := resolveVolumeParam(ctx, ctx.QueryParam("volume_uuid"))
	if !ok {
		return nil
	}
	name := ctx.Param("name")
	if err := service.MyService.Snapshot().DeleteByName(ctx.Request().Context(), vol, name); err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), nil)
}

// getSnapshotPolicy handles GET /v2/snapshot/policy?volume_uuid=. This is a
// pure DB read, so it deliberately does not require the volume to be
// currently mounted — only that volume_uuid identifies a known btrfs
// volume (Fix Round 1: an admin must be able to see a saved policy for a
// volume that's temporarily offline).
func getSnapshotPolicy(ctx echo.Context) error {
	vol, ok := resolveVolumeIdentityParam(ctx, ctx.QueryParam("volume_uuid"))
	if !ok {
		return nil
	}
	policy, err := service.MyService.Snapshot().GetPolicy(vol.UUID)
	if err != nil {
		logger.Error("snapshot: failed to load policy", zap.String("volume_uuid", vol.UUID), zap.Error(err))
		return writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), policy)
}

// putSnapshotPolicy handles PUT /v2/snapshot/policy. This is a full-replace
// PUT (matching GormStore.SavePolicy's upsert-by-Save semantics — see
// service/snapshot/store.go): callers must send every field, not just the
// one(s) they're changing, or unspecified numeric fields will be persisted
// as zero.
//
// Volume resolution mirrors Service.SavePolicy's own branching (Fix Round
// 1): enabling automatic snapshots needs the volume mounted (it calls
// EnsureSnapshotsMount), but disabling an already-saved policy is a plain
// DB write and must work even when the volume is temporarily offline.
func putSnapshotPolicy(ctx echo.Context) error {
	var req snapshotPolicyRequest
	if err := ctx.Bind(&req); err != nil {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, err.Error(), nil)
	}

	var vol snapshot.VolumeInfo
	var ok bool
	if req.Enabled {
		vol, ok = resolveVolumeParam(ctx, req.VolumeUUID)
	} else {
		vol, ok = resolveVolumeIdentityParam(ctx, req.VolumeUUID)
	}
	if !ok {
		return nil
	}

	if req.HourlyKeep < 0 || req.DailyKeep < 0 || req.WeeklyKeep < 0 {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "keep counts must not be negative", nil)
	}
	if req.Enabled && (req.PauseThresholdPct < 1 || req.PauseThresholdPct > 100) {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "pause_threshold_pct must be between 1 and 100", nil)
	}

	policy := svcmodel.SnapshotPolicy{
		Enabled:           req.Enabled,
		HourlyKeep:        req.HourlyKeep,
		DailyKeep:         req.DailyKeep,
		WeeklyKeep:        req.WeeklyKeep,
		PauseThresholdPct: req.PauseThresholdPct,
	}
	if err := service.MyService.Snapshot().SavePolicy(ctx.Request().Context(), vol, policy); err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), nil)
}

// restoreSnapshotFile handles POST /v2/snapshot/restore
// ({volume_uuid, snapshot, path, dest_dir?, with_marker?, on_conflict?},
// path relative to the volume's root). This is the payoff of the whole
// snapshot feature — see snapshot.Service.Restore for the full safety
// contract (never overwrites unless on_conflict=overwrite was explicitly
// requested — and even then, only ever via an atomic rename, never an
// in-place write; path escape defended on both the snapshot and live-volume
// (or caller-chosen dest_dir) sides; snapshot must genuinely exist on disk).
func restoreSnapshotFile(ctx echo.Context) error {
	var req restoreRequest
	if err := ctx.Bind(&req); err != nil {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, err.Error(), nil)
	}
	vol, ok := resolveVolumeParam(ctx, req.VolumeUUID)
	if !ok {
		return nil
	}
	if req.Snapshot == "" || req.Path == "" {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "snapshot and path are required", nil)
	}

	// dest_dir is validated against every currently known volume, not just
	// the restore's source volume — a dest_dir override may legitimately
	// point at a different (also snapshot-supported, currently mounted)
	// volume than the one being restored from.
	volumes, err := currentVolumes()
	if err != nil {
		logger.Error("snapshot: failed to list volumes", zap.Error(err))
		return writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
	}

	opts := snapshot.RestoreOptions{DestDir: req.DestDir, WithMarker: req.WithMarker, OnConflict: req.OnConflict}
	result, err := service.MyService.Snapshot().Restore(ctx.Request().Context(), volumes, vol, req.Snapshot, req.Path, opts)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), result)
}

// listFileVersions handles GET /v2/snapshot/file-versions?path= (handoff
// §3.4): path is an absolute path (as the file browser would show it),
// which is first mapped to whichever known volume's mount point is its
// longest matching prefix (snapshot.FindVolumeForPath), then to that
// volume's relative path, before walking up to snapshot.MaxFileVersionsSnapshots
// of that volume's most recent snapshots.
func listFileVersions(ctx echo.Context) error {
	path := ctx.QueryParam("path")
	if path == "" {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "path is required", nil)
	}
	if !filepath.IsAbs(path) {
		return writeSnapshotResult(ctx, http.StatusBadRequest, common_err.INVALID_PARAMS, "path must be an absolute path", nil)
	}

	volumes, err := currentVolumes()
	if err != nil {
		logger.Error("snapshot: failed to list volumes", zap.Error(err))
		return writeSnapshotResult(ctx, http.StatusInternalServerError, common_err.SERVICE_ERROR, err.Error(), nil)
	}

	found, relPath, err := snapshot.FindVolumeForPath(volumes, path)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}
	vol, err := service.MyService.Snapshot().ResolveVolume(volumes, found.UUID)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}

	versions, err := service.MyService.Snapshot().FileVersions(ctx.Request().Context(), vol, relPath)
	if err != nil {
		return writeSnapshotError(ctx, err)
	}
	return writeSnapshotResult(ctx, http.StatusOK, common_err.SUCCESS, common_err.GetMsg(common_err.SUCCESS), versions)
}
