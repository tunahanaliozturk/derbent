package gate_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

func budgets(t *testing.T, specs ...rule.BudgetSpec) rule.Budgets {
	t.Helper()
	b, err := rule.CompileBudgets(specs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// passedBefore appends a receipt for a call agent had let through at the given time.
func passedBefore(t *testing.T, e *env, agent, tool string, at time.Time) {
	t.Helper()
	if _, err := e.receipts.Append(t.Context(), receipt.Receipt{
		At: at, Project: "/work/shop", Agent: agent, Session: "earlier", Tool: tool, Args: "{}",
		Decision: "allow", DecidedBy: "rule:1", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestABudgetRefusesTheCallAfterItsLimit(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude")
	g.Budgets = budgets(t, rule.BudgetSpec{Agent: "*", Tool: "memory_search", Calls: 2, Per: "1h"})
	cs := connect(t, g)
	for range 2 {
		if res := call(t, cs, "memory_search", map[string]any{"query": "x"}); res.IsError {
			t.Fatalf("a call under the budget: %s", text(res))
		}
	}
	res := call(t, cs, "memory_search", map[string]any{"query": "x"})
	want := "derbent: budget 1 in the config is used up: 2 calls to memory_search per 1h for claude; the next call is possible in about "
	if !res.IsError || !strings.HasPrefix(text(res), want) {
		t.Fatalf("third call = %q, want it to start %q", text(res), want)
	}
	if res = call(t, cs, "memory_write", map[string]any{"title": "t", "body": "b"}); res.IsError {
		t.Fatalf("a tool outside the budget: %s", text(res))
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"rule:1", "rule:1", "budget:1", "rule:1"}) {
		t.Fatalf("decided_by = %v", by)
	}
}

// The count reads the receipts, so calls from another gate or session of the same agent count, and
// another agent's calls do not.
func TestABudgetCountsTheAgentsCallsFromEveryGate(t *testing.T) {
	e := newEnv(t)
	limit := budgets(t, rule.BudgetSpec{Tool: "native__Bash", Calls: 2, Per: "1h"})
	passedBefore(t, e, "claude", "native__Bash", time.Now().Add(-time.Minute))
	passedBefore(t, e, "codex", "native__Bash", time.Now().Add(-time.Minute))
	passedBefore(t, e, "claude", "native__Bash", time.Now().Add(-2*time.Hour)) // outside the window
	g := e.gate(t, "claude")
	g.Budgets = limit
	if ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"ls"}`)); err != nil || ans.Verdict != gate.NoDecision {
		t.Fatalf("second call in the window = %+v, %v", ans, err)
	}
	ans, err := g.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"ls"}`))
	if err != nil || ans.Verdict != gate.Denied || !strings.Contains(ans.Reason, "budget 1 in the config is used up: 2 calls to native__Bash per 1h for claude") {
		t.Fatalf("third call = %+v, %v", ans, err)
	}
	if got := hookReceipts(t, e); got[len(got)-1].decidedBy != "budget:1" || got[len(got)-1].outcome != "refused" {
		t.Fatalf("last receipt = %+v", got[len(got)-1])
	}
	other := e.gate(t, "codex")
	other.Budgets = limit
	if ans, err = other.Hook(t.Context(), "native__Bash", json.RawMessage(`{"command":"ls"}`)); err != nil || ans.Verdict != gate.NoDecision {
		t.Fatalf("codex's second call = %+v, %v; claude's calls must not count for codex", ans, err)
	}
}

// A used-up budget refuses without asking, so an agent stuck in a loop never fills the user's queue.
// A call the user approved counts like any call let through.
func TestABudgetRefusesAnAskedCallWithoutAsking(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", askWrites, allowRest)
	g.Budgets = budgets(t, rule.BudgetSpec{Tool: "memory_write", Calls: 1, Per: "1h"})
	cs := connect(t, g)
	done := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "one", "body": "b"})
	p := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if c := awaitCall(t, done); c.err != nil || c.res.IsError {
		t.Fatalf("first call: %+v, %v", c.res, c.err)
	}
	if res := call(t, cs, "memory_write", map[string]any{"title": "two", "body": "b"}); !res.IsError || !strings.Contains(text(res), "budget 1 in the config is used up") {
		t.Fatalf("second call = %q, want it refused by the budget", text(res))
	}
	var asked int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM approvals`).Scan(&asked); err != nil || asked != 1 {
		t.Fatalf("approvals written = %d, err %v; the refused call must not have asked", asked, err)
	}
}

// A budget that cannot be counted refuses the call rather than let it through uncounted.
func TestABudgetThatCannotBeCountedRefuses(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.ExecContext(t.Context(), `INSERT INTO receipts (seq, at, project, agent, session, tool, args,
		args_sha256, decision, decided_by, outcome, result_size, result_sha256, duration_ms, prev_hash, hash)
		VALUES (1, 'not a time', 'p', 'claude', 's', 'memory_search', '{}', '', 'allow', 'rule:1', 'ok', 0, '', 0, '', 'h')`); err != nil {
		t.Fatal(err)
	}
	g := e.gate(t, "claude")
	g.Budgets = budgets(t, rule.BudgetSpec{Tool: "memory_search", Calls: 5, Per: "1h"})
	res := call(t, connect(t, g), "memory_search", map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(text(res), "memory_search was refused because its budget could not be counted") {
		t.Fatalf("call = %q, want it refused", text(res))
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"rule:1", "gate"}) {
		t.Fatalf("decided_by = %v, want the gate named", by)
	}
}

func TestADenyStaysADenyUnderABudget(t *testing.T) {
	e := newEnv(t)
	passedBefore(t, e, "claude", "memory_write", time.Now().Add(-time.Minute))
	g := e.gate(t, "claude", rule.Spec{Tool: "memory_write", Action: rule.Deny}, allowRest)
	g.Budgets = budgets(t, rule.BudgetSpec{Calls: 1, Per: "1h"})
	res := call(t, connect(t, g), "memory_write", map[string]any{"title": "t", "body": "b"})
	if !res.IsError || strings.Contains(text(res), "budget") {
		t.Fatalf("call = %q, want the rule's refusal", text(res))
	}
	// The earlier call's receipt, then the deny.
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"rule:1", "rule:1"}) {
		t.Fatalf("decided_by = %v, want the deny rule named", by)
	}
}
