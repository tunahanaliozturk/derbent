// Package handoff keeps the tasks agents leave for each other, one set per project (ADR 0018). A handoff
// is addressed to an agent label or to anyone, is open until an agent it is addressed to takes it, and is
// done when that agent finishes it. Nothing moves it back.
package handoff

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// Limits on a handoff, the title in characters and the body and note in bytes. The title, body and tags
// follow a memory note's rules (memory.CheckNote and memory.CheckTags).
const (
	MaxTitle = memory.MaxTitle
	MaxBody  = memory.MaxBody
	MaxNote  = 4 << 10
)

// Anyone is the address of a handoff that any agent may take.
const Anyone = "*"

// State is where a handoff is: open, then taken, then done.
type State string

// The states a handoff goes through, in order.
const (
	Open  State = "open"
	Taken State = "taken"
	Done  State = "done"
)

var (
	// ErrNotFound marks an id no handoff has.
	ErrNotFound = errors.New("handoff not found")
	// ErrInvalid wraps every validation failure.
	ErrInvalid = errors.New("invalid handoff")
	// ErrRefused marks a take or a finish that the handoff's state or address does not allow.
	ErrRefused = errors.New("handoff refused")
)

// Handoff is a task one agent left for another.
type Handoff struct {
	ID           int64
	Project      string
	From         string // the agent that created it
	FromSession  string
	To           string // an agent label, or Anyone
	Title        string
	Body         string
	Tags         []string
	State        State
	TakenBy      string
	TakenSession string
	Note         string
	Created      time.Time
	Taken        time.Time // zero until it is taken
	Done         time.Time // zero until it is done
}

// Filter selects handoffs. Empty fields match everything.
type Filter struct {
	Project string // "" for every project
	// Agent, when set, keeps the handoffs addressed to it or to Anyone, or with Mine the ones it created.
	Agent string
	Mine  bool
	State State // "" for every state
}

// Store reads and writes handoffs.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db, which must have been opened with store.Open, or with
// store.OpenExisting for List.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

const columns = `id, project, from_agent, from_session, to_agent, title, body, tags, state, taken_by, taken_session,
	note, created_ms, taken_ms, done_ms`

func scan(row interface{ Scan(dest ...any) error }) (Handoff, error) {
	var h Handoff
	var tags, state string
	var created, taken, done int64
	if err := row.Scan(&h.ID, &h.Project, &h.From, &h.FromSession, &h.To, &h.Title, &h.Body, &tags, &state,
		&h.TakenBy, &h.TakenSession, &h.Note, &created, &taken, &done); err != nil {
		return Handoff{}, err
	}
	h.Tags, h.State, h.Created = strings.Fields(tags), State(state), time.UnixMilli(created)
	if taken != 0 {
		h.Taken = time.UnixMilli(taken)
	}
	if done != 0 {
		h.Done = time.UnixMilli(done)
	}
	return h, nil
}

// Create stores h as an open handoff and returns its id. It takes Project, From, FromSession, To, Title,
// Body and Tags from h, and sets the rest.
func (s *Store) Create(ctx context.Context, h Handoff) (int64, error) {
	h.Title = strings.TrimSpace(h.Title)
	if err := validate(h); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO handoffs (project, from_agent, from_session, to_agent, title, body, tags, created_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.Project, h.From, h.FromSession, h.To, h.Title, h.Body, strings.Join(h.Tags, " "), s.now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("create handoff: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create handoff: %w", err)
	}
	return id, nil
}

// validate checks a new handoff: its title, body and tags as a memory note's, and To, which must be an
// agent label, the form --agent takes, or Anyone, so a handoff never waits for an agent that cannot exist.
func validate(h Handoff) error {
	err := memory.CheckNote(h.Project, h.From, h.Title, h.Body)
	if err == nil {
		err = memory.CheckTags(h.Tags)
	}
	if err == nil && h.To != Anyone && !config.AgentLabel.MatchString(h.To) {
		err = fmt.Errorf("to must be an agent label, 1 to 32 lower-case letters, digits, dashes or underscores, or * for any agent; got %q", h.To)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// List returns the handoffs f selects, newest first. A database from before migration 0007, which a
// command that only reads opens without migrating, holds none.
func (s *Store) List(ctx context.Context, f Filter) ([]Handoff, error) {
	if ok, err := s.hasTable(ctx); err != nil || !ok {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM handoffs
		WHERE (?1 = '' OR project = ?1)
			AND (?2 = '' OR (?3 AND from_agent = ?2) OR (NOT ?3 AND to_agent IN (?2, '*')))
			AND (?4 = '' OR state = ?4)
		ORDER BY id DESC`, f.Project, f.Agent, f.Mine, string(f.State))
	if err != nil {
		return nil, fmt.Errorf("read handoffs: %w", err)
	}
	defer rows.Close()
	var out []Handoff
	for rows.Next() {
		var h Handoff
		if h, err = scan(rows); err != nil {
			return nil, fmt.Errorf("read handoff: %w", err)
		}
		out = append(out, h)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read handoffs: %w", err)
	}
	return out, nil
}

// Take gives the open handoff id to agent, from its gate session, and returns it whole. Only a handoff
// addressed to agent or to Anyone can be taken, and only while it is open. The read and the write are one
// transaction, so of two agents taking a handoff at once one gets it and the other ErrRefused. A handoff of
// any project can be taken by its id, as a note of any project can be read by its id.
func (s *Store) Take(ctx context.Context, id int64, agent, session string) (Handoff, error) {
	var taken Handoff
	err := store.Immediate(ctx, s.db, func(ctx context.Context, conn *sql.Conn) error {
		h, err := get(ctx, conn, id)
		switch {
		case err != nil:
			return err
		case h.To != Anyone && h.To != agent:
			return fmt.Errorf("%w: handoff %d is addressed to %s, not to %s", ErrRefused, id, h.To, agent)
		case h.State != Open:
			return fmt.Errorf("%w: handoff %d is %s by %s, not open", ErrRefused, id, h.State, h.TakenBy)
		}
		now := s.now().UnixMilli()
		if _, err = conn.ExecContext(ctx, `UPDATE handoffs SET state = 'taken', taken_by = ?, taken_session = ?, taken_ms = ? WHERE id = ?`,
			agent, session, now, id); err != nil {
			return fmt.Errorf("take handoff %d: %w", id, err)
		}
		h.State, h.TakenBy, h.TakenSession, h.Taken = Taken, agent, session, time.UnixMilli(now)
		taken = h
		return nil
	})
	return taken, err
}

// Finish marks the handoff id done with note, when agent is the agent that took it, and returns it. The
// note is optional and holds up to MaxNote bytes. Another gate session of the same agent may finish it.
func (s *Store) Finish(ctx context.Context, id int64, agent, note string) (Handoff, error) {
	if len(note) > MaxNote {
		return Handoff{}, fmt.Errorf("%w: the note is larger than %d bytes", ErrInvalid, MaxNote)
	}
	var done Handoff
	err := store.Immediate(ctx, s.db, func(ctx context.Context, conn *sql.Conn) error {
		h, err := get(ctx, conn, id)
		switch {
		case err != nil:
			return err
		case h.State == Done:
			return fmt.Errorf("%w: handoff %d is already done", ErrRefused, id)
		case h.State != Taken || h.TakenBy != agent:
			return fmt.Errorf("%w: handoff %d was not taken by %s, and only the agent that took it can finish it", ErrRefused, id, agent)
		}
		now := s.now().UnixMilli()
		if _, err = conn.ExecContext(ctx, `UPDATE handoffs SET state = 'done', note = ?, done_ms = ? WHERE id = ?`, note, now, id); err != nil {
			return fmt.Errorf("finish handoff %d: %w", id, err)
		}
		h.State, h.Note, h.Done = Done, note, time.UnixMilli(now)
		done = h
		return nil
	})
	return done, err
}

// get reads handoff id on conn, inside the transaction that will change it.
func get(ctx context.Context, conn *sql.Conn, id int64) (Handoff, error) {
	h, err := scan(conn.QueryRowContext(ctx, `SELECT `+columns+` FROM handoffs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Handoff{}, fmt.Errorf("handoff %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Handoff{}, fmt.Errorf("read handoff %d: %w", id, err)
	}
	return h, nil
}

// hasTable reports whether the database has the handoffs table. One from before migration 0007 that no
// gate has migrated yet, opened read-only, has none and so holds no handoffs.
func (s *Store) hasTable(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'handoffs'`).Scan(&n); err != nil {
		return false, fmt.Errorf("read handoffs: %w", err)
	}
	return n > 0, nil
}
