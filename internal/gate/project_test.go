package gate_test

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

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

const askGitStatus = "[[rule]]\ntool = \"native__Bash\"\nargs = { command = \"git status*\" }\naction = \"ask\"\n"

func writeProjectRules(t *testing.T, dir, rules string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.ProjectRulesFile), []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
}

// projectDir returns a new project directory, with rules in its .derbent.toml unless rules is empty.
func projectDir(t *testing.T, rules string) string {
	t.Helper()
	dir := t.TempDir()
	if rules != "" {
		writeProjectRules(t, dir, rules)
	}
	return dir
}

// projectGate is a gate for claude under the user's rules specs and the project rules in dir.
func projectGate(t *testing.T, e *env, dir string, specs ...rule.Spec) *gate.Gate {
	t.Helper()
	g := e.gate(t, "claude", specs...)
	g.ProjectRules = config.NewProjectRules(dir)
	return g
}

func shell(t *testing.T, g *gate.Gate, command string) gate.HookAnswer {
	t.Helper()
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"`+command+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return ans
}

func hookDecidedBy(t *testing.T, e *env) []string {
	t.Helper()
	var by []string
	for _, r := range hookReceipts(t, e) {
		by = append(by, r.decidedBy)
	}
	return by
}

// Once the user approves a call that only the project asked about, the hook answers as the user's rules
// alone would, with no decision, so the CLI's own permission settings still apply: an explicit allow
// would skip them, which is looser than the user's rules without the project.
func TestAProjectRuleCanMakeACallAsk(t *testing.T) {
	e := newEnv(t)
	g := projectGate(t, e, projectDir(t, askGitStatus))
	if ans := shell(t, g, "go test ./..."); ans.Verdict != gate.NoDecision {
		t.Fatalf("a call the project does not match = %+v", ans)
	}
	done := hookAsync(t, g, `{"command":"git status"}`)
	p := waitPending(t, e.approvals)
	if p.Rule != 1 || !p.ProjectRule {
		t.Fatalf("pending = %+v, want project rule 1", p)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans.Verdict != gate.NoDecision {
		t.Fatalf("answer = %+v, want no decision, as the user's allow rule gives", ans)
	}
	if by := hookDecidedBy(t, e); !slices.Equal(by, []string{"rule:1", fmt.Sprintf("user:%d", p.ID)}) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAProjectRuleCanDenyACallTheUsersRulesAllow(t *testing.T) {
	e := newEnv(t)
	g := projectGate(t, e, projectDir(t, "[[rule]]\ntool = \"native__Bash\"\nargs = { command = \"rm *\" }\naction = \"deny\"\n"))
	ans := shell(t, g, "rm -rf build")
	if ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "native__Bash is not allowed in this project (rule 1 of .derbent.toml)") {
		t.Fatalf("answer = %+v", ans)
	}
	if by := hookDecidedBy(t, e); !slices.Equal(by, []string{"project:1"}) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAProjectRuleNeverLoosens(t *testing.T) {
	e := newEnv(t)
	g := projectGate(t, e, projectDir(t, "[[rule]]\ntool = \"native__Bash\"\naction = \"allow\"\n"), shellRules...)
	if ans := shell(t, g, "rm -rf /"); ans.Verdict != gate.Denied {
		t.Fatalf("rm under a project allow = %+v, want the user's deny", ans)
	}
	done := hookAsync(t, g, `{"command":"git push"}`)
	p := waitPending(t, e.approvals)
	if p.Rule != 1 || p.ProjectRule {
		t.Fatalf("pending = %+v, want the user's rule 1", p)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	awaitHook(t, done)
	if by := hookDecidedBy(t, e); by[0] != "rule:2" {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestOnATieTheUsersRuleIsNamed(t *testing.T) {
	e := newEnv(t)
	g := projectGate(t, e, projectDir(t, "[[rule]]\ntool = \"native__Bash\"\naction = \"ask\"\n"), shellRules...)
	for _, tc := range []struct {
		command string
		rule    int
		project bool
	}{{"git push", 1, false}, {"go test ./...", 1, true}} {
		done := hookAsync(t, g, `{"command":"`+tc.command+`"}`)
		p := waitPending(t, e.approvals)
		if p.Rule != tc.rule || p.ProjectRule != tc.project {
			t.Fatalf("%s: pending = %+v, want rule %d, project %v", tc.command, p, tc.rule, tc.project)
		}
		if err := e.approvals.Decide(t.Context(), p.ID, approval.Deny); err != nil {
			t.Fatal(err)
		}
		awaitHook(t, done)
	}
}

func TestAnInvalidProjectFileDeniesEveryCall(t *testing.T) {
	e := newEnv(t)
	dir := projectDir(t, "[servers.x]\ncommand = [\"x\"]\n")
	g := projectGate(t, e, dir)
	ans := shell(t, g, "ls")
	if ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, filepath.Join(dir, config.ProjectRulesFile)) ||
		!strings.Contains(ans.Reason, "only [[rule]] tables are allowed") {
		t.Fatalf("answer = %+v", ans)
	}
	res := call(t, connect(t, g), "memory_search", map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(text(res), config.ProjectRulesFile) {
		t.Fatalf("MCP call = %q", text(res))
	}
	if by := hookDecidedBy(t, e); !slices.Equal(by, []string{"gate", "gate"}) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAMissingProjectFileChangesNothing(t *testing.T) {
	e := newEnv(t)
	if ans := shell(t, projectGate(t, e, projectDir(t, ""), shellRules...), "go test ./..."); ans.Verdict != gate.NoDecision {
		t.Fatalf("answer = %+v", ans)
	}
	if by := hookDecidedBy(t, e); !slices.Equal(by, []string{"rule:3"}) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAnEditToTheProjectFileTakesEffectOnTheNextCall(t *testing.T) {
	e := newEnv(t)
	dir := projectDir(t, "[[rule]]\ntool = \"native__Bash\"\naction = \"deny\"\n")
	g := projectGate(t, e, dir)
	if ans := shell(t, g, "ls"); ans.Verdict != gate.Denied {
		t.Fatalf("before the edit = %+v", ans)
	}
	writeProjectRules(t, dir, "[[rule]]\ntool = \"native__Write\"\naction = \"deny\"\n") // another size
	if ans := shell(t, g, "ls"); ans.Verdict != gate.NoDecision {
		t.Fatalf("after the edit = %+v", ans)
	}
}

// A grant for a call a project rule asked about is keyed on the user's rules up to the one that decided
// and the project's up to the one that asked, so an edit at or above either asks again, and undoing it
// makes the grant apply again.
func TestAProjectGrantIsKeyedOnBothRuleLists(t *testing.T) {
	e := newEnv(t)
	dir := projectDir(t, askGitStatus)
	allow := rule.Spec{Action: rule.Allow}
	done := hookAsync(t, projectGate(t, e, dir, allow), `{"command":"git status"}`)
	p := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans.Verdict != gate.NoDecision {
		t.Fatalf("first call = %+v", ans)
	}
	hook := func(command string, specs ...rule.Spec) gate.HookAnswer {
		t.Helper()
		g := projectGate(t, e, dir, specs...)
		g.ApprovalTimeout = 300 * time.Millisecond
		return shell(t, g, command)
	}
	asked := func(ans gate.HookAnswer) bool {
		return ans.Verdict == gate.Denied && strings.Contains(ans.Reason, "none came within")
	}
	if ans := hook("git status --short", allow); ans.Verdict != gate.NoDecision {
		t.Fatalf("under the same rules = %+v, want the grant to hold", ans)
	}
	writeProjectRules(t, dir, "[[rule]]\ntool = \"native__Read\"\naction = \"ask\"\n\n"+askGitStatus)
	if ans := hook("git status", allow); !asked(ans) {
		t.Fatalf("with a project rule added above = %+v, want it asked about again", ans)
	}
	writeProjectRules(t, dir, askGitStatus)
	if ans := hook("git status", allow); ans.Verdict != gate.NoDecision {
		t.Fatalf("with the project edit undone = %+v, want the grant again", ans)
	}
	if ans := hook("git status", rule.Spec{Tool: "native__Read", Action: rule.Ask}, allow); !asked(ans) {
		t.Fatalf("with a user rule added above = %+v, want it asked about again", ans)
	}
	if by := hookDecidedBy(t, e); by[0] != fmt.Sprintf("user:%d", p.ID) || by[1] != fmt.Sprintf("grant:%d", p.ID) {
		t.Fatalf("decided_by = %v, want the approval and the grant named", by)
	}
}

// When the user's own rules ask too, an approval answers allow, as it does without a project file.
func TestAnApprovalTheUsersRulesAskedForAnswersAllow(t *testing.T) {
	e := newEnv(t)
	g := projectGate(t, e, projectDir(t, askGitStatus), rule.Spec{Tool: "native__Bash", Action: rule.Ask}, rule.Spec{Action: rule.Allow})
	done := hookAsync(t, g, `{"command":"git status"}`)
	p := waitPending(t, e.approvals)
	if p.ProjectRule {
		t.Fatalf("pending = %+v, want the user's rule named on the tie", p)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans.Verdict != gate.Allowed {
		t.Fatalf("answer = %+v, want allow", ans)
	}
}

// Project rules decide calls only: a tool the project denies stays listed, and its calls are refused.
func TestProjectRulesDoNotChangeTheToolList(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, projectGate(t, e, projectDir(t, "[[rule]]\ntool = \"memory_write\"\naction = \"deny\"\n")))
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Tools, func(tool *mcp.Tool) bool { return tool.Name == "memory_write" }) {
		t.Fatal("the project's deny hid memory_write")
	}
	if got := call(t, cs, "memory_write", map[string]any{"title": "t", "body": "b"}); !got.IsError || !strings.Contains(text(got), "not allowed in this project") {
		t.Fatalf("call = %q", text(got))
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"project:1"}) {
		t.Fatalf("decided_by = %v", by)
	}
}
