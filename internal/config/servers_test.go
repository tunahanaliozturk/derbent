package config_test

import (
	"strings"
	"testing"

	"github.com/tunahanaliozturk/portcullis/internal/config"
)

const rulesTail = "\n[[rule]]\naction = \"allow\"\n"

func TestParseServers(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_TOKEN", "ghp_0123456789abcdef")
	cfg, err := config.Parse("test.toml", `
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:PORTCULLIS_TEST_TOKEN}" }

[servers.docs]
url     = "https://docs.example.com/mcp"
headers = { Authorization = "Bearer ${env:PORTCULLIS_TEST_TOKEN}" }

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
		"missing variable":        {"[servers.a]\ncommand = [\"x\"]\nenv = { T = \"${env:PORTCULLIS_TEST_UNSET}\" }\n", "PORTCULLIS_TEST_UNSET"},
		"both command and url":    {"[servers.a]\ncommand = [\"x\"]\nurl = \"https://a.example\"\n", "exactly one"},
		"neither command nor url": {"[servers.a]\nenv = { A = \"b\" }\n", "exactly one"},
		"plain http to a remote":  {"[servers.a]\nurl = \"http://mcp.example.com\"\n", "https"},
		"headers on a command":    {"[servers.a]\ncommand = [\"x\"]\nheaders = { A = \"b\" }\n", "headers"},
		"env on a url":            {"[servers.a]\nurl = \"https://a.example\"\nenv = { A = \"b\" }\n", "env"},
		"underscore in name":      {"[servers.git_hub]\ncommand = [\"x\"]\n", "git_hub"},
		"reserved name":           {"[servers.native]\ncommand = [\"x\"]\n", "reserved"},
		"bad redact pattern":      {"[receipts]\nredact = ['(']\n", "redact"},
		"unknown server key":      {"[servers.a]\ncommand = [\"x\"]\ncwd = \"/tmp\"\n", "cwd"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse("test.toml", tc.toml+rulesTail)
			if err == nil || !strings.Contains(err.Error(), tc.wantInError) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantInError)
			}
		})
	}
}

func TestMissingVariableErrorNeverShowsOtherValues(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_TOKEN", "ghp_0123456789abcdef")
	_, err := config.Parse("test.toml", `
[servers.a]
command = ["x"]
env = { A = "${env:PORTCULLIS_TEST_TOKEN}", B = "${env:PORTCULLIS_TEST_UNSET}" }
`+rulesTail)
	if err == nil || strings.Contains(err.Error(), "ghp_0123456789abcdef") {
		t.Fatalf("err = %v, want an error without the resolved value", err)
	}
}
