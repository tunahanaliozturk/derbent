package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestAddedTextIsGolden fixes, per CLI, the command init runs and the text it adds, for a binary
// whose path holds a space: exec form for Claude Code, & "<path>" for PowerShell, and a double-quoted
// word in every other shell line.
func TestAddedTextIsGolden(t *testing.T) {
	const bin = "C:/Program Files/Derbent/derbent.exe"
	p := Paths{MCP: "mcp", Hook: "hook"}
	for _, tc := range []struct{ cli, mcp, hook string }{
		{
			"claude",
			`claude mcp add --scope user derbent -- "C:/Program Files/Derbent/derbent.exe" mcp --agent claude`,
			`{
  "hooks": [
    {
      "args": [
        "gate",
        "--agent",
        "claude"
      ],
      "command": "C:/Program Files/Derbent/derbent.exe",
      "type": "command"
    }
  ],
  "matcher": "*"
}`,
		},
		{
			"codex",
			`codex mcp add derbent -- "C:/Program Files/Derbent/derbent.exe" mcp --agent codex`,
			`# derbent init added Derbent's pre-tool hook; derbent doctor checks it.
[[hooks.PreToolUse]]
matcher = ".*"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "\"C:/Program Files/Derbent/derbent.exe\" gate --agent codex"
`,
		},
		{
			"copilot",
			`copilot mcp add derbent -- "C:/Program Files/Derbent/derbent.exe" mcp --agent copilot`,
			`{
  "bash": "\"C:/Program Files/Derbent/derbent.exe\" gate --agent copilot",
  "powershell": "& \"C:/Program Files/Derbent/derbent.exe\" gate --agent copilot",
  "timeoutSec": 120,
  "type": "command"
}`,
		},
		{
			"antigravity",
			`{
  "derbent": {
    "args": [
      "mcp",
      "--agent",
      "antigravity"
    ],
    "command": "C:/Program Files/Derbent/derbent.exe"
  }
}`,
			`{
  "derbent": {
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "\"C:/Program Files/Derbent/derbent.exe\" gate --agent antigravity",
            "timeout": 120,
            "type": "command"
          }
        ],
        "matcher": ".*"
      }
    ]
  }
}`,
		},
	} {
		if got := mcpChange(tc.cli, p, bin).Text; got != tc.mcp {
			t.Errorf("%s MCP entry:\n%s\nwant:\n%s", tc.cli, got, tc.mcp)
		}
		if got := hookChange(tc.cli, p, bin).Text; got != tc.hook {
			t.Errorf("%s hook:\n%s\nwant:\n%s", tc.cli, got, tc.hook)
		}
	}
}

func TestShellWordAndCheckBinary(t *testing.T) {
	for in, want := range map[string]string{
		"C:/Users/ada/bin/derbent.exe":         "C:/Users/ada/bin/derbent.exe",
		"C:/Users/TUNAHA~1/bin/derbent.exe":    "C:/Users/TUNAHA~1/bin/derbent.exe",
		"/home/ada/.local/bin/derbent":         "/home/ada/.local/bin/derbent",
		"C:/Program Files/Derbent/derbent.exe": `"C:/Program Files/Derbent/derbent.exe"`,
		"C:/Program Files (x86)/derbent.exe":   `"C:/Program Files (x86)/derbent.exe"`,
		"C:/Users/O'Brien/derbent.exe":         `"C:/Users/O'Brien/derbent.exe"`,
	} {
		if got := ShellWord(in); got != want {
			t.Errorf("ShellWord(%q) = %q, want %q", in, got, want)
		}
		if err := CheckBinary(in); err != nil {
			t.Errorf("CheckBinary(%q) = %v, want nil", in, err)
		}
	}
	for _, bad := range []string{`C:/a"b/derbent.exe`, "/home/$USER/derbent", "C:/100%/derbent.exe", "/tmp/wow!/derbent", "/tmp/`x`/derbent", "/tmp/a\nb/derbent"} {
		if err := CheckBinary(bad); err == nil {
			t.Errorf("CheckBinary(%q) = nil, want it refused", bad)
		}
	}
}

// Codex's config.toml is appended to, never re-encoded: every byte of it stays, comments and CRLF line
// endings included. A file that does not parse, or would not parse with the hook appended, is refused.
func TestAppendTOMLKeepsTheFileAndRefusesAnAppendThatBreaksIt(t *testing.T) {
	text := hookChange("codex", Paths{Hook: "config.toml"}, "/usr/local/bin/derbent").Text
	old := []byte("# my settings\r\nmodel = \"gpt-5\" # the default\r\n\r\n[mcp_servers.docs]\r\ncommand = \"docs-mcp\"\r\n")
	out, err := AppendTOML("config.toml", old, text)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, old) {
		t.Fatalf("the file's own bytes changed:\n%q", out)
	}
	var c codexConfig
	if _, err = toml.Decode(string(out), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Hooks.PreToolUse) != 1 || c.Hooks.PreToolUse[0].Hooks[0].Command != "/usr/local/bin/derbent gate --agent codex" {
		t.Fatalf("hooks = %+v", c.Hooks)
	}
	if out, err = AppendTOML("config.toml", []byte(`model = "x"`), text); err != nil || !strings.HasPrefix(string(out), "model = \"x\"\n\n# derbent init") {
		t.Fatalf("a file without a final newline: err %v, got %q", err, out)
	}
	for _, bad := range []string{"model = \n", "hooks = \"off\"\n"} {
		if _, err = AppendTOML("config.toml", []byte(bad), text); err == nil || !strings.Contains(err.Error(), "config.toml") {
			t.Errorf("%q: err = %v, want an error naming the file", bad, err)
		}
	}
}

// TestEntryRunsGateInEveryForm reads the hook forms a user or an earlier init may have written.
func TestEntryRunsGateInEveryForm(t *testing.T) {
	for _, e := range []Entry{
		{Command: "derbent gate --agent claude", Shell: true},
		{Command: `"C:/Program Files/Derbent/derbent.exe" gate --agent claude`, Shell: true},
		{Command: `& "C:/Program Files/Derbent/derbent.exe" gate --agent copilot`, Shell: true},
		{Command: "'/opt/my tools/derbent' gate --agent codex", Shell: true},
		{Command: "/opt/derbent/derbent-v1.0.0-linux-amd64 gate --agent codex", Shell: true},
		{Command: "C:/Program Files/Derbent/derbent.exe", Args: []string{"gate", "--agent", "claude"}},
	} {
		if !e.RunsGate() || e.RunsMCP() {
			t.Errorf("%+v: RunsGate %v, RunsMCP %v; want a gate", e, e.RunsGate(), e.RunsMCP())
		}
	}
	for _, e := range []Entry{
		{Command: "derbent mcp --agent claude", Shell: true},
		{Command: "echo derbent gate", Shell: true},
		{Command: "derbent", Args: []string{"mcp", "--agent", "claude"}},
		{Command: "", Shell: true},
	} {
		if e.RunsGate() {
			t.Errorf("%+v: taken for derbent gate", e)
		}
	}
	e := Entry{Command: `& "C:/Program Files/Derbent/derbent.exe" gate --agent=copilot-work --cli copilot`, Shell: true}
	if a, ok := e.Flag("--agent"); !ok || a != "copilot-work" {
		t.Errorf("--agent = %q, %v", a, ok)
	}
	if c, ok := e.Flag("--cli"); !ok || c != "copilot" {
		t.Errorf("--cli = %q, %v", c, ok)
	}
	if _, ok := e.Flag("--server"); ok {
		t.Error("--server found where there is none")
	}
	if got := e.Program(); got != "C:/Program Files/Derbent/derbent.exe" {
		t.Errorf("Program() = %q", got)
	}
}

// A CLI's own variable moves its files, as it moves them for the CLI.
func TestPathsFollowTheCLIsOwnVariables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME"} {
		t.Setenv(k, "")
	}
	check := func(cli string, want Paths) {
		t.Helper()
		if got, err := PathsOf(cli); err != nil || got != want {
			t.Errorf("PathsOf(%s) = %+v, %v; want %+v", cli, got, err, want)
		}
	}
	join := func(elem ...string) string { return filepath.Join(append([]string{home}, elem...)...) }
	check("claude", Paths{MCP: join(".claude.json"), Hook: join(".claude", "settings.json")})
	check("codex", Paths{MCP: join(".codex", "config.toml"), Hook: join(".codex", "config.toml")})
	check("copilot", Paths{MCP: join(".copilot", "mcp-config.json"), Hook: join(".copilot", "hooks", "derbent.json"), HookDir: join(".copilot", "hooks")})
	check("antigravity", Paths{MCP: join(".gemini", "config", "mcp_config.json"), Hook: join(".gemini", "config", "hooks.json")})
	t.Setenv("CLAUDE_CONFIG_DIR", join("cc"))
	t.Setenv("CODEX_HOME", join("cx"))
	t.Setenv("COPILOT_HOME", join("cp"))
	check("claude", Paths{MCP: join("cc", ".claude.json"), Hook: join("cc", "settings.json")})
	check("codex", Paths{MCP: join("cx", "config.toml"), Hook: join("cx", "config.toml")})
	check("copilot", Paths{MCP: join("cp", "mcp-config.json"), Hook: join("cp", "hooks", "derbent.json"), HookDir: join("cp", "hooks")})
	if _, err := PathsOf("gemini"); err == nil {
		t.Error("PathsOf(gemini) = nil error")
	}
	if _, err := Select("claude,gemini"); err == nil || !strings.Contains(err.Error(), "use claude, codex, copilot, antigravity") {
		t.Errorf("Select: err = %v, want one listing the four CLIs", err)
	}
	if got, err := Select("copilot, claude"); err != nil || !slices.Equal(got, []string{"claude", "copilot"}) {
		t.Errorf("Select = %v, %v; want claude and copilot in the usual order", got, err)
	}
}

// A JSON file keeps its other keys and numbers as written; a byte order mark goes, and a key init needs
// as an object that holds something else is an error.
func TestEditJSONKeepsOtherKeys(t *testing.T) {
	c := hookChange("claude", Paths{Hook: "settings.json"}, "/usr/local/bin/derbent")
	out, err := c.edit([]byte("\uFEFF{\"model\": \"opus\", \"ratio\": 1.50}"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(out, []byte("\uFEFF")) || !bytes.Contains(out, []byte(`"ratio": 1.50`)) || !bytes.Contains(out, []byte(`"model": "opus"`)) || !bytes.HasSuffix(out, []byte("}\n")) {
		t.Fatalf("got:\n%s", out)
	}
	var root map[string]any
	if err = json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	if _, err = c.edit([]byte(`{"hooks": []}`)); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("hooks as an array: err = %v, want an error naming the file", err)
	}
	if _, err = c.edit([]byte(`{"hooks": `)); err == nil {
		t.Fatal("a file that does not parse was edited")
	}
}

// Claude Code keeps local-scope entries under the project's path, with the drive letter in either case
// on Windows; only the current directory's count.
func TestClaudeLocalScopeIsKeyedByTheDirectory(t *testing.T) {
	dir := t.TempDir()
	here := filepath.ToSlash(filepath.Join(dir, "work"))
	registry := filepath.Join(dir, ".claude.json")
	data, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{"derbent": map[string]any{"command": "derbent", "args": []string{"mcp", "--agent", "claude"}}},
		"projects": map[string]any{
			here:            map[string]any{"mcpServers": map[string]any{"gate": map[string]any{"command": "derbent", "args": []string{"mcp"}}}},
			here + "-other": map[string]any{"mcpServers": map[string]any{"elsewhere": map[string]any{"command": "x"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registry, data, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := MCPEntries("claude", Paths{MCP: registry}, filepath.Join(dir, "work"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"derbent", "gate"}) {
		t.Fatalf("entries = %v, want derbent and gate", names)
	}
}
