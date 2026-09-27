package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/config"
)

const rulesTail = "\n[[rule]]\naction = \"allow\"\n"

func TestParseServers(t *testing.T) {
	t.Setenv("DERBENT_TEST_TOKEN", "ghp_0123456789abcdef")
	cfg, err := config.Parse("test.toml", `
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:DERBENT_TEST_TOKEN}" }

[servers.docs]
url     = "https://docs.example.com/mcp"
headers = { Authorization = "Bearer ${env:DERBENT_TEST_TOKEN}" }

[servers.local]
url = "http://127.0.0.1:8080/mcp"
`+rulesTail)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 3 || cfg.Servers[0].Name != "docs" || cfg.Servers[1].Name != "github" || cfg.Servers[2].Name != "local" {
		t.Fatalf("servers = %+v, want docs, github, local in that order", cfg.Servers)
	}
	if got := cfg.Servers[1].Env["GITHUB_PERSONAL_ACCESS_TOKEN"]; got != "ghp_0123456789abcdef" {
		t.Fatalf("env not resolved: %q", got)
	}
	if got := cfg.Servers[0].Headers["Authorization"]; got != "Bearer ghp_0123456789abcdef" {
		t.Fatalf("header not resolved: %q", got)
	}
	if got := cfg.Redact.JSON(`{"note":"token ghp_0123456789abcdef"}`); strings.Contains(got, "ghp_0123456789abcdef") {
		t.Fatalf("a resolved secret is not masked: %s", got)
	}
}

func TestParseRedactPatterns(t *testing.T) {
	cfg, err := config.Parse("test.toml", "[receipts]\nredact = ['sk-[a-z]+']\n"+rulesTail)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Redact.JSON(`{"key":"sk-abc"}`); got != `{"key":"[redacted]"}` {
		t.Fatalf("JSON = %s", got)
	}
}

func TestParseRejectsBadServers(t *testing.T) {
	tests := map[string]struct{ toml, wantInError string }{
		"missing variable":           {"[servers.a]\ncommand = [\"x\"]\nenv = { T = \"${env:DERBENT_TEST_UNSET}\" }\n", "DERBENT_TEST_UNSET"},
		"both command and url":       {"[servers.a]\ncommand = [\"x\"]\nurl = \"https://a.example\"\n", "exactly one"},
		"neither command nor url":    {"[servers.a]\nenv = { A = \"b\" }\n", "exactly one"},
		"plain http to a remote":     {"[servers.a]\nurl = \"http://mcp.example.com\"\n", "https"},
		"headers on a command":       {"[servers.a]\ncommand = [\"x\"]\nheaders = { A = \"b\" }\n", "headers"},
		"env on a url":               {"[servers.a]\nurl = \"https://a.example\"\nenv = { A = \"b\" }\n", "env"},
		"env reference in a url":     {"[servers.a]\nurl = \"https://a.example/mcp?key=${env:DERBENT_TEST_TOKEN}\"\n", "headers"},
		"env reference in a command": {"[servers.a]\ncommand = [\"srv\", \"--token=${env:DERBENT_TEST_TOKEN}\"]\n", "pass secrets to the server in env"},
		"underscore in name":         {"[servers.git_hub]\ncommand = [\"x\"]\n", "git_hub"},
		"reserved name":              {"[servers.native]\ncommand = [\"x\"]\n", "reserved"},
		"bad redact pattern":         {"[receipts]\nredact = ['(']\n", "redact"},
		"unknown server key":         {"[servers.a]\ncommand = [\"x\"]\ncwd = \"/tmp\"\n", "cwd"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse("test.toml", tc.toml+rulesTail)
			if err == nil || !strings.Contains(err.Error(), tc.wantInError) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantInError)
			}
			if name == "missing variable" {
				return // the one mistake the hook's loader lets pass
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err = os.WriteFile(path, []byte(tc.toml+rulesTail), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = config.LoadForHook(path); err == nil || !strings.Contains(err.Error(), tc.wantInError) {
				t.Fatalf("LoadForHook: err = %v, want it to mention %q", err, tc.wantInError)
			}
		})
	}
}

// A pre-tool hook runs without the secrets a CLI gives only to its MCP server entry. Its loader leaves
// a reference to a variable that is not set as written, and still resolves and masks one that is set.
func TestLoadForHookLeavesUnsetServerVariables(t *testing.T) {
	t.Setenv("DERBENT_TEST_TOKEN", "ghp_0123456789abcdef")
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := `
[servers.a]
command = ["x"]
env = { A = "${env:DERBENT_TEST_TOKEN}", B = "${env:DERBENT_TEST_UNSET}" }

[servers.b]
url     = "https://b.example/mcp"
headers = { Authorization = "Bearer ${env:DERBENT_TEST_UNSET}" }
` + rulesTail
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "DERBENT_TEST_UNSET") {
		t.Fatalf("Load: err = %v, want it to name DERBENT_TEST_UNSET", err)
	}
	got, err := config.LoadForHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 2 || got.Servers[0].Env["A"] != "ghp_0123456789abcdef" || got.Servers[0].Env["B"] != "${env:DERBENT_TEST_UNSET}" ||
		got.Servers[1].Headers["Authorization"] != "Bearer ${env:DERBENT_TEST_UNSET}" {
		t.Fatalf("servers = %+v", got.Servers)
	}
	if masked := got.Redact.JSON(`{"note":"token ghp_0123456789abcdef"}`); strings.Contains(masked, "ghp_0123456789abcdef") {
		t.Fatalf("a resolved secret is not masked: %s", masked)
	}
}

func TestMissingVariableErrorNeverShowsOtherValues(t *testing.T) {
	t.Setenv("DERBENT_TEST_TOKEN", "ghp_0123456789abcdef")
	_, err := config.Parse("test.toml", `
[servers.a]
command = ["x"]
env = { A = "${env:DERBENT_TEST_TOKEN}", B = "${env:DERBENT_TEST_UNSET}" }
`+rulesTail)
	if err == nil || strings.Contains(err.Error(), "ghp_0123456789abcdef") {
		t.Fatalf("err = %v, want an error without the resolved value", err)
	}
}

func TestServersArePinnedUnlessTheySayNot(t *testing.T) {
	cfg, err := config.Parse("test.toml", "[servers.a]\ncommand = ['a']\n\n[servers.b]\ncommand = ['b']\npin = false\n\n[[rule]]\naction = \"allow\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 || !cfg.Servers[0].Pin || cfg.Servers[1].Pin {
		t.Fatalf("servers = %+v, want a pinned and b not", cfg.Servers)
	}
}
