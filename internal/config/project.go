package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectKey returns the key that notes and receipts for dir are filed under: the root of the git
// repository containing dir, or dir itself outside a repository. A linked worktree belongs to the
// repository it was added from, so agents working in separate worktrees of one repository share a key.
// The key is absolute, cleaned, with symbolic links and Windows short names resolved, written with
// forward slashes, and lower-cased on Windows, so that one project reached through different spellings
// has one key.
func ProjectKey(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve project %s: %w", dir, err)
	}
	root := resolve(abs)
	for d := root; ; d = filepath.Dir(d) {
		if fi, statErr := os.Stat(filepath.Join(d, ".git")); statErr == nil {
			root = d
			if !fi.IsDir() {
				root = mainCheckout(d)
			}
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	key := filepath.ToSlash(filepath.Clean(resolve(root)))
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, nil
}

// mainCheckout maps a directory whose .git is a file to the repository it belongs to. A linked
// worktree's .git file points at <repo>/.git/worktrees/<name>, which holds a commondir file leading
// back to <repo>/.git. A submodule's .git file points at a git directory without commondir, so the
// submodule stays a project of its own, as does anything whose .git file cannot be read.
func mainCheckout(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".git")) //nolint:gosec // reading the .git file of the user's own checkout
	if err != nil {
		return dir
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return dir
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir")) //nolint:gosec // a file inside the same checkout's git directory
	if err != nil {
		return dir
	}
	commonDir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}
	return commonDir // a worktree of a bare repository
}

// resolve expands symbolic links and Windows short names, and keeps the path as it is when that fails.
func resolve(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
