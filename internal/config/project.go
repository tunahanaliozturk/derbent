package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectKey returns the key that notes and receipts for dir are filed under: the root of the git
// repository containing dir, or dir itself outside a repository. The key is absolute, cleaned, with
// symbolic links and Windows short names resolved, written with forward slashes, and lower-cased on
// Windows, so that one project reached through different spellings has one key.
func ProjectKey(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve project %s: %w", dir, err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	}
	root := abs
	for d := abs; ; d = filepath.Dir(d) {
		if _, statErr := os.Stat(filepath.Join(d, ".git")); statErr == nil {
			root = d
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	key := filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, nil
}
