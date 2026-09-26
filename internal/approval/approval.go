// Package approval holds the calls a rule sends to the user. A gate process writes a pending approval
// and polls the database for the decision, which the UI or `derbent approve` writes. SQLite is all
// they share (ADR 0001).
package approval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

// Verdict is the user's answer to a pending approval.
type Verdict string

// The answers the user can give.
const (
	ApproveOnce    Verdict = "once"
	ApproveSession Verdict = "session"
	Deny           Verdict = "deny"
)

// What decided an approval, as Outcome.By reports it.
const (
	ByUser    = "user"
	ByTimeout = "timeout"
)

// ErrNotPending marks a decision on an approval that is not waiting: already decided, expired,
// withdrawn by its agent, left behind by a gate that stopped, or never written.
var ErrNotPending = errors.New("approval is not pending")

// pollEvery is how often a waiting call looks for the user's decision (ADR 0001).
const pollEvery = 200 * time.Millisecond

// Request is a call waiting for the user. Args are the call's arguments after redaction, as the UI
// shows them.
type Request struct {
	Project string
	Agent   string
	Session string
	Tool    string
	Args    string
	Rule    int
}

// Pending is an approval the user has not decided yet.
type Pending struct {
	Request
	ID       int64
	Created  time.Time
	Deadline time.Time
}

// Outcome is how an Ask ended.
type Outcome struct {
	ID       int64
	Approved bool
	By       string // ByUser or ByTimeout
}

// Queue writes, decides and waits on approvals.
type Queue struct {
	db  *sql.DB
	now func() time.Time
}

// NewQueue returns a Queue over db, which must have been opened with store.Open.
func NewQueue(db *sql.DB) *Queue {
	return &Queue{db: db, now: time.Now}
}

// Ask writes r as a pending approval and waits for the user. It returns the user's decision, a denial
// by ByTimeout when timeout passes first, or ctx's error when ctx ends first, in which case the
// approval is withdrawn so that nobody can approve a call whose agent has gone.
func (q *Queue) Ask(ctx context.Context, r Request, timeout time.Duration) (Outcome, error) {
	created := q.now()
	res, err := q.db.ExecContext(ctx, `INSERT INTO approvals
		(created_ms, deadline_ms, project, agent, session, tool, args, rule) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		created.UnixMilli(), created.Add(timeout).UnixMilli(), r.Project, r.Agent, r.Session, r.Tool, r.Args, r.Rule)
	if err != nil {
		return Outcome{}, fmt.Errorf("write approval: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Outcome{}, fmt.Errorf("write approval: %w", err)
	}
	expire := time.NewTimer(timeout)
	defer expire.Stop()
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			if _, err := q.end(context.WithoutCancel(ctx), id, "withdrawn"); err != nil {
				return Outcome{ID: id}, errors.Join(ctx.Err(), err)
			}
			return Outcome{ID: id}, ctx.Err()
		case <-expire.C:
			ended, err := q.end(ctx, id, "expired")
			if err != nil || ended {
				return Outcome{ID: id, By: ByTimeout}, err
			}
			// The user decided in the same instant, and was told it worked: that decision stands.
			out, _, err := q.outcome(ctx, id)
			return out, err
		case <-poll.C:
			out, decided, err := q.outcome(ctx, id)
			if err != nil || decided {
				return out, err
			}
		}
	}
}

// Decide records the user's verdict on a pending approval. ApproveSession also lets every later call
// to the same tool from the same agent session through without asking. An approval that is not
// waiting, including one past its deadline that its gate has not closed yet, gives ErrNotPending.
func (q *Queue) Decide(ctx context.Context, id int64, v Verdict) error {
	state := "approved"
	switch v {
	case ApproveOnce, ApproveSession:
	case Deny:
		state = "denied"
	default:
		return fmt.Errorf("decide approval %d: unknown verdict %q", id, v)
	}
	now := q.now().UnixMilli()
	return store.Immediate(ctx, q.db, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `UPDATE approvals SET state = ?, decided_ms = ?
			WHERE id = ? AND state = 'pending' AND deadline_ms > ?`, state, now, id, now)
		if err != nil {
			return fmt.Errorf("decide approval %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("decide approval %d: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("approval %d: %w", id, ErrNotPending)
		}
		if v != ApproveSession {
			return nil
		}
		if _, err = conn.ExecContext(ctx, `INSERT OR REPLACE INTO grants (agent, session, tool, approval_id)
			SELECT agent, session, tool, id FROM approvals WHERE id = ?`, id); err != nil {
			return fmt.Errorf("grant approval %d: %w", id, err)
		}
		return nil
	})
}

// Pending lists the approvals waiting for the user, oldest first. One past its deadline is left out
// even while it is still marked pending, which is what a gate that stopped while waiting leaves.
func (q *Queue) Pending(ctx context.Context) ([]Pending, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, created_ms, deadline_ms, project, agent, session, tool, args, rule
		FROM approvals WHERE state = 'pending' AND deadline_ms > ? ORDER BY id`, q.now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("read approvals: %w", err)
	}
	defer rows.Close()
	var out []Pending
	for rows.Next() {
		var p Pending
		var created, deadline int64
		if err = rows.Scan(&p.ID, &created, &deadline, &p.Project, &p.Agent, &p.Session, &p.Tool, &p.Args, &p.Rule); err != nil {
			return nil, fmt.Errorf("read approval: %w", err)
		}
		p.Created, p.Deadline = time.UnixMilli(created), time.UnixMilli(deadline)
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read approvals: %w", err)
	}
	return out, nil
}

// Granted reports whether the user approved tool for the rest of this agent's session, and the
// approval that did.
func (q *Queue) Granted(ctx context.Context, agent, session, tool string) (int64, bool, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT approval_id FROM grants WHERE agent = ? AND session = ? AND tool = ?`,
		agent, session, tool).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read grant: %w", err)
	}
	return id, true, nil
}

// end moves a pending approval to state and reports whether it was still pending.
func (q *Queue) end(ctx context.Context, id int64, state string) (bool, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE approvals SET state = ?, decided_ms = ? WHERE id = ? AND state = 'pending'`,
		state, q.now().UnixMilli(), id)
	if err != nil {
		return false, fmt.Errorf("close approval %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("close approval %d: %w", id, err)
	}
	return n == 1, nil
}

// outcome reads an approval and reports whether it has been decided.
func (q *Queue) outcome(ctx context.Context, id int64) (Outcome, bool, error) {
	var state string
	if err := q.db.QueryRowContext(ctx, `SELECT state FROM approvals WHERE id = ?`, id).Scan(&state); err != nil {
		return Outcome{ID: id}, false, fmt.Errorf("read approval %d: %w", id, err)
	}
	switch state {
	case "approved":
		return Outcome{ID: id, Approved: true, By: ByUser}, true, nil
	case "denied":
		return Outcome{ID: id, By: ByUser}, true, nil
	case "expired":
		return Outcome{ID: id, By: ByTimeout}, true, nil
	default: // pending; only this Ask withdraws, after ctx has ended
		return Outcome{ID: id}, false, nil
	}
}
