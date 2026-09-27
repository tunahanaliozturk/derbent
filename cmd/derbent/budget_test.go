package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Budgets from the config apply to hook calls and to MCP calls, through the real binary.
func TestABudgetInTheConfigRefusesHookAndMCPCalls(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte(`
[[budget]]
agent = "claude"
tool  = "native__Bash"
calls = 1
per   = "1h"

[[budget]]
tool  = "memory_search"
calls = 1
per   = "1m"

[[rule]]
action = "allow"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := runHook(t, dir, claudeHookInput(dir, "echo one"), "--agent", "claude"); code != 0 || out != "" {
		t.Fatalf("first hook call: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	out, _, _ := runHook(t, dir, claudeHookInput(dir, "echo two"), "--agent", "claude")
	if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "budget 1 in the config is used up: 1 call to native__Bash per 1h for claude") {
		t.Fatalf("second hook call answered %q", out)
	}
	cs := connectProcess(t, dir, "codex", cfg)
	defer cs.Close()
	for i, wantRefused := range []bool{false, true} {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_search", Arguments: map[string]any{"query": "x"}})
		if err != nil || res.IsError != wantRefused {
			t.Fatalf("memory_search %d: err %v, result %q", i+1, err, resultText(res))
		}
		if wantRefused && !strings.Contains(resultText(res), "budget 2 in the config is used up: 1 call to memory_search per 1m for codex") {
			t.Fatalf("memory_search %d: %q", i+1, resultText(res))
		}
	}
	var by []string
	for _, r := range hookReceiptRows(t, dir) {
		by = append(by, r.decidedBy)
	}
	if !slices.Equal(by, []string{"rule:1", "budget:1", "rule:1", "budget:2"}) {
		t.Fatalf("decided_by = %v", by)
	}
}
