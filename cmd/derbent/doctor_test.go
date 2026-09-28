package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/store"
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

// replaceIn replaces every old in the file at path, which must hold it.
func replaceIn(t *testing.T, path, old, updated string) {
	t.Helper()
	text := readString(t, path)
	if !strings.Contains(text, old) {
		t.Fatalf("%s does not hold %q:\n%s", path, old, text)
	}
	writeFile(t, path, strings.ReplaceAll(text, old, updated))
}

func defaultConfig(t *testing.T) string {
	t.Helper()
	p, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func defaultDB(t *testing.T) string {
	t.Helper()
	p, err := config.DefaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// addServerWithEnv adds a server to Derbent's default config that takes DERBENT_TEST_TOKEN from the
// environment.
func addServerWithEnv(t *testing.T) {
	t.Helper()
	path := defaultConfig(t)
	writeFile(t, path, readString(t, path)+
		"\n[servers.github]\ncommand = [\"github-mcp-server\", \"stdio\"]\nenv = { GITHUB_PERSONAL_ACCESS_TOKEN = \"${env:DERBENT_TEST_TOKEN}\" }\n")
}

// withCopilotHookConfig has Copilot CLI's hook pass --config path.
func withCopilotHookConfig(t *testing.T, home, path string) {
	t.Helper()
	replaceIn(t, filepath.Join(home, ".copilot", "hooks", "derbent.json"), "gate --agent copilot", "gate --agent copilot --config "+filepath.ToSlash(path))
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
	for _, want := range []string{
		"ok      claude: Claude Code 2.1.283 passes a hook its args",
		"note    codex: Codex runs a new hook only after you trust it in /hooks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor printed:\n%s\nwant %q", out, want)
		}
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
	remove := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	unixOnly := func(t *testing.T) {
		t.Helper()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs Unix file modes that bind the user")
		}
	}
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, home string)
		want  []string
	}{
		{"no config", func(t *testing.T, _ string) { remove(t, defaultConfig(t)) }, []string{"derbent: no config at", "every call is allowed"}},
		{"a config that does not load", func(t *testing.T, _ string) {
			writeFile(t, defaultConfig(t), "[[rule]]\naction = \"maybe\"\n")
		}, []string{"derbent: the config does not load"}},
		{"a hook timeout at or below the approvals'", func(t *testing.T, home string) {
			replaceIn(t, filepath.Join(home, ".copilot", "hooks", "derbent.json"), `"timeoutSec": 120`, `"timeoutSec": 30`)
		}, []string{"copilot: the hook's timeout, 30s, is not above [approvals] timeout, 50s"}},
		{"an MCP entry under another name", func(t *testing.T, home string) {
			replaceIn(t, filepath.Join(home, ".claude.json"), `"derbent": {`, `"gate": {`)
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
		{"a hook whose binary is gone", func(t *testing.T, home string) {
			gone := filepath.ToSlash(filepath.Join(home, "gone", "derbent.exe"))
			writeFile(t, filepath.Join(home, ".gemini", "config", "hooks.json"),
				`{"derbent": {"PreToolUse": [{"matcher": ".*", "hooks": [{"type": "command", "command": "\"`+gone+`\" gate --agent antigravity", "timeout": 120}]}]}}`)
		}, []string{"antigravity: ", "hooks.json names ", "gone/derbent.exe, which does not exist"}},
		{"a program that is a directory", func(t *testing.T, home string) {
			dir := filepath.Join(home, "dir", "derbent.exe")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"),
				`{"mcpServers": {"derbent": {"command": "`+filepath.ToSlash(dir)+`", "args": ["mcp", "--agent", "antigravity"]}}}`)
		}, []string{"antigravity: ", "dir/derbent.exe, which is not a file"}},
		{"a program that is not executable", func(t *testing.T, home string) {
			unixOnly(t)
			prog := filepath.Join(home, "plain", "derbent")
			writeFile(t, prog, "#!/bin/sh\n")
			writeFile(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"),
				`{"mcpServers": {"derbent": {"command": "`+prog+`", "args": ["mcp", "--agent", "antigravity"]}}}`)
		}, []string{"antigravity: ", "plain/derbent, which is not executable"}},
		{"a variable Codex does not pass", func(t *testing.T, _ string) {
			addServerWithEnv(t)
		}, []string{"codex: Derbent's config ", "uses ${env:DERBENT_TEST_TOKEN}", `env_vars = ["DERBENT_TEST_TOKEN"]`}},
		{"no hook", func(t *testing.T, home string) {
			remove(t, filepath.Join(home, ".gemini", "config", "hooks.json"))
		}, []string{"antigravity: no pre-tool hook runs derbent gate", "derbent init --cli antigravity"}},
		{"two hooks that run the gate", func(t *testing.T, home string) {
			dir := filepath.Join(home, ".copilot", "hooks")
			writeFile(t, filepath.Join(dir, "mine.json"), readString(t, filepath.Join(dir, "derbent.json")))
		}, []string{"copilot: 2 hooks run derbent gate", "derbent.json", "mine.json", "every call is decided twice"}},
		{"a hook that gates only some tools", func(t *testing.T, home string) {
			replaceIn(t, filepath.Join(home, ".claude", "settings.json"), `"matcher": "*"`, `"matcher": "Bash"`)
		}, []string{"claude: the hook in ", `matches only "Bash"`, `set its matcher to "*"`}},
		{"a local-scope MCP entry, which Claude Code uses before the user one", func(t *testing.T, home string) {
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(home, ".claude.json")
			var root map[string]any
			if err = json.Unmarshal([]byte(readString(t, file)), &root); err != nil {
				t.Fatal(err)
			}
			root["projects"] = map[string]any{wd: map[string]any{"mcpServers": map[string]any{
				"derbent": map[string]any{"command": bin, "args": []string{"mcp", "--agent", "reviewer"}},
			}}}
			data, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, file, string(data))
		}, []string{"claude: the hook says --agent claude and the MCP entry --agent reviewer"}},
		{"a second MCP entry that runs derbent mcp", func(t *testing.T, home string) {
			if err := setJSONServer(filepath.Join(home, ".claude.json"), "derbent-2", bin, []string{"mcp", "--agent", "claude"}); err != nil {
				t.Fatal(err)
			}
		}, []string{"claude: the MCP entry derbent-2 runs derbent mcp, but the hook skips only the entry named derbent", "remove the entry derbent-2"}},
		{"a hook config that does not exist", func(t *testing.T, home string) {
			withCopilotHookConfig(t, home, filepath.Join(home, "none.toml"))
		}, []string{"copilot: the hook's --config names ", "none.toml, which does not exist"}},
		{"a hook config with a longer approval timeout", func(t *testing.T, home string) {
			path := filepath.Join(home, "hook.toml")
			writeFile(t, path, "[approvals]\ntimeout = \"150s\"\n\n[[rule]]\naction = \"allow\"\n")
			withCopilotHookConfig(t, home, path)
		}, []string{"copilot: the hook's timeout, 2m0s, is not above [approvals] timeout, 2m30s, in ", "hook.toml"}},
		{"a CLI file that does not parse", func(t *testing.T, home string) {
			writeFile(t, filepath.Join(home, ".copilot", "hooks", "derbent.json"), "{")
		}, []string{"copilot: ", "does not parse as JSON"}},
		{"a database that cannot be written", func(t *testing.T, _ string) {
			if err := os.MkdirAll(defaultDB(t), 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"derbent: database ", "cannot be written"}},
		{"a database that is not Derbent's", func(t *testing.T, _ string) {
			writeFile(t, defaultDB(t), "not a database, just some text")
		}, []string{"derbent: database ", "not a database"}},
		{"a database directory the user cannot create files in", func(t *testing.T, _ string) {
			unixOnly(t)
			dir := filepath.Dir(defaultDB(t))
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		}, []string{"derbent: ", "cannot create the database"}},
		{"a hook binary that does not start in time", func(t *testing.T, _ string) {
			limit := hookStartLimit
			hookStartLimit = time.Nanosecond
			t.Cleanup(func() { hookStartLimit = limit })
		}, []string{"derbent: ", "version did not finish within 1ns"}},
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

// What looks like a problem but is not one passes, and doctor leaves every file as it was, an existing
// database byte for byte with nothing new beside it.
func TestDoctorPassesWhatIsNotAProblem(t *testing.T) {
	template := cleanSetup(t)
	bin := testBinary(t)
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, home string)
	}{
		{"a database that exists", func(t *testing.T, _ string) {
			db, err := store.Open(t.Context(), defaultDB(t))
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
		}},
		{"a variable the Codex entry sets in env", func(t *testing.T, home string) {
			addServerWithEnv(t)
			replaceIn(t, filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.derbent]\n", "[mcp_servers.derbent]\nenv = { DERBENT_TEST_TOKEN = \"x\" }\n")
		}},
		{"a Codex entry with a config of its own", func(t *testing.T, home string) {
			addServerWithEnv(t)
			path := filepath.Join(home, "mcp.toml")
			writeFile(t, path, "[[rule]]\naction = \"allow\"\n")
			replaceIn(t, filepath.Join(home, ".codex", "config.toml"), `"--agent", "codex"]`, `"--agent", "codex", "--config", "`+filepath.ToSlash(path)+`"]`)
		}},
		{"a hook with a config of its own", func(t *testing.T, home string) {
			replaceIn(t, filepath.Join(home, ".copilot", "hooks", "derbent.json"), `"timeoutSec": 120`, `"timeoutSec": 30`)
			path := filepath.Join(home, "hook.toml")
			writeFile(t, path, "[approvals]\ntimeout = \"20s\"\n\n[[rule]]\naction = \"allow\"\n")
			withCopilotHookConfig(t, home, path)
		}},
		{"an old Claude Code with a hook in shell form", func(t *testing.T, home string) {
			t.Setenv("DERBENT_TEST_FAKE_CLAUDE_VERSION", "2.1.100 (Claude Code)")
			writeFile(t, filepath.Join(home, ".claude", "settings.json"),
				`{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "\"`+bin+`\" gate --agent claude"}]}]}}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := scratchHome(t)
			copyHome(t, template, home)
			tc.plant(t, home)
			before := snapshot(t, home)
			out, err := doctorCmd(t)
			if err != nil || len(problems(out)) != 0 {
				t.Fatalf("doctor: %v\n%s", err, out)
			}
			if after := snapshot(t, home); !maps.Equal(before, after) {
				t.Errorf("doctor changed files: before %v, after %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
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
