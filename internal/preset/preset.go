// Package preset holds the starting configs derbent init can write: commented TOML files that the user
// owns once they are written (ADR 0016).
package preset

import (
	"embed"
	"fmt"
	"slices"
	"strings"
)

//go:embed watch.toml balanced.toml strict.toml
var files embed.FS

// names lists the presets from the loosest to the strictest.
var names = []string{"watch", "balanced", "strict"}

// Names lists the presets from the loosest to the strictest.
func Names() []string { return slices.Clone(names) }

// Text returns the preset's TOML. An unknown name is an error that lists the presets.
func Text(name string) (string, error) {
	if !slices.Contains(names, name) {
		return "", fmt.Errorf("unknown preset %q: use one of %s", name, strings.Join(names, ", "))
	}
	b, err := files.ReadFile(name + ".toml")
	if err != nil {
		return "", fmt.Errorf("read preset %s: %w", name, err)
	}
	return string(b), nil
}
