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
	if _, ok, err := q.Granted(t.Context(), req.Agent, req.Session, req.Tool); err != nil || ok {
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
	if _, ok, err := q.Granted(t.Context(), req.Agent, req.Session, req.Tool); err != nil || ok {
		t.Fatalf("a denial left a grant (ok %v, err %v)", ok, err)
	}
	noneLeft(t, q)
}

func TestApproveForTheSessionGrantsOnlyThatAgentSessionAndTool(t *testing.T) {
	q, _ := open(t)
	done := ask(t.Context(), q, req, 10*time.Second)
	p := waitPending(t, q)
	if err := q.Decide(t.Context(), p.ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	if a := result(t, done); !a.out.Approved {
		t.Fatalf("Ask = %+v, %v", a.out, a.err)
	}
	id, ok, err := q.Granted(t.Context(), "codex", "s1", "github__create_issue")
	if err != nil || !ok || id != p.ID {
		t.Fatalf("Granted = %d, %v, %v; want %d, true", id, ok, err, p.ID)
	}
	for _, other := range [][3]string{
		{"claude", "s1", "github__create_issue"},
		{"codex", "s2", "github__create_issue"},
		{"codex", "s1", "github__create_repo"},
	} {
		if _, ok, err := q.Granted(t.Context(), other[0], other[1], other[2]); err != nil || ok {
			t.Errorf("Granted(%v) = %v, %v; the grant reaches beyond its agent, session and tool", other, ok, err)
		}
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

// Whichever of the user and the deadline gets there first, the waiting call and the user must be told
// the same thing: a decision the user was told succeeded is the one the call gets.
func TestDecisionRacingTheDeadlineHasOneWinner(t *testing.T) {
	q, _ := open(t)
	var decided, timedOut int
	for i := range 20 {
		timeout := 60 * time.Millisecond
		done := ask(t.Context(), q, req, timeout)
		p := waitPending(t, q)
		time.Sleep(time.Until(p.Deadline) - time.Duration(i%5)*time.Millisecond)
		decideErr := q.Decide(t.Context(), p.ID, approval.ApproveOnce)
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
