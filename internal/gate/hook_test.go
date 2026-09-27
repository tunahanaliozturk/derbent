package gate_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/rule"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

type hookRow struct{ tool, args, decision, decidedBy, outcome string }

func hookReceipts(t *testing.T, e *env) []hookRow {
	t.Helper()
	rows, err := e.db.QueryContext(t.Context(), `SELECT tool, args, decision, decided_by, outcome FROM receipts ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []hookRow
	for rows.Next() {
		var r hookRow
		if err := rows.Scan(&r.tool, &r.args, &r.decision, &r.decidedBy, &r.outcome); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var shellRules = []rule.Spec{
	{Tool: "native__Bash", Args: map[string]string{"command": "git push*"}, Action: rule.Ask},
	{Tool: "native__Bash", Args: map[string]string{"command": "rm -rf*"}, Action: rule.Deny},
	{Action: rule.Allow},
}

// hookAsync starts a hook call in the background; its answer arrives on the returned channel.
func hookAsync(t *testing.T, g *gate.Gate, args string) <-chan gate.HookAnswer {
	done := make(chan gate.HookAnswer, 1)
	go func() {
		ans, _ := g.Hook(t.Context(), "native__Bash", json.RawMessage(args))
		done <- ans
	}()
	return done
}

func awaitHook(t *testing.T, done <-chan gate.HookAnswer) gate.HookAnswer {
	t.Helper()
	select {
	case ans := <-done:
		return ans
	case <-time.After(10 * time.Second):
		t.Fatal("the hook call did not return")
		return gate.HookAnswer{}
	}
}

func TestHookAllowedByARuleGivesNoDecisionAndAGatedReceipt(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", shellRules...)
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"go test ./..."}`))
	if err != nil || ans != (gate.HookAnswer{Verdict: gate.NoDecision}) {
		t.Fatalf("Hook = %+v, %v", ans, err)
	}
	got := hookReceipts(t, e)
	if len(got) != 1 || got[0] != (hookRow{"native__Bash", `{"command":"go test ./..."}`, "allow", "rule:3", "gated"}) {
		t.Fatalf("receipts = %+v", got)
	}
}

func TestHookDeniedByARuleSaysWhy(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", shellRules...)
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"rm -rf /"}`))
	if err != nil || ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "native__Bash is not allowed") {
		t.Fatalf("Hook = %+v, %v", ans, err)
	}
	if got := hookReceipts(t, e); got[0].decision != "deny" || got[0].decidedBy != "rule:2" || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
}

func TestHookAskWaitsForTheUserAndASessionGrantCoversLaterHookCalls(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", shellRules...)
	done := hookAsync(t, g, `{"command":"git push origin main"}`)
	p := waitPending(t, e.approvals)
	if p.Tool != "native__Bash" || p.Session != "claude-session" {
		t.Fatalf("pending = %+v", p)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans != (gate.HookAnswer{Verdict: gate.Allowed}) {
		t.Fatalf("answer = %+v", ans)
	}
	// The grant covers the next hook call of the same tool in the same session without asking.
	if ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"git push --tags"}`)); err != nil || ans.Verdict != gate.Allowed {
		t.Fatalf("second call = %+v, %v", ans, err)
	}
	want := []string{fmt.Sprintf("user:%d", p.ID), fmt.Sprintf("grant:%d", p.ID)}
	got := hookReceipts(t, e)
	if len(got) != 2 || got[0].decidedBy != want[0] || got[1].decidedBy != want[1] || got[1].outcome != "gated" {
		t.Fatalf("receipts = %+v, want decided_by %v", got, want)
	}
}

// Built-in tools are coarse: every shell command is native__Bash. A session grant from one ask rule
// must not let through a command another ask rule holds. It follows the rule and the rules above it,
// so the same grant holds when only rules below it are reordered, and a rule edited since asks again.
func TestHookSessionGrantCoversOnlyTheRuleThatAsked(t *testing.T) {
	e := newEnv(t)
	push := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push*"}, Action: rule.Ask}
	apply := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "terraform apply*"}, Action: rule.Ask}
	rm := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "rm -rf*"}, Action: rule.Deny}
	g := e.gate(t, "claude", push, apply, rm, rule.Spec{Action: rule.Allow})
	g.ApprovalTimeout = 300 * time.Millisecond
	done := hookAsync(t, g, `{"command":"git push origin main"}`)
	p := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans.Verdict != gate.Allowed {
		t.Fatalf("answer = %+v", ans)
	}
	hook := func(g *gate.Gate, command string) gate.HookAnswer {
		t.Helper()
		ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"`+command+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		return ans
	}
	if ans := hook(g, "git push --tags"); ans.Verdict != gate.Allowed {
		t.Fatalf("next push = %+v, want it let through by the grant", ans)
	}
	if ans := hook(g, "terraform apply -auto-approve"); ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "none came within") {
		t.Fatalf("terraform apply = %+v, want it asked about and timed out", ans)
	}

	reordered := e.gate(t, "claude", push, rm, apply, rule.Spec{Action: rule.Allow})
	reordered.ApprovalTimeout = 300 * time.Millisecond
	if ans := hook(reordered, "git push"); ans.Verdict != gate.Allowed {
		t.Fatalf("push with the rules below it reordered = %+v, want the same grant to hold", ans)
	}
	edited := e.gate(t, "claude", rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push origin*"}, Action: rule.Ask},
		rule.Spec{Action: rule.Allow})
	edited.ApprovalTimeout = 300 * time.Millisecond
	if ans := hook(edited, "git push origin main"); ans.Verdict != gate.Denied {
		t.Fatalf("push under an edited rule = %+v, want it asked about again", ans)
	}

	var asked int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM approvals`).Scan(&asked); err != nil || asked != 3 {
		t.Fatalf("approvals written = %d, err %v; want 3: the push, the terraform apply and the edited rule's push", asked, err)
	}
	var by []string
	for _, r := range hookReceipts(t, e) {
		by = append(by, r.decidedBy)
	}
	grant := fmt.Sprintf("grant:%d", p.ID)
	if len(by) != 5 || by[0] != fmt.Sprintf("user:%d", p.ID) || by[1] != grant || !strings.HasPrefix(by[2], "timeout:") ||
		by[3] != grant || !strings.HasPrefix(by[4], "timeout:") {
		t.Fatalf("decided_by = %v", by)
	}
}

// Under first match, which calls a rule asks about depends on the rules above it. With "git push*--force*"
// above "git push*", A on a plain push must not let a force push through once the two rules swap, or once
// the force rule is narrowed. A grant follows its rule and every rule above it, so a change there asks
// again, and a change below it keeps the grant (ADR 0011).
func TestHookSessionGrantAsksAgainWhenARuleAboveItChanges(t *testing.T) {
	e := newEnv(t)
	force := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push*--force*"}, Action: rule.Ask}
	push := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push*"}, Action: rule.Ask}
	rm := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "rm -rf*"}, Action: rule.Deny}
	allow := rule.Spec{Action: rule.Allow}
	done := hookAsync(t, e.gate(t, "claude", force, push, rm, allow), `{"command":"git push origin main"}`)
	p := waitPending(t, e.approvals)
	if p.Rule != 2 {
		t.Fatalf("pending = %+v, want it asked by the plain push rule, 2", p)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans.Verdict != gate.Allowed {
		t.Fatalf("answer = %+v", ans)
	}
	hook := func(command string, specs ...rule.Spec) gate.HookAnswer {
		t.Helper()
		g := e.gate(t, "claude", specs...)
		g.ApprovalTimeout = 300 * time.Millisecond
		ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"`+command+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		return ans
	}
	asked := func(ans gate.HookAnswer) bool {
		return ans.Verdict == gate.Denied && strings.Contains(ans.Reason, "none came within")
	}

	below := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "rm -r*"}, Action: rule.Deny}
	if ans := hook("git push --tags", force, push, below, allow); ans.Verdict != gate.Allowed {
		t.Fatalf("push with a rule below it edited = %+v, want the grant to hold", ans)
	}
	if ans := hook("git push --force origin main", push, force, rm, allow); !asked(ans) {
		t.Fatalf("force push with the two rules swapped = %+v, want it asked about again", ans)
	}
	narrower := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push --force*"}, Action: rule.Ask}
	if ans := hook("git push origin --force", narrower, push, rm, allow); !asked(ans) {
		t.Fatalf("force push with the rule above narrowed = %+v, want it asked about again", ans)
	}

	var by []string
	for _, r := range hookReceipts(t, e) {
		by = append(by, r.decidedBy)
	}
	if len(by) != 4 || by[0] != fmt.Sprintf("user:%d", p.ID) || by[1] != fmt.Sprintf("grant:%d", p.ID) ||
		!strings.HasPrefix(by[2], "timeout:") || !strings.HasPrefix(by[3], "timeout:") {
		t.Fatalf("decided_by = %v, want user:%d, grant:%d, then two timeouts", by, p.ID, p.ID)
	}
}

// A grant is for one tool, and a hook call's tool is always a native__ name, which no MCP tool can have
// ("native" is a reserved server name). So a grant from the hook path never covers an MCP call, even
// in the same gate, agent and session: the MCP call asks the user again.
func TestASessionGrantForAHookToolDoesNotCoverAnMCPTool(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", append([]rule.Spec{askWrites}, shellRules...)...)
	cs := connect(t, g)
	done := hookAsync(t, g, `{"command":"git push"}`)
	hookAsk := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), hookAsk.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if ans := awaitHook(t, done); ans != (gate.HookAnswer{Verdict: gate.Allowed}) {
		t.Fatalf("hook answer = %+v", ans)
	}
	mcpDone := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "t", "body": "b"})
	mcpAsk := waitPending(t, e.approvals)
	if mcpAsk.ID == hookAsk.ID || mcpAsk.Tool != "memory_write" || mcpAsk.Session != hookAsk.Session {
		t.Fatalf("pending = %+v, want a new approval for memory_write in session %s", mcpAsk, hookAsk.Session)
	}
	if err := e.approvals.Decide(t.Context(), mcpAsk.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if c := awaitCall(t, mcpDone); c.err != nil || c.res.IsError {
		t.Fatalf("MCP call: %+v, %v", c.res, c.err)
	}
	want := []string{fmt.Sprintf("user:%d", hookAsk.ID), fmt.Sprintf("user:%d", mcpAsk.ID)}
	if by := decidedBy(t, e.db); !slices.Equal(by, want) {
		t.Fatalf("decided_by = %v, want %v", by, want)
	}
}

func TestHookTimeoutDenies(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", shellRules...)
	g.ApprovalTimeout = 300 * time.Millisecond
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"git push"}`))
	if err != nil || ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "none came within 300ms") {
		t.Fatalf("Hook = %+v, %v", ans, err)
	}
	if got := hookReceipts(t, e); !strings.HasPrefix(got[0].decidedBy, "timeout:") || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
}

func TestHookArgumentsThatAreNotAnObjectAreRefused(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude")
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`"git push"`))
	if err != nil || ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "not a JSON object") {
		t.Fatalf("Hook = %+v, %v", ans, err)
	}
}

func TestHookThatCannotRecordRefuses(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude")
	breakReceipts(t, e.db)
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"ls"}`))
	if err == nil || ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "could not be recorded") {
		t.Fatalf("Hook = %+v, %v; a call that cannot be recorded must not run", ans, err)
	}
}

// A hook call waiting for the user when the gate is told to stop is withdrawn, exactly as an MCP call is.
func TestStoppingTheGateWithdrawsAWaitingHookCall(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", shellRules...)
	stop, cancel := context.WithCancel(t.Context())
	defer cancel()
	g.Stop = stop
	done := hookAsync(t, g, `{"command":"git push"}`)
	p := waitPending(t, e.approvals)
	cancel()
	if ans := awaitHook(t, done); ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "withdrawn before the user decided, because the gate is stopping") {
		t.Fatalf("answer = %+v", ans)
	}
	if got := hookReceipts(t, e); len(got) != 1 || got[0].decidedBy != fmt.Sprintf("withdrawn:%d", p.ID) || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
}

// checkStoppingGateRefused reports how a hook call to a stopping gate failed to be refused by the gate
// itself, with no approval row and a receipt that says the gate decided, or returns nil when it was.
func checkStoppingGateRefused(t *testing.T, e *env, done <-chan gate.HookAnswer) error {
	t.Helper()
	ans := awaitHook(t, done)
	if want := "derbent: native__Bash was refused because the gate is stopping; it did not run"; ans != (gate.HookAnswer{Verdict: gate.Denied, Reason: want}) {
		return fmt.Errorf("answer = %+v, want Denied with %q", ans, want)
	}
	if got := hookReceipts(t, e); len(got) != 1 || got[0].decidedBy != "gate" || got[0].outcome != "refused" {
		return fmt.Errorf("receipts = %+v", got)
	}
	return nil
}

func TestAHookCallToAGateAlreadyStoppingAsksNothing(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", shellRules...)
	stop, cancel := context.WithCancel(t.Context())
	cancel()
	g.Stop = stop
	if err := checkStoppingGateRefused(t, e, hookAsync(t, g, `{"command":"git push"}`)); err != nil {
		t.Fatal(err)
	}
	if p, err := e.approvals.Pending(t.Context()); err != nil || len(p) != 0 {
		t.Fatalf("pending = %+v, %v", p, err)
	}
}

// A gate told to stop while the approval row is still being written ends the call before the row
// exists. Nothing was withdrawn, so the agent is told what the receipt says: the gate refused it.
func TestAGateThatStopsBeforeTheApprovalIsWrittenRefusesTheCall(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude", shellRules...)
	// The approvals live in a database of their own here, so a write lock on it holds the approval row
	// back without holding back the receipt. The queue's handle gives up on the lock after a second,
	// because SQLite does not cut a busy wait short when the wait's context ends.
	path := filepath.Join(t.TempDir(), "approvals.db")
	adb, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adb.Close() })
	qdb, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { qdb.Close() })
	g.Approvals = approval.NewQueue(qdb)
	lock, err := adb.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = lock.ExecContext(context.WithoutCancel(t.Context()), `ROLLBACK`) // the lock ends with the test either way
		lock.Close()
	})
	stop, cancel := context.WithCancel(t.Context())
	defer cancel()
	g.Stop = stop
	done := hookAsync(t, g, `{"command":"git push"}`)
	// A fixed pause for the call to reach the locked write. A call slower than that meets a gate
	// already stopping instead, which must end the same way, so the pause cannot make the test pass
	// wrongly, only test the earlier check twice.
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := checkStoppingGateRefused(t, e, done); err != nil {
		t.Fatal(err)
	}
	var asked int
	if err := adb.QueryRowContext(t.Context(), `SELECT count(*) FROM approvals`).Scan(&asked); err != nil || asked != 0 {
		t.Fatalf("approvals written = %d, err %v; want none", asked, err)
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"gate"}) {
		t.Fatalf("decided_by = %v, want [gate]", by)
	}
}
