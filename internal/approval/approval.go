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
// shows them. Rule is the position of the rule that asked, in the user's config or, when ProjectRule is
// set, in the project's .derbent.toml (ADR 0014). RuleKey is what a session grant for the call is keyed
// on (rule.Set.Key, joined with the project rule's for a call the project asked about), which scopes
// the grant to that rule.
type Request struct {
	Project     string
	Agent       string
	Session     string
	Tool        string
	Args        string
	Rule        int
	ProjectRule bool // Rule is a position in the project's rules file, not in the user's config
	RuleKey     string
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
	deadline := created.Add(timeout)
	res, err := q.db.ExecContext(ctx, `INSERT INTO approvals
		(created_ms, deadline_ms, project, agent, session, tool, args, rule, project_rule, rule_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		created.UnixMilli(), deadline.UnixMilli(), r.Project, r.Agent, r.Session, r.Tool, r.Args, r.Rule, r.ProjectRule, r.RuleKey)
	if err != nil {
		return Outcome{}, fmt.Errorf("write approval: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Outcome{}, fmt.Errorf("write approval: %w", err)
	}
	// The wait ends at the deadline the row carries, however long the write took.
	expire := time.NewTimer(time.Until(deadline))
	defer expire.Stop()
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	// Every exit below either has already read a final state or closes the row itself before
	// returning: none can leave it pending.
	for {
		select {
		case <-ctx.Done():
			return q.closeForCtx(ctx, id)

		case <-expire.C:
			// A cancel landing in the same instant must not stop this write from completing, or the
			// row is left pending after Ask has already returned.
			ectx := context.WithoutCancel(ctx)
			ended, err := q.end(ectx, id, "expired")
			if err != nil || ended {
				return Outcome{ID: id, By: ByTimeout}, err
			}
			// The user decided in the same instant, and was told it worked: that decision stands.
			out, _, err := q.outcome(ectx, id)
			return out, err

		case <-poll.C:
			out, decided, err := q.outcome(ctx, id)
			if err == nil {
				if decided {
					return out, nil
				}
				continue
			}
			// The read failed. A ctx that is already gone is the same exit as above. Otherwise this
			// was a transient failure with the agent still waiting: close the row so it cannot be
			// decided out from under a call about to fail, unless the user's decision had already
			// landed, in which case that decision wins instead of the read error.
			if ctx.Err() != nil {
				return q.closeForCtx(ctx, id)
			}
			fout, withdrawn, ferr := q.finish(ctx, id)
			if ferr != nil {
				return fout, ferr
			}
			if withdrawn {
				return fout, err
			}
			return fout, nil
		}
	}
}

// finish closes id as withdrawn if it is still pending, using a context immune to ctx's cancellation,
// and reports whether it did. If the approval had already been decided or had expired, it reads back
// that outcome instead of overwriting it.
func (q *Queue) finish(ctx context.Context, id int64) (out Outcome, withdrawn bool, err error) {
	ctx = context.WithoutCancel(ctx)
	ended, err := q.end(ctx, id, "withdrawn")
	if err != nil {
		return Outcome{ID: id}, false, err
	}
	if ended {
		return Outcome{ID: id}, true, nil
	}
	out, _, err = q.outcome(ctx, id)
	return out, false, err
}

// closeForCtx withdraws id, or reports the decision that beat it there, and returns ctx's own error.
// It is used by the two exits from Ask's loop that end because ctx did.
func (q *Queue) closeForCtx(ctx context.Context, id int64) (Outcome, error) {
	out, _, err := q.finish(ctx, id)
	if err != nil {
		return out, errors.Join(ctx.Err(), err)
	}
	return out, ctx.Err()
}

// Decide records the user's verdict on a pending approval. ApproveSession also lets through, without
// asking, every later call to the same tool from the same agent session that the same rule asks about
// under the same rules above it; an approval with no rule key is approved once and grants nothing. An
// approval that is not waiting, including one past its deadline that its gate has not closed yet, gives
// ErrNotPending.
func (q *Queue) Decide(ctx context.Context, id int64, v Verdict) error {
	state := "approved"
	switch v {
	case ApproveOnce, ApproveSession:
	case Deny:
		state = "denied"
	default:
		return fmt.Errorf("decide approval %d: unknown verdict %q", id, v)
	}
	return store.Immediate(ctx, q.db, func(ctx context.Context, conn *sql.Conn) error {
		now := q.now().UnixMilli()
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
		if _, err = conn.ExecContext(ctx, `INSERT OR REPLACE INTO grants (agent, session, tool, rule_key, approval_id)
			SELECT agent, session, tool, rule_key, id FROM approvals WHERE id = ? AND rule_key <> ''`, id); err != nil {
			return fmt.Errorf("grant approval %d: %w", id, err)
		}
		return nil
	})
}

// Pending lists the approvals waiting for the user, oldest first. One past its deadline is left out
// even while it is still marked pending, which is what a gate that stopped while waiting leaves.
func (q *Queue) Pending(ctx context.Context) ([]Pending, error) {
	projectRule, err := q.projectRuleColumn(ctx, "project_rule")
	if err != nil {
		return nil, err
	}
	//nolint:gosec // projectRule is a column name or 0, never input
	rows, err := q.db.QueryContext(ctx, `SELECT id, created_ms, deadline_ms, project, agent, session, tool, args, rule, `+projectRule+`, rule_key
		FROM approvals WHERE state = 'pending' AND deadline_ms > ? ORDER BY id`, q.now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("read approvals: %w", err)
	}
	defer rows.Close()
	var out []Pending
	for rows.Next() {
		var p Pending
		var created, deadline int64
		if err = rows.Scan(&p.ID, &created, &deadline, &p.Project, &p.Agent, &p.Session, &p.Tool, &p.Args, &p.Rule, &p.ProjectRule, &p.RuleKey); err != nil {
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

// Granted reports whether the user approved, for the rest of this agent's session, the calls to tool
// that the rule with fingerprint ruleKey asks about, and the approval that did. An empty ruleKey never
// matches a grant.
func (q *Queue) Granted(ctx context.Context, agent, session, tool, ruleKey string) (int64, bool, error) {
	if ruleKey == "" {
		return 0, false, nil
	}
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT approval_id FROM grants WHERE agent = ? AND session = ? AND tool = ? AND rule_key = ?`,
		agent, session, tool, ruleKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read grant: %w", err)
	}
	return id, true, nil
}

// ErrNoGrant marks a revoke of an approval id that holds no session grant.
var ErrNoGrant = errors.New("no session grant")

// Grant is a session grant: the approval that made it, whose id names it, and what it covers.
type Grant struct {
	ID          int64 // the approval the user approved for the session
	Agent       string
	Session     string
	Tool        string
	Rule        int       // the rule that asked, from the approval
	ProjectRule bool      // Rule is a position in the project's rules file
	Granted     time.Time // when the user approved it
}

// grantColumns reads a grant together with the approval behind it, in the order scanGrant takes them,
// with projectRule as what projectRuleColumn gives for "a.project_rule".
func grantColumns(projectRule string) string {
	return `SELECT g.approval_id, g.agent, g.session, g.tool, a.rule, ` + projectRule + `, coalesce(a.decided_ms, 0)
	FROM grants g JOIN approvals a ON a.id = g.approval_id`
}

// projectRuleColumn returns column, which names approvals.project_rule, or 0 on a database from before
// migration 0006 added it: the commands that only read open a database without migrating it, and every
// approval written before project rules was asked by one of the user's rules.
func (q *Queue) projectRuleColumn(ctx context.Context, column string) (string, error) {
	var n int
	if err := q.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('approvals') WHERE name = 'project_rule'`).Scan(&n); err != nil {
		return "", fmt.Errorf("read approvals columns: %w", err)
	}
	if n == 0 {
		return "0", nil
	}
	return column, nil
}

// RuleName names the rule that asked: "rule 3" for the user's rules, "project rule 2" for the project's.
func RuleName(n int, project bool) string {
	if project {
		return fmt.Sprintf("project rule %d", n)
	}
	return fmt.Sprintf("rule %d", n)
}

func scanGrant(row interface{ Scan(dest ...any) error }) (Grant, error) {
	var g Grant
	var decided int64
	if err := row.Scan(&g.ID, &g.Agent, &g.Session, &g.Tool, &g.Rule, &g.ProjectRule, &decided); err != nil {
		return Grant{}, err
	}
	g.Granted = time.UnixMilli(decided)
	return g, nil
}

// Grants lists every session grant, oldest approval first.
func (q *Queue) Grants(ctx context.Context) ([]Grant, error) {
	projectRule, err := q.projectRuleColumn(ctx, "a.project_rule")
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, grantColumns(projectRule)+` ORDER BY g.approval_id`)
	if err != nil {
		return nil, fmt.Errorf("read grants: %w", err)
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if g, err = scanGrant(rows); err != nil {
			return nil, fmt.Errorf("read grant: %w", err)
		}
		out = append(out, g)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read grants: %w", err)
	}
	return out, nil
}

// Answered is a call the user approved or denied: what rule suggestions are made from (ADR 0019).
type Answered struct {
	Agent       string
	Tool        string
	Args        string // after redaction, as the approval queue holds them
	Rule        int
	ProjectRule bool // Rule is a position in the project's rules file
	Approved    bool
}

// Answered lists the calls the user approved or denied, oldest first: every agent's calls to every tool,
// or only agent's calls to tool when both are given. Calls that timed out or were withdrawn were never
// answered and are left out. A database from before migration 0006, which the commands that only read do
// not migrate, is read as Grants reads it.
func (q *Queue) Answered(ctx context.Context, agent, tool string) ([]Answered, error) {
	projectRule, err := q.projectRuleColumn(ctx, "project_rule")
	if err != nil {
		return nil, err
	}
	//nolint:gosec // projectRule is a column name or 0, never input
	rows, err := q.db.QueryContext(ctx, `SELECT agent, tool, args, rule, `+projectRule+`, state = 'approved'
		FROM approvals WHERE state IN ('approved', 'denied') AND (?1 = '' OR agent = ?1) AND (?2 = '' OR tool = ?2)
		ORDER BY id`, agent, tool)
	if err != nil {
		return nil, fmt.Errorf("read answered approvals: %w", err)
	}
	defer rows.Close()
	var out []Answered
	for rows.Next() {
		var a Answered
		if err = rows.Scan(&a.Agent, &a.Tool, &a.Args, &a.Rule, &a.ProjectRule, &a.Approved); err != nil {
			return nil, fmt.Errorf("read answered approval: %w", err)
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read answered approvals: %w", err)
	}
	return out, nil
}

// Revoke deletes the session grant that approval id made and returns it. Both paths read the grants on
// every call, so the next call it covered asks again. An id that holds no grant gives ErrNoGrant.
func (q *Queue) Revoke(ctx context.Context, id int64) (Grant, error) {
	var revoked Grant
	err := store.Immediate(ctx, q.db, func(ctx context.Context, conn *sql.Conn) error {
		// Revoke writes, so its database was opened with store.Open and is migrated.
		g, err := scanGrant(conn.QueryRowContext(ctx, grantColumns("a.project_rule")+` WHERE g.approval_id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("grant #%d: %w", id, ErrNoGrant)
		}
		if err != nil {
			return fmt.Errorf("read grant #%d: %w", id, err)
		}
		if _, err = conn.ExecContext(ctx, `DELETE FROM grants WHERE approval_id = ?`, id); err != nil {
			return fmt.Errorf("revoke grant #%d: %w", id, err)
		}
		revoked = g
		return nil
	})
	return revoked, err
}

// RevokeAll deletes every session grant and returns how many there were.
func (q *Queue) RevokeAll(ctx context.Context) (int64, error) {
	res, err := q.db.ExecContext(ctx, `DELETE FROM grants`)
	if err != nil {
		return 0, fmt.Errorf("revoke grants: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("revoke grants: %w", err)
	}
	return n, nil
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
	default: // pending; only the Ask waiting on it withdraws it, when its ctx ends or a read fails
		return Outcome{ID: id}, false, nil
	}
}
