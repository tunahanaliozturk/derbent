package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeProjectFile(t *testing.T, dir, rules string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".derbent.toml"), []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The milestone's evidence for project rules through the real binary: a .derbent.toml makes a call ask
// that the user's rules allow, pending and approve name the project rule, and the approval lets it run.
func TestAProjectRuleAsksThroughTheHookAndItsApprovalNamesIt(t *testing.T) {
	dir := t.TempDir()
	writeAllowConfig(t, dir)
	writeProjectFile(t, dir, "[[rule]]\ntool = \"native__Bash\"\nargs = { command = \"git status*\" }\naction = \"ask\"\n")
	db := filepath.Join(dir, "p.db")
	done := make(chan string, 1)
	go func() {
		out, _, _ := runHook(t, dir, claudeHookInput(dir, "git status"), "--agent", "claude")
		done <- out
	}()
	id := awaitHookApproval(t, dir)
	if out := runOK(t, "pending", "--db", db); !strings.Contains(out, "project rule 1") {
		t.Fatalf("pending:\n%s", out)
	}
	var line pendingLine
	if err := json.Unmarshal([]byte(runOK(t, "pending", "--json", "--db", db)), &line); err != nil || !line.ProjectRule || line.Rule != 1 {
		t.Fatalf("pending --json = %+v, %v", line, err)
	}
	want := fmt.Sprintf("approved native__Bash calls that project rule 1 asks about, for the rest of claude's session: %d", id)
	if out := runOK(t, "approve", "--session", "--db", db, fmt.Sprint(id)); !strings.Contains(out, want) {
		t.Fatalf("approve output %q, want %q", out, want)
	}
	select {
	case out := <-done:
		if !strings.Contains(out, `"permissionDecision":"allow"`) {
			t.Fatalf("approved call answered %q", out)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the approved hook call did not return")
	}
	if out := runOK(t, "grants", "--db", db); !strings.Contains(out, "project rule 1") {
		t.Fatalf("grants:\n%s", out)
	}
	if out, _, _ := runHook(t, dir, claudeHookInput(dir, "go test ./..."), "--agent", "claude"); out != "" {
		t.Fatalf("a call the project does not match answered %q, want nothing", out)
	}
	var by []string
	for _, r := range hookReceiptRows(t, dir) {
		by = append(by, r.decidedBy)
	}
	if !slices.Equal(by, []string{fmt.Sprintf("user:%d", id), "rule:1"}) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAnInvalidProjectFileDeniesEveryHookCall(t *testing.T) {
	dir := t.TempDir()
	writeAllowConfig(t, dir)
	writeProjectFile(t, dir, "[approvals]\ntimeout = \"1s\"\n")
	out, errOut, code := runHook(t, dir, claudeHookInput(dir, "ls"), "--agent", "claude")
	if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, ".derbent.toml") ||
		!strings.Contains(out, "only [[rule]] tables are allowed") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// The MCP gate reads the project's rules too, and a project deny leaves the tool listed.
func TestAProjectRuleDecidesMCPCallsWithoutChangingTheList(t *testing.T) {
	dir := t.TempDir()
	cfg := writeAllowConfig(t, dir)
	writeProjectFile(t, dir, "[[rule]]\ntool = \"memory_write\"\naction = \"deny\"\n")
	cs := connectProcess(t, dir, "claude", cfg)
	defer cs.Close()
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(tools.Tools, func(tool *mcp.Tool) bool { return tool.Name == "memory_write" }) {
		t.Fatal("the project's deny hid memory_write")
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"title": "t", "body": "b"}})
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "not allowed in this project") {
		t.Fatalf("memory_write: err %v, result %q", err, resultText(res))
	}
}
