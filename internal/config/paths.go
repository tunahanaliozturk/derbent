package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultConfigPath is config.toml in the user config directory: %AppData% on Windows,
// $XDG_CONFIG_HOME on Linux, ~/Library/Application Support on macOS.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config directory: %w", err)
	}
	return filepath.Join(dir, "derbent", "config.toml"), nil
}

// DefaultDBPath is derbent.db in the user state directory: %LocalAppData% on Windows,
// $XDG_STATE_HOME or ~/.local/state on Linux, ~/Library/Application Support on macOS. It stays out of
// roaming and synced folders, where SQLite's file locking cannot be relied on.
func DefaultDBPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "derbent", "derbent.db"), nil
}

func stateDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return "", errors.New("find state directory: LOCALAPPDATA is not set")
	case "darwin":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find state directory: %w", err)
		}
		return dir, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find state directory: %w", err)
	}
	return filepath.Join(home, ".local", "state"), nil
}
