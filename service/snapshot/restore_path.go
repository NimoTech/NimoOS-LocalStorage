package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveWithinDir validates a caller-supplied relative path rel, once
// joined onto base, still resolves — after following any symlinks along
// the way — to somewhere inside base. This is restore's and file-versions'
// path-safety boundary (handoff §3.4: "path 拼接后 filepath.Clean 必须仍在
// 快照目录/卷挂载点内"), and defends independently against three escape
// shapes:
//
//  1. an absolute path (rejected outright — rel must be relative to base);
//  2. lexical "../" traversal (caught by filepath.Clean before any disk
//     access, so it's rejected even if base doesn't exist);
//  3. a symlink — at any level, including one whose target doesn't exist
//     yet only in the path's final component — redirecting outside base.
//     filepath.Clean alone cannot catch this (it's purely lexical), so this
//     also resolves real symlinks via resolveExistingPrefix.
//
// This deliberately talks to the real filesystem (os.Lstat /
// filepath.EvalSymlinks) rather than going through the injectable
// PathChecker seam (fs.go): it IS the security boundary, so it must observe
// genuine filesystem semantics rather than a fake's model of them. It's
// still fully unit-testable without root or btrfs — see
// restore_path_test.go, which uses only t.TempDir() and os.Symlink.
func resolveWithinDir(base, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative, got absolute path %q", rel)
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes its base directory", rel)
	}

	realBase, err := resolveExistingPrefix(base)
	if err != nil {
		return "", fmt.Errorf("resolve base directory %s: %w", base, err)
	}

	joined := filepath.Join(realBase, cleanRel)
	resolved, err := resolveExistingPrefix(joined)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if resolved != realBase && !strings.HasPrefix(resolved, realBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes its base directory", rel)
	}
	return resolved, nil
}

// resolveExistingPrefix resolves symlinks along the longest existing prefix
// of path (mirroring filepath.EvalSymlinks, which requires the full path to
// exist), then rejoins any not-yet-existing trailing components literally.
// This lets it be used both for a source path (which must exist) and a
// not-yet-existing restore destination (whose parent directories may or may
// not exist) — either way, any *existing* component that happens to be a
// symlink is still resolved, so it can't redirect the result outside of
// wherever the caller expects.
func resolveExistingPrefix(path string) (string, error) {
	cur := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding anything that
			// exists; nothing left to resolve.
			break
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}

	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %s: %w", cur, err)
	}

	full := real
	for _, s := range suffix {
		full = filepath.Join(full, s)
	}
	return filepath.Clean(full), nil
}
