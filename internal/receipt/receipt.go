// Package receipt appends a hash-chained receipt for every call through the gate and verifies the
// chain afterwards, in the database or in an export of it.
package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

// Genesis is the previous hash recorded on the first receipt.
const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

// Receipt is the record of one call through the gate.
type Receipt struct {
	Seq          int64
	At           time.Time
	Project      string
	Agent        string
	Session      string
	Tool         string
	Args         string // the call's arguments as a JSON object
	ArgsSHA256   string
	Decision     string
	DecidedBy    string
	Outcome      string
	ResultSize   int64
	ResultSHA256 string
	Duration     time.Duration
	PrevHash     string
	Hash         string
}

// Result is what Verify found.
type Result struct {
	Count int64
	// Head is the hash of the last receipt, or Genesis for an empty log. Keeping a copy of it
	// elsewhere is what shows a chain whose tail was cut off or that was rewritten as a whole.
	Head string
	// FirstBad is the sequence number where the chain first breaks, or 0 when it is intact.
	FirstBad int64
	Reason   string
}

// Log appends receipts to the database and verifies the chain they form.
type Log struct {
	db  *sql.DB
	now func() time.Time
}

// NewLog returns a Log over db, which must have been opened with store.Open.
func NewLog(db *sql.DB) *Log {
	return &Log{db: db, now: time.Now}
}

// Append stores r as the next receipt and returns it with Seq, PrevHash and Hash filled in, and At
// when it was zero. Appends from any number of processes are serialised by SQLite's write lock, so the
// chain never forks.
func (l *Log) Append(ctx context.Context, r Receipt) (Receipt, error) {
	if r.At.IsZero() {
		r.At = l.now()
	}
	r.At = r.At.UTC()
	err := store.Immediate(ctx, l.db, func(ctx context.Context, conn *sql.Conn) error {
		var last int64
		prev := Genesis
		err := conn.QueryRowContext(ctx, `SELECT seq, hash FROM receipts ORDER BY seq DESC LIMIT 1`).Scan(&last, &prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read chain head: %w", err)
		}
		r.Seq, r.PrevHash = last+1, prev
		w := rowOf(r)
		r.Hash = w.Sum()
		_, err = conn.ExecContext(ctx, `INSERT INTO receipts (`+rowColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.Seq, w.At, w.Project, w.Agent, w.Session, w.Tool, w.Args, w.ArgsSHA256, w.Decision, w.DecidedBy,
			w.Outcome, w.ResultSize, w.ResultSHA256, w.DurationMS, w.PrevHash, r.Hash)
		if err != nil {
			return fmt.Errorf("insert receipt: %w", err)
		}
		return nil
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("append receipt: %w", err)
	}
	return r, nil
}

// Verify walks the whole chain in sequence order and reports the first receipt whose sequence
// number, previous hash or own hash is wrong.
func (l *Log) Verify(ctx context.Context) (Result, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM receipts ORDER BY seq`)
	if err != nil {
		return Result{}, fmt.Errorf("read receipts: %w", err)
	}
	defer rows.Close()
	res := Result{Head: Genesis}
	want, prev := int64(1), Genesis
	for rows.Next() {
		var w Row
		if w, err = scanRow(rows); err != nil {
			return Result{}, fmt.Errorf("read receipt: %w", err)
		}
		res.Count++
		if res.FirstBad == 0 {
			switch {
			case w.Seq != want:
				res.FirstBad, res.Reason = w.Seq, fmt.Sprintf("expected sequence number %d", want)
			case w.PrevHash != prev:
				res.FirstBad, res.Reason = w.Seq, "previous hash does not match the receipt before it"
			case w.Sum() != w.Hash:
				res.FirstBad, res.Reason = w.Seq, "hash does not match the receipt's contents"
			}
		}
		want, prev, res.Head = w.Seq+1, w.Hash, w.Hash
	}
	if err = rows.Err(); err != nil {
		return Result{}, fmt.Errorf("read receipts: %w", err)
	}
	return res, nil
}

// Filter selects receipts. Empty fields match everything.
type Filter struct {
	Agent   string
	Tool    string // a glob: * matches any run of characters, ? exactly one
	Project string
	// Since keeps receipts from this second on.
	Since time.Time
	// AfterSeq keeps receipts with a greater sequence number, for following new ones.
	AfterSeq int64
	// Limit keeps the newest matches; 0 means 100, and below 0 means every match.
	Limit int
}

// Rows returns the newest receipts matching f exactly as the database stores them, oldest first.
// derbent receipts --json prints them, so an export carries every field the hash covers (ADR 0017).
func (l *Log) Rows(ctx context.Context, f Filter) ([]Row, error) {
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	since := ""
	if !f.Since.IsZero() {
		since = sinceBound(f.Since)
	}
	// SQLite reads a negative LIMIT as no limit.
	rows, err := l.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM receipts
		WHERE seq > ?1 AND (?2 = '' OR agent = ?2) AND (?3 = '' OR tool GLOB ?3)
			AND (?4 = '' OR project = ?4) AND (?5 = '' OR at >= ?5)
		ORDER BY seq DESC LIMIT ?6`, f.AfterSeq, f.Agent, f.Tool, f.Project, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var w Row
		if w, err = scanRow(rows); err != nil {
			return nil, fmt.Errorf("list receipts: %w", err)
		}
		out = append(out, w)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	slices.Reverse(out)
	return out, nil
}

// List returns the newest receipts matching f, oldest first.
func (l *Log) List(ctx context.Context, f Filter) ([]Receipt, error) {
	rows, err := l.Rows(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]Receipt, 0, len(rows))
	for _, w := range rows {
		r, convErr := w.Receipt()
		if convErr != nil {
			return nil, convErr
		}
		out = append(out, r)
	}
	return out, nil
}

// AgentSeen is an agent and the time of its latest receipt.
type AgentSeen struct {
	Agent string
	Last  time.Time
}

// Agents returns the agents with a receipt since the given time, by name.
func (l *Log) Agents(ctx context.Context, since time.Time) ([]AgentSeen, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT agent, max(at) FROM receipts WHERE at >= ? GROUP BY agent ORDER BY agent`,
		sinceBound(since))
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []AgentSeen
	for rows.Next() {
		var a AgentSeen
		var at string
		if err = rows.Scan(&a.Agent, &at); err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		if a.Last, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("list agents: time %q: %w", at, err)
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return out, nil
}

// Allowed is a call a receipt says was let through: its tool and when.
type Allowed struct {
	Tool string
	At   time.Time
}

// AllowedSince returns the calls agent had let through (decision allow, whatever let them through) at
// or after since, in no particular order. Stored times do not compare correctly as text within one
// second, so it reads from the start of since's second and keeps the exact ones.
func (l *Log) AllowedSince(ctx context.Context, agent string, since time.Time) ([]Allowed, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT tool, at FROM receipts WHERE agent = ? AND at >= ? AND decision = 'allow'`,
		agent, sinceBound(since))
	if err != nil {
		return nil, fmt.Errorf("read allowed calls: %w", err)
	}
	defer rows.Close()
	var out []Allowed
	for rows.Next() {
		var a Allowed
		var at string
		if err = rows.Scan(&a.Tool, &at); err != nil {
			return nil, fmt.Errorf("read allowed call: %w", err)
		}
		if a.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("read allowed call: time %q: %w", at, err)
		}
		if !a.At.Before(since) {
			out = append(out, a)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read allowed calls: %w", err)
	}
	return out, nil
}

// sinceBound writes t so that it compares correctly as text against stored times, which are UTC RFC
// 3339 with a fraction of any length: to the second, without the zone, so that every time in that
// second sorts after it.
func sinceBound(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05")
}

// Row is a receipt exactly as the database stores it: every field the hash covers, in the form the hash
// reads it, and the hash. derbent receipts --json prints rows and derbent verify --file reads them back,
// so an export can be checked without the database (ADR 0017). Hashes are computed over these values, so
// verification never depends on how a time or a duration is formatted when read back.
type Row struct {
	Seq          int64  `json:"seq"`
	At           string `json:"at"` // UTC RFC 3339 with the fraction as stored
	Project      string `json:"project"`
	Agent        string `json:"agent"`
	Session      string `json:"session"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	ArgsSHA256   string `json:"args_sha256"`
	Decision     string `json:"decision"`
	DecidedBy    string `json:"decided_by"`
	Outcome      string `json:"outcome"`
	ResultSize   int64  `json:"result_size"`
	ResultSHA256 string `json:"result_sha256"`
	DurationMS   int64  `json:"duration_ms"`
	PrevHash     string `json:"prev_hash"`
	Hash         string `json:"hash"`
}

// rowColumns are the receipts table's columns in the order of Row's fields.
const rowColumns = `seq, at, project, agent, session, tool, args, args_sha256, decision, decided_by, outcome,
	result_size, result_sha256, duration_ms, prev_hash, hash`

// scanRow reads one row selected with rowColumns.
func scanRow(rows *sql.Rows) (Row, error) {
	var r Row
	err := rows.Scan(&r.Seq, &r.At, &r.Project, &r.Agent, &r.Session, &r.Tool, &r.Args, &r.ArgsSHA256, &r.Decision,
		&r.DecidedBy, &r.Outcome, &r.ResultSize, &r.ResultSHA256, &r.DurationMS, &r.PrevHash, &r.Hash)
	return r, err
}

// rowOf is r as it will be stored, without its hash.
func rowOf(r Receipt) Row {
	return Row{
		Seq: r.Seq, At: r.At.Format(time.RFC3339Nano), Project: r.Project, Agent: r.Agent, Session: r.Session,
		Tool: r.Tool, Args: r.Args, ArgsSHA256: r.ArgsSHA256, Decision: r.Decision, DecidedBy: r.DecidedBy,
		Outcome: r.Outcome, ResultSize: r.ResultSize, ResultSHA256: r.ResultSHA256,
		DurationMS: r.Duration.Milliseconds(), PrevHash: r.PrevHash,
	}
}

// Sum hashes the row's fields in a fixed order, each prefixed with its length, so that two different rows
// never feed the hash the same bytes. The row's own Hash is not among them: a row is intact when Sum
// equals it. Append, Verify and ExportCheck all use it.
func (r Row) Sum() string {
	h := sha256.New()
	for _, f := range [...]string{
		strconv.FormatInt(r.Seq, 10), r.At, r.Project, r.Agent, r.Session, r.Tool, r.Args, r.ArgsSHA256,
		r.Decision, r.DecidedBy, r.Outcome, strconv.FormatInt(r.ResultSize, 10), r.ResultSHA256,
		strconv.FormatInt(r.DurationMS, 10), r.PrevHash,
	} {
		_, _ = fmt.Fprintf(h, "%d:%s;", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Receipt turns the row back into a Receipt.
func (r Row) Receipt() (Receipt, error) {
	at, err := time.Parse(time.RFC3339Nano, r.At)
	if err != nil {
		return Receipt{}, fmt.Errorf("receipt %d: time %q: %w", r.Seq, r.At, err)
	}
	return Receipt{
		Seq: r.Seq, At: at, Project: r.Project, Agent: r.Agent, Session: r.Session, Tool: r.Tool, Args: r.Args,
		ArgsSHA256: r.ArgsSHA256, Decision: r.Decision, DecidedBy: r.DecidedBy, Outcome: r.Outcome,
		ResultSize: r.ResultSize, ResultSHA256: r.ResultSHA256, Duration: time.Duration(r.DurationMS) * time.Millisecond,
		PrevHash: r.PrevHash, Hash: r.Hash,
	}, nil
}

// Export is what an ExportCheck found in the lines it was given.
type Export struct {
	Lines int
	// Runs are the stretches of lines whose sequence numbers follow on, each as its first and last number.
	// A whole chain is one run; a filtered export has one per stretch it kept.
	Runs   [][2]int64
	Anchor string // the first line's prev_hash: the hash of the receipt before the export
	Head   string // the last line's hash
}

// ExportCheck checks the lines of an export of derbent receipts --json one at a time, in the order they
// were exported, without the database (ADR 0017). The zero value is ready to use.
type ExportCheck struct {
	res  Export
	last Row
}

// Add checks the next line of the export and returns why it fails, or nil. Its hash must match its
// fields, its sequence number must be greater than the line before it, and when the two follow on its
// prev_hash must be that line's hash; receipt 1 must follow the genesis hash. A gap in the numbers starts
// a new run and is not a failure. A line that fails is not added.
func (c *ExportCheck) Add(r Row) error {
	switch {
	case r.Sum() != r.Hash:
		return errors.New("its hash does not match its fields")
	case r.Seq < 1:
		return fmt.Errorf("its sequence number %d is not one a receipt can have", r.Seq)
	case r.Seq == 1 && r.PrevHash != Genesis:
		return errors.New("it is receipt 1, and its prev_hash is not the genesis hash")
	case c.res.Lines > 0 && r.Seq <= c.last.Seq:
		return fmt.Errorf("its sequence number %d does not come after %d, the line before it", r.Seq, c.last.Seq)
	case c.res.Lines > 0 && r.Seq == c.last.Seq+1 && r.PrevHash != c.last.Hash:
		return errors.New("its prev_hash is not the hash of the line before it")
	}
	switch {
	case c.res.Lines == 0:
		c.res.Anchor = r.PrevHash
		c.res.Runs = append(c.res.Runs, [2]int64{r.Seq, r.Seq})
	case r.Seq == c.last.Seq+1:
		c.res.Runs[len(c.res.Runs)-1][1] = r.Seq
	default:
		c.res.Runs = append(c.res.Runs, [2]int64{r.Seq, r.Seq})
	}
	c.res.Lines++
	c.last, c.res.Head = r, r.Hash
	return nil
}

// Result is what the lines added so far show.
func (c *ExportCheck) Result() Export {
	return c.res
}
