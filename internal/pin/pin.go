// Package pin keeps a fingerprint of each downstream tool's definition, taken the first time a gate sees
// the tool, so that a server changing a tool's description later is noticed before an agent reads it
// (ADR 0013).
package pin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

// State is how a tool's definition compares with its pin.
type State string

// The states a tool can be in.
const (
	New     State = "new"     // no gate has pinned the tool yet
	Pinned  State = "pinned"  // the definition is the pinned one
	Changed State = "changed" // a definition that differs from the pinned one was seen
)

var (
	// ErrNoPin marks a tool no gate has pinned.
	ErrNoPin = errors.New("no pin")
	// ErrNotChanged marks an accept of a tool whose server has sent nothing but the pinned definition.
	ErrNotChanged = errors.New("no change to accept")
	// ErrOtherChange marks an accept whose hash is not the recorded change's, such as a change that was
	// recorded after the user reviewed another.
	ErrOtherChange = errors.New("the change on record is not the one given")
)

// Definition returns the canonical form of t that a pin is taken over: a JSON object with its name,
// title, description, input schema, output schema and annotations, object keys sorted, no insignificant
// whitespace, numbers as written and no HTML escaping. Every key is present, null or empty when the
// server left it out, so the form does not depend on which fields the SDK omits. The annotations are
// written here field by field, since the SDK's own encoding of them changes with its version and with
// MCPGODEBUG=hintomitempty.
func Definition(t *mcp.Tool) (string, error) {
	var annotations any // null when the server sent none
	if a := t.Annotations; a != nil {
		annotations = map[string]any{
			"title": a.Title, "readOnlyHint": a.ReadOnlyHint, "idempotentHint": a.IdempotentHint,
			"destructiveHint": a.DestructiveHint, "openWorldHint": a.OpenWorldHint,
		}
	}
	raw, err := json.Marshal(map[string]any{
		"name": t.Name, "title": t.Title, "description": t.Description,
		"inputSchema": t.InputSchema, "outputSchema": t.OutputSchema, "annotations": annotations,
	})
	if err != nil {
		return "", fmt.Errorf("encode tool %s: %w", t.Name, err)
	}
	// A second pass through a generic value turns structs, such as the annotations, into objects, whose
	// keys encoding/json writes sorted.
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err = dec.Decode(&v); err != nil {
		return "", fmt.Errorf("encode tool %s: %w", t.Name, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(v); err != nil {
		return "", fmt.Errorf("encode tool %s: %w", t.Name, err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// Sum is the SHA-256 of a definition, in hex.
func Sum(def string) string {
	sum := sha256.Sum256([]byte(def))
	return hex.EncodeToString(sum[:])
}

// Pin is a tool's pin and, when its server has since sent something else, that definition.
type Pin struct {
	Server, Tool  string
	SHA256        string
	Definition    string
	PinnedAt      time.Time
	NewSHA256     string // "" when no differing definition is recorded
	NewDefinition string
	ChangedAt     time.Time // zero when no differing definition is recorded
}

// State is Changed while a differing definition is recorded, and Pinned otherwise.
func (p Pin) State() State {
	if p.NewSHA256 != "" {
		return Changed
	}
	return Pinned
}

// Store reads and writes the pins table.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db, which must have been opened with store.Open, or with
// store.OpenExisting for the methods that only read.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

const pinColumns = `SELECT server, tool, sha256, definition, pinned_ms, new_sha256, new_definition, changed_ms FROM pins`

func scanPin(row interface{ Scan(dest ...any) error }) (Pin, error) {
	var p Pin
	var pinned, changed int64
	if err := row.Scan(&p.Server, &p.Tool, &p.SHA256, &p.Definition, &pinned, &p.NewSHA256, &p.NewDefinition, &changed); err != nil {
		return Pin{}, err
	}
	p.PinnedAt = time.UnixMilli(pinned)
	if changed != 0 {
		p.ChangedAt = time.UnixMilli(changed)
	}
	return p, nil
}

// Check compares server's tools with their pins and returns the names of the tools whose definition
// differs from their pin. It pins a tool that has none, records a differing definition next to the pin,
// and drops a recorded change when the server sends the pinned definition again. Tools the server no
// longer lists keep their pins. One transaction covers the whole list, so gates that start at the same
// moment pin each tool once.
func (s *Store) Check(ctx context.Context, server string, tools []*mcp.Tool) (map[string]bool, error) {
	return s.check(ctx, server, tools, true)
}

// CheckWithheld is Check for tools a gate withheld because their pins could not be checked, as its
// watcher checks them again. It pins a tool that has none and reports changes as Check does, but, like
// Recheck, records a change only when none is recorded and never drops one, so the change the user is
// reviewing stays the change on record.
func (s *Store) CheckWithheld(ctx context.Context, server string, tools []*mcp.Tool) (map[string]bool, error) {
	return s.check(ctx, server, tools, false)
}

// check is Check, and with replace false CheckWithheld.
func (s *Store) check(ctx context.Context, server string, tools []*mcp.Tool, replace bool) (map[string]bool, error) {
	type seen struct{ name, def, sum string }
	list := make([]seen, 0, len(tools))
	for _, t := range tools {
		def, err := Definition(t)
		if err != nil {
			return nil, err
		}
		list = append(list, seen{t.Name, def, Sum(def)})
	}
	changed := map[string]bool{}
	err := store.Immediate(ctx, s.db, func(ctx context.Context, conn *sql.Conn) error {
		now := s.now().UnixMilli()
		for _, t := range list {
			p, err := scanPin(conn.QueryRowContext(ctx, pinColumns+` WHERE server = ? AND tool = ?`, server, t.name))
			switch {
			case errors.Is(err, sql.ErrNoRows):
				_, err = conn.ExecContext(ctx, `INSERT INTO pins (server, tool, sha256, definition, pinned_ms) VALUES (?, ?, ?, ?, ?)`,
					server, t.name, t.sum, t.def, now)
			case err != nil:
			case p.SHA256 == t.sum && p.NewSHA256 != "" && replace:
				_, err = conn.ExecContext(ctx, `UPDATE pins SET new_sha256 = '', new_definition = '', changed_ms = 0
					WHERE server = ? AND tool = ?`, server, t.name)
			case p.SHA256 == t.sum:
			default:
				changed[t.name] = true
				if p.NewSHA256 != t.sum && (replace || p.NewSHA256 == "") {
					_, err = conn.ExecContext(ctx, `UPDATE pins SET new_sha256 = ?, new_definition = ?, changed_ms = ?
						WHERE server = ? AND tool = ?`, t.sum, t.def, now, server, t.name)
				}
			}
			if err != nil {
				return fmt.Errorf("pin %s__%s: %w", server, t.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("check pins of %s: %w", server, err)
	}
	return changed, nil
}

// Recheck compares t, which a gate withholds because it differed from its pin, with the pin again and
// reports whether it still differs. It writes only when it does and no change is recorded, which is
// after the user accepted another definition: it then records t's. It never replaces a recorded change,
// so gates that hold different definitions of the tool do not overwrite each other's, and the change
// the user reviews stays the change on record.
func (s *Store) Recheck(ctx context.Context, server string, t *mcp.Tool) (bool, error) {
	def, err := Definition(t)
	if err != nil {
		return false, err
	}
	sum := Sum(def)
	p, err := s.Get(ctx, server, t.Name)
	switch {
	case err != nil:
		return false, err
	case p.SHA256 == sum:
		return false, nil
	case p.NewSHA256 != "":
		return true, nil
	}
	// The conditions make this a no-op when another gate recorded a change or the pin became t's since
	// the read above.
	if _, err = s.db.ExecContext(ctx, `UPDATE pins SET new_sha256 = ?, new_definition = ?, changed_ms = ?
		WHERE server = ? AND tool = ? AND new_sha256 = '' AND sha256 <> ?`, sum, def, s.now().UnixMilli(), server, t.Name, sum); err != nil {
		return false, fmt.Errorf("record change of %s__%s: %w", server, t.Name, err)
	}
	return true, nil
}

// hasTable reports whether the database has the pins table. One from before migration 0005 that no
// gate has migrated yet, opened read-only, has none and so holds no pins.
func (s *Store) hasTable(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'pins'`).Scan(&n); err != nil {
		return false, fmt.Errorf("read pins: %w", err)
	}
	return n > 0, nil
}

// List returns every pin, by server and tool.
func (s *Store) List(ctx context.Context) ([]Pin, error) {
	if ok, err := s.hasTable(ctx); err != nil || !ok {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, pinColumns+` ORDER BY server, tool`)
	if err != nil {
		return nil, fmt.Errorf("read pins: %w", err)
	}
	defer rows.Close()
	var out []Pin
	for rows.Next() {
		var p Pin
		if p, err = scanPin(rows); err != nil {
			return nil, fmt.Errorf("read pin: %w", err)
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read pins: %w", err)
	}
	return out, nil
}

// Get returns the pin of server's tool, or ErrNoPin, which a database without the pins table gives too.
func (s *Store) Get(ctx context.Context, server, tool string) (Pin, error) {
	ok, err := s.hasTable(ctx)
	if err != nil {
		return Pin{}, err
	}
	if !ok {
		return Pin{}, fmt.Errorf("%s__%s: %w", server, tool, ErrNoPin)
	}
	p, err := scanPin(s.db.QueryRowContext(ctx, pinColumns+` WHERE server = ? AND tool = ?`, server, tool))
	if errors.Is(err, sql.ErrNoRows) {
		return Pin{}, fmt.Errorf("%s__%s: %w", server, tool, ErrNoPin)
	}
	if err != nil {
		return Pin{}, fmt.Errorf("read pin %s__%s: %w", server, tool, err)
	}
	return p, nil
}

// State reports how t, as server sends it now, compares with its pin. It writes nothing.
func (s *Store) State(ctx context.Context, server string, t *mcp.Tool) (State, error) {
	def, err := Definition(t)
	if err != nil {
		return "", err
	}
	p, err := s.Get(ctx, server, t.Name)
	switch {
	case errors.Is(err, ErrNoPin):
		return New, nil
	case err != nil:
		return "", err
	case p.SHA256 == Sum(def):
		return Pinned, nil
	}
	return Changed, nil
}

// Accept makes the change recorded for server's tool its pin, when that change's hash is given, and
// returns the pin as it now is. given is the whole hash as pins show printed it, in lower-case hex;
// binding the accept to it means a change recorded after the user reviewed another is never accepted
// unseen. A prefix is never enough: a hostile server controls every definition of its tool, so it can
// find two whose hashes share a short prefix, show one for review and then send the other. A tool with
// no recorded change gives ErrNotChanged, one with no pin ErrNoPin, and any other hash ErrOtherChange.
func (s *Store) Accept(ctx context.Context, server, tool, given string) (Pin, error) {
	var accepted Pin
	err := store.Immediate(ctx, s.db, func(ctx context.Context, conn *sql.Conn) error {
		p, err := scanPin(conn.QueryRowContext(ctx, pinColumns+` WHERE server = ? AND tool = ?`, server, tool))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%s__%s: %w", server, tool, ErrNoPin)
		case err != nil:
			return fmt.Errorf("read pin %s__%s: %w", server, tool, err)
		case p.NewSHA256 == "":
			return fmt.Errorf("%s__%s: %w", server, tool, ErrNotChanged)
		case given != p.NewSHA256:
			return fmt.Errorf("%s__%s: %w", server, tool, ErrOtherChange)
		}
		now := s.now().UnixMilli()
		if _, err = conn.ExecContext(ctx, `UPDATE pins SET sha256 = new_sha256, definition = new_definition, pinned_ms = ?,
			new_sha256 = '', new_definition = '', changed_ms = 0 WHERE server = ? AND tool = ?`, now, server, tool); err != nil {
			return fmt.Errorf("accept pin %s__%s: %w", server, tool, err)
		}
		accepted = Pin{Server: server, Tool: tool, SHA256: p.NewSHA256, Definition: p.NewDefinition, PinnedAt: time.UnixMilli(now)}
		return nil
	})
	return accepted, err
}

// CountChanged counts the tools with a recorded change.
func (s *Store) CountChanged(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pins WHERE new_sha256 <> ''`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count changed pins: %w", err)
	}
	return n, nil
}
