//go:build !windows

package config

import "path/filepath"

// finalPath is path with its symbolic links resolved.
func finalPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
