package gate_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/redact"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

var (
	askWrites = rule.Spec{Tool: "memory_write", Action: rule.Ask}
	allowRest = rule.Spec{Action: rule.Allow}
)

type called struct {
	res *mcp.CallToolResult
	err error
}

// callAsync starts a tool call in the background; its result arrives on the returned channel.
func callAsync(ctx context.Context, cs *mcp.ClientSession, tool string, args map[string]any) <-chan called {
	done := make(chan called, 1)
	go func() {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		done <- called{res, err}
	}()
	return done
}

func awaitCall(t *testing.T, done <-chan called) called {
	t.Helper()
	select {
	case c := <-done:
		return c
	case <-time.After(15 * time.Second):
		t.Fatal("the call did not return")
		return called{}
	}
}

func waitPending(t *testing.T, q *approval.Queue) approval.Pending {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		p, err := q.Pending(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(p) > 0 {
			return p[0]
		}
	}
	t.Fatal("no approval became pending")
	return approval.Pending{}
}

func decidedBy(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT decided_by FROM receipts ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAskedCallRunsOnceApproved(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", askWrites, allowRest)
	red, err := redact.New([]string{`s3cret-[a-z]+`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Redact = red.JSON
	cs := connect(t, g)
	done := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "deploy", "body": "token s3cret-abc"})
	p := waitPending(t, e.approvals)
	if p.Agent != "codex" || p.Session != "codex-session" || p.Tool != "memory_write" || p.Rule != 1 || p.Project != "/work/shop" {
		t.Fatalf("pending = %+v", p)
	}
	if strings.Contains(p.Args, "s3cret-abc") || !strings.Contains(p.Args, redact.Mask) {
		t.Fatalf("pending args = %s, want the secret masked before it is shown to anyone", p.Args)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	c := awaitCall(t, done)
	if c.err != nil {
		t.Fatal(c.err)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	decode(t, c.res, &out)
	got := receipts(t, e.db)
	if len(got) != 1 || got[0].decision != "allow" || got[0].outcome != "ok" {
		t.Fatalf("receipts = %+v", got)
	}
	if by := decidedBy(t, e.db); by[0] != fmt.Sprintf("user:%d", p.ID) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAskedCallDeniedByTheUserDoesNotRun(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "codex", askWrites, allowRest))
	done := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "deploy", "body": "b"})
	p := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), p.ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	c := awaitCall(t, done)
	if c.err != nil || !c.res.IsError || !strings.Contains(text(c.res), "the user denied memory_write") {
		t.Fatalf("result = %+v, err %v", c.res, c.err)
	}
	if hits, err := e.memory.Search(t.Context(), "/work/shop", "deploy", 10, false); err != nil || len(hits) != 0 {
		t.Fatalf("a denied write was stored: %+v, %v", hits, err)
	}
	got := receipts(t, e.db)
	if len(got) != 1 || got[0].decision != "deny" || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
	if by := decidedBy(t, e.db); by[0] != fmt.Sprintf("user:%d", p.ID) {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestAskedCallWithoutAnAnswerTimesOut(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", askWrites, allowRest)
	g.ApprovalTimeout = 300 * time.Millisecond
	cs := connect(t, g)
	res := call(t, cs, "memory_write", map[string]any{"title": "t", "body": "b"})
	if !res.IsError || !strings.Contains(text(res), "none came within 300ms") {
		t.Fatalf("result = %s", text(res))
	}
	by := decidedBy(t, e.db)
	if len(by) != 1 || !strings.HasPrefix(by[0], "timeout:") {
		t.Fatalf("decided_by = %v", by)
	}
}

func TestApprovalForTheSessionCoversLaterCallsOfThatSessionOnly(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "codex", askWrites, allowRest))
	done := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "one", "body": "b"})
	p := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if c := awaitCall(t, done); c.err != nil || c.res.IsError {
		t.Fatalf("first call: %+v, %v", c.res, c.err)
	}
	if res := call(t, cs, "memory_write", map[string]any{"title": "two", "body": "b"}); res.IsError {
		t.Fatalf("second call: %s", text(res))
	}
	var asked int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM approvals`).Scan(&asked); err != nil || asked != 1 {
		t.Fatalf("approvals written = %d, err %v; the second call should not have asked", asked, err)
	}
	want := []string{fmt.Sprintf("user:%d", p.ID), fmt.Sprintf("grant:%d", p.ID)}
	if by := decidedBy(t, e.db); !slices.Equal(by, want) {
		t.Fatalf("decided_by = %v, want %v", by, want)
	}

	other := e.gate(t, "codex", askWrites, allowRest)
	other.Session = "codex-session-2"
	other.ApprovalTimeout = 300 * time.Millisecond
	if res := call(t, connect(t, other), "memory_write", map[string]any{"title": "three", "body": "b"}); !res.IsError {
		t.Fatal("a session grant let another session of the same agent through")
	}
}

func TestAgentGivingUpLeavesAReceiptThatTheCallDidNotRun(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "codex", askWrites, allowRest))
	ctx, cancel := context.WithCancel(t.Context())
	done := callAsync(ctx, cs, "memory_write", map[string]any{"title": "t", "body": "b"})
	p := waitPending(t, e.approvals)
	cancel()
	awaitCall(t, done)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if by := decidedBy(t, e.db); len(by) == 1 {
			if by[0] != fmt.Sprintf("withdrawn:%d", p.ID) {
				t.Fatalf("decided_by = %v", by)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no receipt for the abandoned call")
		}
	}
	if got := receipts(t, e.db); got[0].decision != "deny" || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
	if pending, err := e.approvals.Pending(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v; the abandoned call is still offered", pending, err)
	}
}

// Approving once covers that call only: the next call of the same tool asks again. The Claude Code
// hook reuses this path.
func TestApprovingOnceDoesNotCoverTheNextCall(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "codex", askWrites, allowRest))
	done := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "one", "body": "b"})
	first := waitPending(t, e.approvals)
	if err := e.approvals.Decide(t.Context(), first.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if c := awaitCall(t, done); c.err != nil || c.res.IsError {
		t.Fatalf("first call: %+v, %v", c.res, c.err)
	}
	done = callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "two", "body": "b"})
	second := waitPending(t, e.approvals)
	if second.ID == first.ID {
		t.Fatalf("the second call reused approval %d", first.ID)
	}
	if err := e.approvals.Decide(t.Context(), second.ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	if c := awaitCall(t, done); c.err != nil || !c.res.IsError || !strings.Contains(text(c.res), "the user denied") {
		t.Fatalf("second call: %+v, %v", c.res, c.err)
	}
	want := []string{fmt.Sprintf("user:%d", first.ID), fmt.Sprintf("user:%d", second.ID)}
	if by := decidedBy(t, e.db); !slices.Equal(by, want) {
		t.Fatalf("decided_by = %v, want %v", by, want)
	}
}

// An agent cannot fill the user's queue with calls that would fail anyway: a name the gate does not
// serve is refused before any rule is read, even under a rule that asks about everything.
func TestACallToAToolTheGateDoesNotServeIsNeverAsked(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", rule.Spec{Action: rule.Ask})
	g.ApprovalTimeout = time.Second
	res := call(t, connect(t, g), "no_such_tool", map[string]any{"x": "y"})
	if want := "derbent: no_such_tool is not a tool this gate serves"; !res.IsError || text(res) != want {
		t.Fatalf("result = %q, IsError %v; want %q", text(res), res.IsError, want)
	}
	var asked int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM approvals`).Scan(&asked); err != nil || asked != 0 {
		t.Fatalf("approvals written = %d, err %v; want none", asked, err)
	}
	if got := receipts(t, e.db); len(got) != 1 || got[0].decision != "deny" || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"gate"}) {
		t.Fatalf("decided_by = %v, want [gate]", by)
	}
}

// A gate told to stop withdraws the calls still waiting, so none of them can be approved while it
// shuts down.
func TestStoppingTheGateWithdrawsWaitingCalls(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", askWrites, allowRest)
	stop, cancel := context.WithCancel(t.Context())
	defer cancel()
	g.Stop = stop
	cs := connect(t, g)
	done := callAsync(t.Context(), cs, "memory_write", map[string]any{"title": "t", "body": "b"})
	p := waitPending(t, e.approvals)
	cancel()
	c := awaitCall(t, done)
	if c.err != nil || !c.res.IsError || !strings.Contains(text(c.res), "the gate is stopping") {
		t.Fatalf("result = %+v, err %v", c.res, c.err)
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{fmt.Sprintf("withdrawn:%d", p.ID)}) {
		t.Fatalf("decided_by = %v", by)
	}
	if err := e.approvals.Decide(t.Context(), p.ID, approval.ApproveOnce); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("approving a withdrawn call: err = %v, want ErrNotPending", err)
	}
}

// A call that reaches a gate already told to stop never waits: the gate refuses it without writing an
// approval row, and its receipt says the gate decided, not that a call was withdrawn.
func TestAGateAlreadyStoppingAsksNothing(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", askWrites, allowRest)
	stop, cancel := context.WithCancel(t.Context())
	cancel()
	g.Stop = stop
	res := call(t, connect(t, g), "memory_write", map[string]any{"title": "t", "body": "b"})
	if !res.IsError || !strings.Contains(text(res), "the gate is stopping") {
		t.Fatalf("result = %s", text(res))
	}
	var asked int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM approvals`).Scan(&asked); err != nil || asked != 0 {
		t.Fatalf("approvals written = %d, err %v; want none", asked, err)
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"gate"}) {
		t.Fatalf("decided_by = %v, want [gate]", by)
	}
	if got := receipts(t, e.db); got[0].decision != "deny" || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
}

func TestAskedToolIsListed(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "codex", askWrites, rule.Spec{Action: rule.Deny}))
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"memory_write"}) {
		t.Fatalf("tools = %v, want only memory_write", names)
	}
}

func TestGateWithoutAQueueRefusesAskedCalls(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "codex", askWrites, allowRest)
	g.Approvals = nil
	res := call(t, connect(t, g), "memory_write", map[string]any{"title": "t", "body": "b"})
	if !res.IsError || !strings.Contains(text(res), "cannot ask") {
		t.Fatalf("result = %s", text(res))
	}
	if by := decidedBy(t, e.db); len(by) != 1 || by[0] != "rule:1" {
		t.Fatalf("decided_by = %v", by)
	}
}
