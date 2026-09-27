package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/config"
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

// gitIn runs git in dir, skipping the test where git is not installed.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	cmd := exec.CommandContext(t.Context(), git, append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("git %v: %v\n%s", args, runErr, out)
	}
}

// newRepo creates a git repository with one commit at base/name and returns its path.
func newRepo(t *testing.T, base, name string) string {
	t.Helper()
	repo := filepath.Join(base, name)
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "start")
	return repo
}

// A linked worktree reads the .derbent.toml at its own root, so a branch that adds or tightens the file
// is enforced there, while its calls stay filed under the main checkout's project.
func TestALinkedWorktreeReadsItsOwnProjectFile(t *testing.T) {
	base := t.TempDir()
	repo := newRepo(t, base, "shop")
	wt := filepath.Join(base, "shop-feature")
	gitIn(t, repo, "worktree", "add", "-q", wt)
	cfg := "[approvals]\ntimeout = \"1s\"\n\n[[rule]]\naction = \"allow\"\n"
	if err := os.WriteFile(filepath.Join(base, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, wt, "[[rule]]\ntool = \"native__Bash\"\naction = \"ask\"\n")
	out, errOut, _ := runHook(t, base, claudeHookInput(wt, "ls"), "--agent", "claude")
	if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "none came within 1s") {
		t.Fatalf("stdout %q, stderr %q; want the call asked about and timed out", out, errOut)
	}
	key, err := config.ProjectKey(repo)
	if err != nil {
		t.Fatal(err)
	}
	rows := hookReceiptRows(t, base)
	if len(rows) != 1 || !strings.HasPrefix(rows[0].decidedBy, "timeout:") || rows[0].project != key {
		t.Fatalf("receipts %+v, want one timeout filed under %s", rows, key)
	}
}

// A worktree of a bare repository has no main checkout with files in it, so its own file is the only one
// there is.
func TestAWorktreeOfABareRepositoryReadsItsProjectFile(t *testing.T) {
	base := t.TempDir()
	src := newRepo(t, base, "src")
	gitIn(t, base, "clone", "-q", "--bare", src, "shop.git")
	wt := filepath.Join(base, "wt")
	gitIn(t, filepath.Join(base, "shop.git"), "worktree", "add", "-q", wt)
	writeAllowConfig(t, base)
	writeProjectFile(t, wt, "[[rule]]\ntool = \"native__Bash\"\naction = \"deny\"\n")
	out, errOut, _ := runHook(t, base, claudeHookInput(wt, "ls"), "--agent", "claude")
	if !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Fatalf("stdout %q, stderr %q; want the project's deny", out, errOut)
	}
	if rows := hookReceiptRows(t, base); len(rows) != 1 || rows[0].decidedBy != "project:1" {
		t.Fatalf("receipts %+v, want one decided by project:1", rows)
	}
}
