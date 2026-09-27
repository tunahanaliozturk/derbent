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
