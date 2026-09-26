package config_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

func TestParseRules(t *testing.T) {
	cfg, err := config.Parse("test.toml", `
[[rule]]
agent  = "copilot"
tool   = "memory_write"
action = "deny"

[[rule]]
tool   = "memory_search"
args   = { query = "secret*" }
action = "deny"

[[rule]]
action = "allow"
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Rules.Decide("copilot", "memory_write", nil); got.Action != rule.Deny || got.Rule != 1 {
		t.Fatalf("copilot write = %+v", got)
	}
	if got := cfg.Rules.Decide("claude", "memory_search", map[string]any{"query": "secret plan"}); got.Rule != 2 {
		t.Fatalf("secret search = %+v", got)
	}
	if got := cfg.Rules.Decide("claude", "memory_write", nil); got.Action != rule.Allow {
		t.Fatalf("claude write = %+v", got)
	}
}

func TestParseRejectsUnknownKey(t *testing.T) {
	_, err := config.Parse("test.toml", "[[rule]]\nacton = \"deny\"\n\n[[rule]]\naction = \"allow\"\n")
	if err == nil || !strings.Contains(err.Error(), "acton") {
		t.Fatalf("err = %v, want it to name the unknown key", err)
	}
}

func TestParseSyntaxErrorNamesLine(t *testing.T) {
	_, err := config.Parse("test.toml", "[[rule]]\naction = \"allow\"\n[[rule\n")
	if err == nil || !strings.Contains(err.Error(), "line") {
		t.Fatalf("err = %v, want it to name the line", err)
	}
}

func TestParseRejectsFileWithoutRules(t *testing.T) {
	if _, err := config.Parse("test.toml", ""); err == nil {
		t.Fatal("a config without rules was accepted")
	}
}

func TestLoadMissingFileGivesDefault(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Rules.Decide("claude", "memory_write", nil); got.Action != rule.Allow {
		t.Fatalf("default decision = %+v, want allow", got)
	}
}

func TestDefaultDBPathFollowsEnvironment(t *testing.T) {
	dir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", dir)
	case "darwin":
		t.Skip("macOS uses the user config directory, which has no override")
	default:
		t.Setenv("XDG_STATE_HOME", dir)
	}
	got, err := config.DefaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "derbent", "derbent.db"); got != want {
		t.Fatalf("DefaultDBPath = %q, want %q", got, want)
	}
}
