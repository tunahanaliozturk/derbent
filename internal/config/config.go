// Package config loads the user's configuration and works out where Portcullis keeps its files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/portcullis/internal/rule"
)

// Config is a loaded and validated configuration.
type Config struct {
	Rules rule.Set
}

type file struct {
	Rules []rule.Spec `toml:"rule"`
}

// Default is the configuration used when there is no config file. Milestone 1 serves only the memory
// tools, so it allows every call.
func Default() Config {
	set, err := rule.Compile([]rule.Spec{{Action: rule.Allow}})
	if err != nil {
		panic(err) // a constant rule list that always compiles
	}
	return Config{Rules: set}
}

// Load reads the config file at path. A file that does not exist gives Default.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the user's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(path, string(data))
}

// Parse decodes and validates config text. name only appears in error messages.
func Parse(name, text string) (Config, error) {
	var f file
	md, err := toml.Decode(text, &f)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", name, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("config %s: unknown keys: %s", name, strings.Join(keys, ", "))
	}
	rules, err := rule.Compile(f.Rules)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", name, err)
	}
	return Config{Rules: rules}, nil
}
