// Package receipt appends a hash-chained receipt for every call through the gate and verifies the
// chain afterwards.
package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/tunahanaliozturk/portcullis/internal/store"
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
		r.Hash = w.digest()
		_, err = conn.ExecContext(ctx, `INSERT INTO receipts (seq, at, project, agent, session, tool, args,
			args_sha256, decision, decided_by, outcome, result_size, result_sha256, duration_ms, prev_hash, hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.seq, w.at, w.project, w.agent, w.session, w.tool, w.args, w.argsSHA256, w.decision, w.decidedBy,
			w.outcome, w.resultSize, w.resultSHA256, w.durationMS, w.prevHash, r.Hash)
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
	rows, err := l.db.QueryContext(ctx, `SELECT seq, at, project, agent, session, tool, args, args_sha256,
		decision, decided_by, outcome, result_size, result_sha256, duration_ms, prev_hash, hash
		FROM receipts ORDER BY seq`)
	if err != nil {
		return Result{}, fmt.Errorf("read receipts: %w", err)
	}
	defer rows.Close()
	res := Result{Head: Genesis}
	want, prev := int64(1), Genesis
	for rows.Next() {
		var w row
		var stored string
		if err = rows.Scan(&w.seq, &w.at, &w.project, &w.agent, &w.session, &w.tool, &w.args, &w.argsSHA256,
			&w.decision, &w.decidedBy, &w.outcome, &w.resultSize, &w.resultSHA256, &w.durationMS,
			&w.prevHash, &stored); err != nil {
			return Result{}, fmt.Errorf("read receipt: %w", err)
		}
		res.Count++
		if res.FirstBad == 0 {
			switch {
			case w.seq != want:
				res.FirstBad, res.Reason = w.seq, fmt.Sprintf("expected sequence number %d", want)
			case w.prevHash != prev:
				res.FirstBad, res.Reason = w.seq, "previous hash does not match the receipt before it"
			case w.digest() != stored:
				res.FirstBad, res.Reason = w.seq, "hash does not match the receipt's contents"
			}
		}
		want, prev, res.Head = w.seq+1, stored, stored
	}
	if err = rows.Err(); err != nil {
		return Result{}, fmt.Errorf("read receipts: %w", err)
	}
	return res, nil
}

// row is a receipt exactly as stored. Hashes are computed over these values, so verification never
// depends on how a time or a duration is formatted when read back.
type row struct {
	seq          int64
	at           string
	project      string
	agent        string
	session      string
	tool         string
	args         string
	argsSHA256   string
	decision     string
	decidedBy    string
	outcome      string
	resultSize   int64
	resultSHA256 string
	durationMS   int64
	prevHash     string
}

func rowOf(r Receipt) row {
	return row{
		seq: r.Seq, at: r.At.Format(time.RFC3339Nano), project: r.Project, agent: r.Agent, session: r.Session,
		tool: r.Tool, args: r.Args, argsSHA256: r.ArgsSHA256, decision: r.Decision, decidedBy: r.DecidedBy,
		outcome: r.Outcome, resultSize: r.ResultSize, resultSHA256: r.ResultSHA256,
		durationMS: r.Duration.Milliseconds(), prevHash: r.PrevHash,
	}
}

// digest hashes the fields in a fixed order, each prefixed with its length, so that two different
// rows can never feed the hash the same bytes.
func (w row) digest() string {
	h := sha256.New()
	for _, f := range [...]string{
		strconv.FormatInt(w.seq, 10), w.at, w.project, w.agent, w.session, w.tool, w.args, w.argsSHA256,
		w.decision, w.decidedBy, w.outcome, strconv.FormatInt(w.resultSize, 10), w.resultSHA256,
		strconv.FormatInt(w.durationMS, 10), w.prevHash,
	} {
		_, _ = fmt.Fprintf(h, "%d:%s;", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))
}
