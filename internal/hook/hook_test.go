package hook_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/hook"
)

func protocol(t *testing.T, cli string) hook.Protocol {
	t.Helper()
	p, ok := hook.Lookup(cli)
	if !ok {
		t.Fatalf("no protocol %q", cli)
	}
	return p
}

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimRight(string(b), "\r\n"))
}

func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("args %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("args = %s, want %s", gb, wb)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		cli, file                string
		session, dir, tool, args string
	}{
		{"claude", "claude/bash.json", "abc123", "/work/shop", "Bash", `{"command":"git push origin main","description":"Push"}`},
		{"codex", "codex/patch.json", "s-9", "/work/shop", "apply_patch", `{"command":"*** Begin Patch\n*** Update File: a.go\n*** End Patch"}`},
		{"copilot", "copilot/bash-camel.json", "cp-1", "/work/shop", "bash", `{"command":"git push"}`},
		{"copilot", "copilot/bash-object.json", "cp-1", "/work/shop", "bash", `{"command":"git push"}`},
		{"copilot", "copilot/bash-pascal.json", "cp-1", "/work/shop", "bash", `{"command":"git push"}`},
		{"copilot", "copilot/null-args.json", "cp-1", "/work/shop", "list_files", `{}`},
		{"antigravity", "antigravity/run.json", "conv-7", "/work/shop", "run_command", `{"CommandLine":"git push","Cwd":"/work/shop"}`},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			c, err := protocol(t, tc.cli).Parse(golden(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if c.Session != tc.session || c.Dir != tc.dir || c.Tool != tc.tool {
				t.Fatalf("call = %+v", c)
			}
			sameJSON(t, c.Args, tc.args)
		})
	}
}

// A .NET program writing hook input through Process.StandardInput sends a UTF-8 byte order mark when the
// console input encoding is UTF-8. The call behind the mark is read as if the mark were not there.
func TestParseSkipsAByteOrderMark(t *testing.T) {
	for cli, file := range map[string]string{
		"claude": "claude/bash.json", "codex": "codex/patch.json",
		"copilot": "copilot/bash-camel.json", "antigravity": "antigravity/run.json",
	} {
		t.Run(cli, func(t *testing.T) {
			p := protocol(t, cli)
			want, err := p.Parse(golden(t, file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Parse(append([]byte("\xef\xbb\xbf"), golden(t, file)...))
			if err != nil {
				t.Fatalf("with a byte order mark: %v", err)
			}
			if got.Session != want.Session || got.Dir != want.Dir || got.Tool != want.Tool {
				t.Fatalf("call = %+v, want %+v", got, want)
			}
			sameJSON(t, got.Args, string(want.Args))
		})
	}
}

func TestParseRefusesWhatItCannotUse(t *testing.T) {
	for _, tc := range []struct{ cli, in string }{
		{"claude", ``},
		{"claude", `not json`},
		{"claude", `{"session_id":"s","tool_input":{}}`},
		{"claude", `{"tool_name":"Bash","tool_input":{}}`},
		{"copilot", `{"sessionId":"s","toolName":"bash","toolArgs":"{broken"}`},
		{"antigravity", `{"toolCall":{"args":{}},"conversationId":"c"}`},
	} {
		if _, err := protocol(t, tc.cli).Parse([]byte(tc.in)); !errors.Is(err, hook.ErrInput) {
			t.Errorf("%s %q: err = %v, want ErrInput", tc.cli, tc.in, err)
		}
	}
}

func TestAnswers(t *testing.T) {
	deny := gate.HookAnswer{Verdict: gate.Denied, Reason: "derbent: native__Bash is not allowed for this agent (rule 2)"}
	for _, tc := range []struct {
		cli  string
		ans  gate.HookAnswer
		file string
	}{
		{"claude", deny, "claude/deny.out"},
		{"claude", gate.HookAnswer{Verdict: gate.Allowed}, "claude/allow.out"},
		{"codex", deny, "claude/deny.out"},
		{"copilot", deny, "copilot/deny.out"},
		{"antigravity", deny, "antigravity/deny.out"},
	} {
		t.Run(tc.cli+" "+tc.file, func(t *testing.T) {
			if got := protocol(t, tc.cli).Answer(tc.ans); string(got) != string(golden(t, tc.file)) {
				t.Fatalf("answer = %s\nwant     %s", got, golden(t, tc.file))
			}
		})
	}
	for _, cli := range hook.Names() {
		if got := protocol(t, cli).Answer(gate.HookAnswer{Verdict: gate.NoDecision}); got != nil {
			t.Errorf("%s: no decision wrote %s", cli, got)
		}
	}
}

// served accepts what Derbent serves when its config has one downstream server, github.
func served(tool string) bool {
	switch tool {
	case "memory_write", "memory_search", "memory_read":
		return true
	}
	name, _, ok := strings.Cut(tool, "__")
	return ok && name == "github"
}

func TestOwnToolsAreRecognisedPerCLI(t *testing.T) {
	for _, tc := range []struct {
		cli, tool string
		own       bool
	}{
		{"claude", "mcp__derbent__memory_write", true},
		{"claude", "mcp__derbent__github__get_me", true},
		{"claude", "mcp__github__get_me", false},
		{"claude", "Bash", false},
		{"claude", "mcp__derbent__evil__x", false}, // a foreign server entry called derbent__evil
		{"codex", "mcp__derbent__memory_search", true},
		{"codex", "mcp__derbent__evil__x", false},
		{"copilot", "derbent-memory_write", true},
		{"copilot", "derbentx-memory_write", false},
		{"copilot", "derbent-evil-x", false}, // a foreign server entry called derbent-evil
		{"antigravity", "mcp_derbent_memory_write", true},
		{"antigravity", "run_command", false},
		{"antigravity", "mcp_derbent_evil_x", false}, // a foreign server entry called derbent_evil
	} {
		if got := protocol(t, tc.cli).Own("derbent", hook.Call{Tool: tc.tool}, served); got != tc.own {
			t.Errorf("%s Own(%q) = %v, want %v", tc.cli, tc.tool, got, tc.own)
		}
	}
}

func TestOwnChecksTheServerTheCLINames(t *testing.T) {
	for _, tc := range []struct {
		cli, file string
		own       bool
	}{
		{"claude", "claude/own.json", true},             // no mcp_server: older Claude Code
		{"claude", "claude/own-server.json", true},      // mcp_server.name is derbent
		{"claude", "claude/foreign-server.json", false}, // mcp_server.name is derbent-evil
		{"codex", "claude/foreign-server.json", false},
	} {
		p := protocol(t, tc.cli)
		c, err := p.Parse(golden(t, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Own("derbent", c, served); got != tc.own {
			t.Errorf("%s %s: Own = %v, want %v (call %+v)", tc.cli, tc.file, got, tc.own, c)
		}
	}
}

func TestNames(t *testing.T) {
	if got := strings.Join(hook.Names(), ","); got != "antigravity,claude,codex,copilot" {
		t.Fatalf("Names = %s", got)
	}
}
