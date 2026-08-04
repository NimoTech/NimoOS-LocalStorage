package snapshot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Copier is the seam between restore and the real `cp` binary. Production
// code uses ExecCopier; tests use FakeCopier (copy_fake.go) so restore's
// orchestration (never-overwrite destination selection, reflink-failure
// fallback) can be fully unit tested without root, without a real
// reflink-capable filesystem, and without ever risking an actual copy
// landing somewhere a test didn't intend.
type Copier interface {
	// Copy copies src to dest, preferring a reflink (copy-on-write) copy
	// and falling back to a plain deep copy if that fails — handoff §3.4:
	// "cp -a --reflink=always <snap>/<path> <live>/<path>.restored-<ts>;
	// if reflink fails (shouldn't happen on the same filesystem in theory) fall back to a plain cp -a".
	//
	// Copy DOES check whether dest already exists, both itself (an
	// explicit stat immediately before shelling out) and via the
	// underlying `cp` invocation's own no-clobber flags, and returns
	// ErrRestoreDestinationExists instead of silently skipping or
	// overwriting if it does. This existence check is deliberately not
	// left to callers: computeRestoreDestination choosing a destination
	// that's free *at the moment it checked* is necessary but not
	// sufficient, because another process (most plausibly a concurrent
	// restore) can create that exact path in the window between that
	// check and this call actually running. Copy is the last line of
	// defense against that race, not a redundant check that could race
	// with itself the way a bare "check then `cp` without any no-clobber
	// flag" would.
	Copy(ctx context.Context, src, dest string) error
}

// ExecCopier is the production Copier: it shells out to `cp`, with a
// timeout and stderr-to-log on failure (matching ExecRunner's pattern in
// btrfs.go — handoff §3.1's "every external command goes through an exec
// wrapper, with a timeout guard and stderr logging" applies here too, even though this isn't a btrfs-specific command).
type ExecCopier struct {
	// Timeout bounds each `cp` invocation; DefaultCommandTimeout is used if
	// zero. A large copy (e.g. restoring tens of GB with a reflink, which
	// should be near-instant, or falling back to a real deep copy, which
	// isn't) may need a longer timeout in production than the default 30s;
	// NewExecCopier sets a longer default for exactly this reason.
	Timeout time.Duration
}

var _ Copier = (*ExecCopier)(nil)

// DefaultCopyTimeout is longer than DefaultCommandTimeout (btrfs.go): a
// reflink copy of a large directory tree should be fast, but the fallback
// plain `cp -a` (taken when reflink isn't available) is a real deep copy
// and can legitimately take a while for a large restore.
const DefaultCopyTimeout = 10 * time.Minute

// NewExecCopier returns an ExecCopier configured with DefaultCopyTimeout.
func NewExecCopier() *ExecCopier {
	return &ExecCopier{Timeout: DefaultCopyTimeout}
}

func (c *ExecCopier) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultCopyTimeout
	}
	return c.Timeout
}

func (c *ExecCopier) run(parent context.Context, name string, args ...string) ([]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout())
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		logger.Error("snapshot: restore copy command failed",
			zap.String("command", name),
			zap.Strings("args", args),
			zap.String("output", strings.TrimSpace(string(out))),
			zap.Error(err))
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// noClobber returns the flags passed to every `cp` invocation Copy makes, as
// defense-in-depth alongside Copy's own pre-flight os.Lstat check:
//
//   - "-T"/"--no-target-directory" makes cp always treat dest as the
//     literal target path, never as "an existing directory to copy the
//     source into" (GNU cp's default when dest already exists as a
//     directory). Without it, restoring a directory onto an
//     already-existing (e.g. concurrently created) destination directory
//     would silently nest the copy one level deeper (dest/<basename(src)>)
//     and exit 0 instead of failing. Ancient flag; always passed.
//   - "--update=none-fail" makes cp itself refuse to replace an existing
//     destination FILE and exit non-zero — unlike "-n"/"--no-clobber",
//     which silently skips and exits 0, indistinguishable from success
//     without a separate check. This is why "-n" alone was rejected: it
//     would have reproduced the exact "reports success on a skipped copy"
//     bug this code closes, just with cp doing the skipping. Passed only
//     where cp supports it; see below.
//
// "--update=none-fail" arrived in GNU coreutils 9.5. Debian 12 ships 9.1 and
// Ubuntu 24.04 ships 9.4, so on the platforms NimoOS actually installs onto,
// passing it makes cp exit 1 with "option '--update' doesn't allow an
// argument" — and because it was on both the reflink attempt and the
// plain-copy fallback, every restore copy failed outright instead of
// degrading. The comment this replaces asserted ">= 9.2, confirmed present on
// this system's coreutils 9.7"; the version floor was wrong and the machine it
// was confirmed on was not one we ship to, which is how it went unnoticed.
//
// Dropping the flag loses defense-in-depth, not the guarantee. Neither flag
// fully closes the race by itself anyway — for a directory dest that exists
// but contains no conflicting filenames, cp would merge into it and exit 0.
// Copy's own pre-flight and post-failure os.Lstat checks are the actual
// authority for "does dest exist at all", independent of cp's version.
//
// Probed lazily rather than in an initialiser: package-level init runs before
// logger.Init, so logging from there dereferences a nil *zap.Logger, and it
// would also shell out to cp on import in every binary linking this package.
var (
	noClobberOnce sync.Once
	noClobberArgs []string
)

func noClobber() []string {
	noClobberOnce.Do(func() {
		noClobberArgs = []string{"-T"}
		out, err := exec.Command("cp", "--help").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "none-fail") {
			logger.Info("snapshot: this cp does not support --update=none-fail; " +
				"relying on the pre-flight existence check alone")
			return
		}
		noClobberArgs = append(noClobberArgs, "--update=none-fail")
	})
	return noClobberArgs
}

func (c *ExecCopier) Copy(ctx context.Context, src, dest string) error {
	// Pre-flight: refuse outright if dest already exists in any form. This
	// is the primary defense — an explicit, cp-version-independent check —
	// closing the race between computeRestoreDestination's own Exists
	// check and this call actually running.
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%w: %s", ErrRestoreDestinationExists, dest)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check restore destination %s: %w", dest, err)
	}

	reflinkArgs := append([]string{"-a", "--reflink=always"}, noClobber()...)
	reflinkArgs = append(reflinkArgs, src, dest)
	if _, err := c.run(ctx, "cp", reflinkArgs...); err != nil {
		logger.Info("snapshot: reflink copy failed, falling back to a plain copy",
			zap.String("src", src), zap.String("dest", dest), zap.Error(err))
		plainArgs := append([]string{"-a"}, noClobber()...)
		plainArgs = append(plainArgs, src, dest)
		if _, fallbackErr := c.run(ctx, "cp", plainArgs...); fallbackErr != nil {
			// If dest exists now despite not existing at the pre-flight
			// check above, something else created it in the intervening
			// window (the residual race the pre-flight check can't fully
			// close by itself) — report the same retryable sentinel rather
			// than an opaque failure.
			if _, statErr := os.Lstat(dest); statErr == nil {
				return fmt.Errorf("%w: %s (reflink attempt: %v; plain-copy attempt: %v)", ErrRestoreDestinationExists, dest, err, fallbackErr)
			}
			return fmt.Errorf("reflink copy failed (%v) and plain copy fallback also failed: %w", err, fallbackErr)
		}
	}
	return nil
}
