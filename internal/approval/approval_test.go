package approval_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func open(t *testing.T) (*approval.Queue, *sql.DB) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return approval.NewQueue(db), db
}

var req = approval.Request{
	Project: "/work/shop", Agent: "codex", Session: "s1", Tool: "github__create_issue", Args: `{"title":"x"}`, Rule: 3,
	RuleKey: "key-of-rule-3",
}

type asked struct {
	out approval.Outcome
	err error
}

// ask runs q.Ask in the background; its result arrives on the returned channel.
func ask(ctx context.Context, q *approval.Queue, r approval.Request, timeout time.Duration) <-chan asked {
	done := make(chan asked, 1)
	go func() {
		out, err := q.Ask(ctx, r, timeout)
		done <- asked{out, err}
	}()
	return done
}

func result(t *testing.T, done <-chan asked) asked {
	t.Helper()
	select {
	case a := <-done:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("Ask did not return")
		return asked{}
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

func noneLeft(t *testing.T, q *approval.Queue) {
	t.Helper()
	p, err := q.Pending(t.Context())
	if err != nil || len(p) != 0 {
		t.Fatalf("pending = %+v, err %v; want none", p, err)
	}
}

func TestApproveOnce(t *testing.T) {
	q, _ := open(t)
	done := ask(t.Context(), q, req, 10*time.Second)
	p := waitPending(t, q)
	if p.Request != req {
		t.Fatalf("pending request = %+v, want %+v", p.Request, req)
	}
	if got := p.Deadline.Sub(p.Created); got != 10*time.Second {
		t.Fatalf("deadline is %s after creation, want 10s", got)
	}
	if err := q.Decide(t.Context(), p.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	a := result(t, done)
	if a.err != nil || a.out != (approval.Outcome{ID: p.ID, Approved: true, By: approval.ByUser}) {
		t.Fatalf("Ask = %+v, %v", a.out, a.err)
	}
	if _, ok, err := q.Granted(t.Context(), req.Agent, req.Session, req.Tool, req.RuleKey); err != nil || ok {
		t.Fatalf("a one-off approval left a grant (ok %v, err %v)", ok, err)
	}
	noneLeft(t, q)
}

func TestDeny(t *testing.T) {
	q, _ := open(t)
	done := ask(t.Context(), q, req, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	if a := result(t, done); a.err != nil || a.out != (approval.Outcome{ID: p.ID, By: approval.ByUser}) {
		t.Fatalf("Ask = %+v, %v", a.out, a.err)
	}
	if _, ok, err := q.Granted(t.Context(), req.Agent, req.Session, req.Tool, req.RuleKey); err != nil || ok {
		t.Fatalf("a denial left a grant (ok %v, err %v)", ok, err)
	}
	noneLeft(t, q)
}

func TestApproveForTheSessionGrantsOnlyThatAgentSessionToolAndRule(t *testing.T) {
	q, _ := open(t)
	done := ask(t.Context(), q, req, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if a := result(t, done); !a.out.Approved {
		t.Fatalf("Ask = %+v, %v", a.out, a.err)
	}
	id, ok, err := q.Granted(t.Context(), "codex", "s1", "github__create_issue", "key-of-rule-3")
	if err != nil || !ok || id != p.ID {
		t.Fatalf("Granted = %d, %v, %v; want %d, true", id, ok, err, p.ID)
	}
	for _, other := range [][4]string{
		{"claude", "s1", "github__create_issue", "key-of-rule-3"},
		{"codex", "s2", "github__create_issue", "key-of-rule-3"},
		{"codex", "s1", "github__create_repo", "key-of-rule-3"},
		{"codex", "s1", "github__create_issue", "key-of-rule-4"},
		{"codex", "s1", "github__create_issue", ""},
	} {
		if _, ok, err := q.Granted(t.Context(), other[0], other[1], other[2], other[3]); err != nil || ok {
			t.Errorf("Granted(%v) = %v, %v; the grant reaches beyond its agent, session, tool and rule", other, ok, err)
		}
	}
}

// A call that no rule key names, such as one asked before grants followed the rule, can be approved
// for the session, but that approval covers nothing later.
func TestAnEmptyRuleKeyNeverMatchesAGrant(t *testing.T) {
	q, db := open(t)
	keyless := req
	keyless.RuleKey = ""
	done := ask(t.Context(), q, keyless, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if a := result(t, done); !a.out.Approved {
		t.Fatalf("Ask = %+v, %v", a.out, a.err)
	}
	if _, ok, err := q.Granted(t.Context(), keyless.Agent, keyless.Session, keyless.Tool, ""); err != nil || ok {
		t.Fatalf("Granted with no rule key = %v, %v; want no grant", ok, err)
	}
	// Even a grant row with an empty key, which Decide never writes, matches nothing.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO grants (agent, session, tool, rule_key, approval_id)
		VALUES (?, ?, ?, '', ?)`, keyless.Agent, keyless.Session, keyless.Tool, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.Granted(t.Context(), keyless.Agent, keyless.Session, keyless.Tool, ""); err != nil || ok {
		t.Fatalf("Granted with no rule key = %v, %v; want no grant", ok, err)
	}
}

func TestNoDecisionTimesOut(t *testing.T) {
	q, _ := open(t)
	a := result(t, ask(t.Context(), q, req, 300*time.Millisecond))
	if a.err != nil || a.out.Approved || a.out.By != approval.ByTimeout {
		t.Fatalf("Ask = %+v, %v; want a timeout", a.out, a.err)
	}
	if err := q.Decide(t.Context(), a.out.ID, approval.ApproveOnce); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("approving an expired call: err = %v, want ErrNotPending", err)
	}
	noneLeft(t, q)
}

// The deadline is fixed before the approval is written, and the wait ends there even when the write
// was slow, so a busy database cannot stretch a call's wait past the deadline the user is shown.
func TestASlowWriteDoesNotStretchTheWait(t *testing.T) {
	q, db := open(t)
	locked, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- store.Immediate(t.Context(), db, func(context.Context, *sql.Conn) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	start := time.Now()
	done := ask(t.Context(), q, req, time.Second)
	time.Sleep(800 * time.Millisecond) // the write waits for the lock all this time
	close(release)
	a := result(t, done)
	took := time.Since(start)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if a.err != nil || a.out.By != approval.ByTimeout {
		t.Fatalf("Ask = %+v, %v; want a timeout", a.out, a.err)
	}
	if took > 1500*time.Millisecond {
		t.Fatalf("Ask returned %s after it began, well past its one-second deadline", took)
	}
}

func TestAgentGivingUpWithdrawsTheApproval(t *testing.T) {
	q, _ := open(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := ask(ctx, q, req, 10*time.Second)
	p := waitPending(t, q)
	cancel()
	if a := result(t, done); !errors.Is(a.err, context.Canceled) {
		t.Fatalf("Ask err = %v, want context.Canceled", a.err)
	}
	noneLeft(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.ApproveOnce); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("approving a withdrawn call: err = %v, want ErrNotPending", err)
	}
}

func TestDecideRefusesWhatIsNotPending(t *testing.T) {
	q, _ := open(t)
	if err := q.Decide(t.Context(), 999, approval.ApproveOnce); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("unknown id: err = %v, want ErrNotPending", err)
	}
	done := ask(t.Context(), q, req, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	if err := q.Decide(t.Context(), p.ID, approval.ApproveOnce); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("second decision: err = %v, want ErrNotPending", err)
	}
	result(t, done)
	if err := q.Decide(t.Context(), p.ID, "maybe"); err == nil || errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("unknown verdict: err = %v", err)
	}
}

// A gate that stopped while its call waited leaves the row marked pending. Past its deadline it must
// not be offered or approvable, or the user would approve a call that can never run.
func TestApprovalLeftBehindByAStoppedGateIsNotOffered(t *testing.T) {
	q, db := open(t)
	past := time.Now().Add(-time.Minute).UnixMilli()
	res, err := db.ExecContext(t.Context(), `INSERT INTO approvals
		(created_ms, deadline_ms, project, agent, session, tool, args, rule) VALUES (?, ?, 'p', 'codex', 's', 't', '{}', 1)`,
		past-50_000, past)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	noneLeft(t, q)
	if err := q.Decide(t.Context(), id, approval.ApproveOnce); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("err = %v, want ErrNotPending", err)
	}
}

// waitRow waits for the first approval written after the one with id after, and returns its id and
// deadline. It reads the table rather than Pending, which hides a row as soon as its deadline passes.
func waitRow(t *testing.T, db *sql.DB, after int64) (int64, time.Time) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		var id, ms int64
		err := db.QueryRowContext(t.Context(), `SELECT id, deadline_ms FROM approvals WHERE id > ? ORDER BY id LIMIT 1`,
			after).Scan(&id, &ms)
		if err == nil {
			return id, time.UnixMilli(ms)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	t.Fatal("no approval was written")
	return 0, time.Time{}
}

// Whichever of the user and the deadline gets there first, the waiting call and the user must be told
// the same thing: a decision the user was told succeeded is the one the call gets.
func TestDecisionRacingTheDeadlineHasOneWinner(t *testing.T) {
	q, db := open(t)
	var decided, timedOut int
	var id int64
	for i := range 20 {
		// Long enough that a slow runner still reads the row well before its deadline, so the decision
		// below lands in the last few milliseconds, where it races Ask's own expiry.
		done := ask(t.Context(), q, req, 250*time.Millisecond)
		var deadline time.Time
		id, deadline = waitRow(t, db, id)
		time.Sleep(time.Until(deadline) - time.Duration(i%5)*time.Millisecond)
		decideErr := q.Decide(t.Context(), id, approval.ApproveOnce)
		a := result(t, done)
		switch {
		case decideErr == nil && (!a.out.Approved || a.out.By != approval.ByUser):
			t.Fatalf("round %d: Decide succeeded but the call got %+v", i, a.out)
		case errors.Is(decideErr, approval.ErrNotPending) && a.out.By != approval.ByTimeout:
			t.Fatalf("round %d: Decide was refused but the call got %+v", i, a.out)
		case decideErr != nil && !errors.Is(decideErr, approval.ErrNotPending):
			t.Fatalf("round %d: %v", i, decideErr)
		}
		if decideErr == nil {
			decided++
		} else {
			timedOut++
		}
	}
	t.Logf("Decide won %d/20 rounds, the deadline won %d/20", decided, timedOut)
}

// Two decisions on the same pending approval, arriving at once, must still leave exactly one winner:
// SQLite's BEGIN IMMEDIATE serializes them, so the second always finds the row no longer pending.
func TestConcurrentDecisionsOnOneApprovalHaveOneWinner(t *testing.T) {
	q, _ := open(t)
	for i := range 20 {
		done := ask(t.Context(), q, req, 10*time.Second)
		p := waitPending(t, q)
		var approveErr, denyErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			approveErr = q.Decide(t.Context(), p.ID, approval.ApproveOnce)
		}()
		go func() {
			defer wg.Done()
			denyErr = q.Decide(t.Context(), p.ID, approval.Deny)
		}()
		wg.Wait()
		a := result(t, done)
		switch {
		case approveErr == nil && denyErr == nil:
			t.Fatalf("round %d: both decisions succeeded", i)
		case approveErr != nil && denyErr != nil:
			t.Fatalf("round %d: both decisions failed: approve %v, deny %v", i, approveErr, denyErr)
		case approveErr == nil:
			if !errors.Is(denyErr, approval.ErrNotPending) {
				t.Fatalf("round %d: losing deny err = %v, want ErrNotPending", i, denyErr)
			}
			if !a.out.Approved || a.out.By != approval.ByUser {
				t.Fatalf("round %d: approve won but Ask got %+v", i, a.out)
			}
		default: // denyErr == nil
			if !errors.Is(approveErr, approval.ErrNotPending) {
				t.Fatalf("round %d: losing approve err = %v, want ErrNotPending", i, approveErr)
			}
			if a.out.Approved || a.out.By != approval.ByUser {
				t.Fatalf("round %d: deny won but Ask got %+v", i, a.out)
			}
		}
	}
}

// grantFor asks r and approves it for the session, as A does, and returns the approval's id.
func grantFor(t *testing.T, q *approval.Queue, r approval.Request) int64 {
	t.Helper()
	done := ask(t.Context(), q, r, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if a := result(t, done); !a.out.Approved {
		t.Fatalf("outcome = %+v, want it approved", a.out)
	}
	return p.ID
}

// covered reports whether a grant covers codex's calls to req's tool in session under req's rule.
func covered(t *testing.T, q *approval.Queue, session string) bool {
	t.Helper()
	_, ok, err := q.Granted(t.Context(), "codex", session, req.Tool, req.RuleKey)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestGrantsListsTheSessionGrants(t *testing.T) {
	q, _ := open(t)
	before := time.Now().Add(-time.Second)
	id := grantFor(t, q, req)
	once := req
	once.Session = "s2"
	done := ask(t.Context(), q, once, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	result(t, done)
	list, err := q.Grants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("grants = %+v, want only the session approval's", list)
	}
	if g := list[0]; g.ID != id || g.Agent != "codex" || g.Session != "s1" || g.Tool != "github__create_issue" ||
		g.Rule != 3 || g.Granted.Before(before) {
		t.Fatalf("grant = %+v", g)
	}
}

func TestRevokeDeletesOneGrantAndSaysWhichItWas(t *testing.T) {
	q, _ := open(t)
	id := grantFor(t, q, req)
	other := req
	other.Session = "s2"
	grantFor(t, q, other)
	g, err := q.Revoke(t.Context(), id)
	if err != nil || g.ID != id || g.Session != "s1" || g.Tool != req.Tool || g.Rule != 3 {
		t.Fatalf("Revoke = %+v, %v", g, err)
	}
	if covered(t, q, "s1") {
		t.Fatal("the revoked grant still covers calls")
	}
	if !covered(t, q, "s2") {
		t.Fatal("revoking one grant took the other too")
	}
	if _, err = q.Revoke(t.Context(), id); !errors.Is(err, approval.ErrNoGrant) {
		t.Fatalf("revoking twice: err = %v, want ErrNoGrant", err)
	}
}

func TestRevokeAllDeletesEveryGrant(t *testing.T) {
	q, _ := open(t)
	grantFor(t, q, req)
	other := req
	other.Session = "s2"
	grantFor(t, q, other)
	n, err := q.RevokeAll(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("RevokeAll = %d, %v; want 2", n, err)
	}
	if list, err := q.Grants(t.Context()); err != nil || len(list) != 0 {
		t.Fatalf("grants after RevokeAll = %+v, %v", list, err)
	}
}

func TestAnApprovalRecordsWhichRuleListAsked(t *testing.T) {
	q, _ := open(t)
	r := req
	r.ProjectRule = true
	done := ask(t.Context(), q, r, 10*time.Second)
	p := waitPending(t, q)
	if !p.ProjectRule || p.Rule != 3 {
		t.Fatalf("pending = %+v, want project rule 3", p)
	}
	if err := q.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	result(t, done)
	if list, err := q.Grants(t.Context()); err != nil || len(list) != 1 || !list[0].ProjectRule {
		t.Fatalf("grants = %+v, %v", list, err)
	}
	if got := approval.RuleName(2, true); got != "project rule 2" {
		t.Fatalf("RuleName(2, true) = %q", got)
	}
	if got := approval.RuleName(3, false); got != "rule 3" {
		t.Fatalf("RuleName(3, false) = %q", got)
	}
}
