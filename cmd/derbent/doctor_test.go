package main

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/config"
)

// doctorCmd runs derbent doctor and returns what it printed and its error.
func doctorCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(t.Context(), append([]string{"doctor"}, args...), strings.NewReader(""), &out, io.Discard)
	return out.String(), err
}

// cleanSetup sets all four CLIs and the balanced preset up with init in a new scratch home, and
// returns the home.
func cleanSetup(t *testing.T) string {
	t.Helper()
	home, _ := scratchHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := initCmd(t, "", "--yes", "--preset", "balanced"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return home
}

// copyHome copies every file of from, but the fake CLIs, into to. No file init or the fakes write
// holds the home's own path, so the copy is a clean setup of its own.
func copyHome(t *testing.T, from, to string) {
	t.Helper()
	for path, text := range snapshot(t, from) {
		rel, err := filepath.Rel(from, path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(to, rel), text)
	}
}

// problems returns doctor's problem lines.
func problems(out string) []string {
	var list []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "problem ") {
			list = append(list, line)
		}
	}
	return list
}

// A setup made by init passes: every CLI's hook and MCP entry are found, nothing is a problem, and the
// hook's start time is a note, never a problem, however slow the machine. Doctor writes nothing.
func TestDoctorPassesACleanSetupMadeByInit(t *testing.T) {
	home := cleanSetup(t)
	before := snapshot(t, home)
	out, err := doctorCmd(t)
	if err != nil || len(problems(out)) != 0 {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if after := snapshot(t, home); !maps.Equal(before, after) {
		t.Errorf("doctor changed files: before %v, after %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
	for _, cli := range []string{"claude", "codex", "copilot", "antigravity"} {
		for _, want := range []string{"ok      " + cli + ": the hook in ", "ok      " + cli + ": the MCP entry derbent runs "} {
			if !strings.Contains(out, want) {
				t.Errorf("doctor printed:\n%s\nwant %q", out, want)
			}
		}
	}
	if !strings.Contains(out, "ok      claude: Claude Code 2.1.283 passes a hook its args") {
		t.Errorf("doctor printed:\n%s\nwant Claude Code's version checked", out)
	}
	if !regexp.MustCompile(`(?m)^note    derbent: starting the hook's binary took \S+, the middle of three runs`).MatchString(out) {
		t.Errorf("doctor printed:\n%s\nwant the hook's start time as a note", out)
	}
}

// Each problem, planted alone in a clean setup, is the one problem doctor reports, with its fix, and
// doctor exits with an error.
func TestDoctorNamesEachProblem(t *testing.T) {
	template := cleanSetup(t)
	bin := testBinary(t)
	replace := func(t *testing.T, path, old, updated string) {
		t.Helper()
		text := readString(t, path)
		if !strings.Contains(text, old) {
			t.Fatalf("%s does not hold %q:\n%s", path, old, text)
		}
		writeFile(t, path, strings.Replace(text, old, updated, 1))
	}
	remove := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := func(t *testing.T) string {
		t.Helper()
		p, err := config.DefaultConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, home string)
		want  []string
	}{
		{"no config", func(t *testing.T, _ string) { remove(t, cfgPath(t)) }, []string{"derbent: no config at", "every call is allowed"}},
		{"a config that does not load", func(t *testing.T, _ string) {
			writeFile(t, cfgPath(t), "[[rule]]\naction = \"maybe\"\n")
		}, []string{"derbent: the config does not load"}},
		{"a hook timeout at or below the approvals'", func(t *testing.T, home string) {
			replace(t, filepath.Join(home, ".copilot", "hooks", "derbent.json"), `"timeoutSec": 120`, `"timeoutSec": 30`)
		}, []string{"copilot: the hook's timeout, 30s, is not above [approvals] timeout, 50s"}},
		{"an MCP entry under another name", func(t *testing.T, home string) {
			replace(t, filepath.Join(home, ".claude.json"), `"derbent": {`, `"gate": {`)
		}, []string{"claude: the MCP entry gate runs derbent mcp, but the hook skips only the entry named derbent", "--server gate"}},
		{"agents that differ", func(t *testing.T, home string) {
			if err := setJSONServer(filepath.Join(home, ".claude.json"), "derbent", bin, []string{"mcp", "--agent", "reviewer"}); err != nil {
				t.Fatal(err)
			}
		}, []string{"claude: the hook says --agent claude and the MCP entry --agent reviewer"}},
		{"an agent name without --cli", func(t *testing.T, home string) {
			writeFile(t, filepath.Join(home, ".claude", "settings.json"),
				`{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "`+bin+`", "args": ["gate", "--agent", "claude-work"]}]}]}}`)
			if err := setJSONServer(filepath.Join(home, ".claude.json"), "derbent", bin, []string{"mcp", "--agent", "claude-work"}); err != nil {
				t.Fatal(err)
			}
		}, []string{"claude: the hook's --agent claude-work is not the CLI's name", "--cli claude"}},
		{"a binary that is gone", func(t *testing.T, home string) {
			gone := filepath.ToSlash(filepath.Join(home, "gone", "derbent.exe"))
			writeFile(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"),
				`{"mcpServers": {"derbent": {"command": "`+gone+`", "args": ["mcp", "--agent", "antigravity"]}}}`)
		}, []string{"antigravity: ", "gone/derbent.exe, which does not exist"}},
		{"a variable Codex does not pass", func(t *testing.T, _ string) {
			path := cfgPath(t)
			writeFile(t, path, readString(t, path)+
				"\n[servers.github]\ncommand = [\"github-mcp-server\", \"stdio\"]\nenv = { GITHUB_PERSONAL_ACCESS_TOKEN = \"${env:DERBENT_TEST_TOKEN}\" }\n")
		}, []string{"codex: Derbent's config uses ${env:DERBENT_TEST_TOKEN}", `env_vars = ["DERBENT_TEST_TOKEN"]`}},
		{"no hook", func(t *testing.T, home string) {
			remove(t, filepath.Join(home, ".gemini", "config", "hooks.json"))
		}, []string{"antigravity: no pre-tool hook runs derbent gate", "derbent init --cli antigravity"}},
		{"a CLI file that does not parse", func(t *testing.T, home string) {
			writeFile(t, filepath.Join(home, ".copilot", "hooks", "derbent.json"), "{")
		}, []string{"copilot: ", "does not parse as JSON"}},
		{"a database that cannot be written", func(t *testing.T, _ string) {
			db, err := config.DefaultDBPath()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(db, 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"derbent: database ", "cannot be written"}},
		{"a Claude Code too old for the exec-form hook", func(t *testing.T, _ string) {
			t.Setenv("DERBENT_TEST_FAKE_CLAUDE_VERSION", "2.1.100 (Claude Code)")
		}, []string{"claude: Claude Code 2.1.100 is older than 2.1.139", "claude update"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := scratchHome(t)
			copyHome(t, template, home)
			tc.plant(t, home)
			out, err := doctorCmd(t)
			if !errors.Is(err, errDoctorFound) {
				t.Fatalf("err = %v, want errDoctorFound\n%s", err, out)
			}
			found := problems(out)
			if len(found) != 1 || !strings.Contains(found[0], "; fix: ") {
				t.Fatalf("doctor printed:\n%s\nwant exactly one problem with its fix", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(found[0], want) {
					t.Errorf("problem %q lacks %q", found[0], want)
				}
			}
		})
	}
}

// What doctor cannot check is a note, not a problem: a Claude Code version it cannot read, claude not on
// PATH, and a hook file init only looks in that does not parse as JSON.
func TestDoctorNotesWhatItCannotCheck(t *testing.T) {
	home := cleanSetup(t)
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	writeFile(t, settings, "{\n  // my settings\n  \"model\": \"x\"\n}\n")
	t.Setenv("DERBENT_TEST_FAKE_CLAUDE_VERSION", "claude, some build")
	out, err := doctorCmd(t)
	if err != nil || len(problems(out)) != 0 {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"note    claude: could not read Claude Code's version",
		"note    antigravity: " + settings + " does not parse as JSON, so doctor did not check it for a derbent gate hook",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor printed:\n%s\nwant %q", out, want)
		}
	}
	if err = os.Remove(filepath.Join(home, "bin", "claude"+exeSuffix())); err != nil {
		t.Fatal(err)
	}
	out, err = doctorCmd(t, "--cli", "claude")
	if err != nil || !strings.Contains(out, "note    claude: claude is not on PATH, so Claude Code's version was not checked") {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
}

// Values from a CLI's config reach the terminal escaped.
func TestDoctorEscapesWhatItReads(t *testing.T) {
	home := cleanSetup(t)
	writeFile(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"),
		`{"mcpServers": {"derbent": {"command": "/gone/derbent\u001b]0;x\u0007", "args": ["mcp", "--agent", "antigravity"]}}}`)
	out, _ := doctorCmd(t)
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, `\u001b`) {
		t.Fatalf("doctor printed a raw control character or no escaped one:\n%q", out)
	}
}
