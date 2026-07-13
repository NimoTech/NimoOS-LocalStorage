package snapshot

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
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
	// reflink 失败(理论同盘不该发生)回退普通 cp -a". Copy does not check
	// whether dest already exists; callers (computeRestoreDestination) are
	// responsible for choosing a destination that doesn't collide, so this
	// never overwrites anything by construction rather than by a check
	// here that could race.
	Copy(ctx context.Context, src, dest string) error
}

// ExecCopier is the production Copier: it shells out to `cp`, with a
// timeout and stderr-to-log on failure (matching ExecRunner's pattern in
// btrfs.go — handoff §3.1's "所有外部命令经 exec 封装、超时保护、stderr 入
// 日志" applies here too, even though this isn't a btrfs-specific command).
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

func (c *ExecCopier) Copy(ctx context.Context, src, dest string) error {
	if _, err := c.run(ctx, "cp", "-a", "--reflink=always", src, dest); err != nil {
		logger.Info("snapshot: reflink copy failed, falling back to a plain copy",
			zap.String("src", src), zap.String("dest", dest), zap.Error(err))
		if _, fallbackErr := c.run(ctx, "cp", "-a", src, dest); fallbackErr != nil {
			return fmt.Errorf("reflink copy failed (%v) and plain copy fallback also failed: %w", err, fallbackErr)
		}
	}
	return nil
}
