// Package config loads the user's configuration and works out where Derbent keeps its files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/redact"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// DefaultApprovalTimeout is how long a call waits for the user when the config does not say. It sits
// below the shortest default tool timeout of the supported CLIs (ADR 0005).
const DefaultApprovalTimeout = 50 * time.Second

// Config is a loaded and validated configuration.
type Config struct {
	Rules   rule.Set
	Servers []Server
	// Redact masks secrets in arguments before they are stored in a receipt. It is never nil.
	Redact *redact.Redactor
	// ApprovalTimeout is how long a call waits for the user's decision before it is denied.
	ApprovalTimeout time.Duration
}

type file struct {
	Rules     []rule.Spec           `toml:"rule"`
	Servers   map[string]serverFile `toml:"servers"`
	Approvals struct {
		Timeout string `toml:"timeout"`
	} `toml:"approvals"`
	Receipts struct {
		Redact []string `toml:"redact"`
	} `toml:"receipts"`
}

// Default is the configuration used when there is no config file. It allows every call.
func Default() Config {
	set, err := rule.Compile([]rule.Spec{{Action: rule.Allow}})
	if err != nil {
		panic(err) // a constant rule list that always compiles
	}
	return Config{Rules: set, Redact: mustRedactor(), ApprovalTimeout: DefaultApprovalTimeout}
}

func mustRedactor() *redact.Redactor {
	r, err := redact.New(nil, nil)
	if err != nil {
		panic(err) // no patterns cannot fail
	}
	return r
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
	srvs, secrets, err := servers(f.Servers)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", name, err)
	}
	red, err := redact.New(f.Receipts.Redact, secrets)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", name, err)
	}
	timeout, err := approvalTimeout(f.Approvals.Timeout)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", name, err)
	}
	return Config{Rules: rules, Servers: srvs, Redact: red, ApprovalTimeout: timeout}, nil
}

// approvalTimeout parses approvals.timeout, a Go duration such as "50s" or "2m" of at least a second.
func approvalTimeout(s string) (time.Duration, error) {
	if s == "" {
		return DefaultApprovalTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("approvals.timeout %q is not a duration such as \"50s\": %w", s, err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("approvals.timeout %q is under one second", s)
	}
	return d, nil
}
