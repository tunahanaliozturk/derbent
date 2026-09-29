// Package memory keeps the notes agents write for each other, one set per project, searchable with
// SQLite full-text search.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

// Limits on a note. Titles are counted in characters, bodies in bytes.
const (
	MaxTitle = 200
	MaxBody  = 16 << 10
	MaxTags  = 10
)

var (
	// ErrNotFound is returned by Read for an id that does not exist.
	ErrNotFound = errors.New("memory entry not found")
	// ErrInvalid wraps every validation failure.
	ErrInvalid = errors.New("invalid memory request")
)

var tagPattern = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,40}$`)

// Entry is one note.
type Entry struct {
	ID           int64
	Project      string
	Author       string
	Session      string
	Title        string
	Body         string
	Tags         []string
	At           time.Time
	SupersededBy int64
}

// Hit is one search result.
type Hit struct {
	ID      int64
	Project string
	Title   string
	Snippet string
	Author  string
	At      time.Time
}

// Store reads and writes notes.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db, which must have been opened with store.Open.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// Write stores e and returns its id. When supersedes is not zero, that entry must be in the same
// project and not superseded yet; it is marked as superseded by the new entry in the same
// transaction and drops out of search results.
func (s *Store) Write(ctx context.Context, e Entry, supersedes int64) (int64, error) {
	e.Title = strings.TrimSpace(e.Title)
	if err := validate(e); err != nil {
		return 0, err
	}
	at := s.now().UTC().Format(time.RFC3339Nano)
	var id int64
	err := store.Immediate(ctx, s.db, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `INSERT INTO memories (project, author, session, title, body, tags, at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, e.Project, e.Author, e.Session, e.Title, e.Body, strings.Join(e.Tags, " "), at)
		if err != nil {
			return fmt.Errorf("insert entry: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("read entry id: %w", err)
		}
		if supersedes == 0 {
			return nil
		}
		res, err = conn.ExecContext(ctx, `UPDATE memories SET superseded_by = ?
			WHERE id = ? AND project = ? AND superseded_by IS NULL`, id, supersedes, e.Project)
		if err != nil {
			return fmt.Errorf("supersede entry %d: %w", supersedes, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("supersede entry %d: %w", supersedes, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: entry %d is not in this project or is already superseded", ErrInvalid, supersedes)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// Search finds entries whose title, body or tags contain any word of query, best matches first, in
// project or, with allProjects, everywhere. Superseded entries are left out. limit is clamped to
// 1..50 and defaults to 10.
func (s *Store) Search(ctx context.Context, project, query string, limit int, allProjects bool) ([]Hit, error) {
	match := matchExpr(query)
	if match == "" {
		return nil, fmt.Errorf("%w: the query has no words to search for", ErrInvalid)
	}
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 50)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.project, m.title, snippet(memories_fts, 1, '[', ']', '...', 12), m.author, m.at
		FROM memories_fts JOIN memories m ON m.id = memories_fts.rowid
		WHERE memories_fts MATCH ? AND m.superseded_by IS NULL AND (? OR m.project = ?)
		ORDER BY bm25(memories_fts)
		LIMIT ?`, match, allProjects, project, limit)
	if err != nil {
		return nil, fmt.Errorf("search memory: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		var at string
		if err = rows.Scan(&h.ID, &h.Project, &h.Title, &h.Snippet, &h.Author, &at); err != nil {
			return nil, fmt.Errorf("search memory: %w", err)
		}
		if h.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("search memory: entry %d: %w", h.ID, err)
		}
		hits = append(hits, h)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("search memory: %w", err)
	}
	return hits, nil
}

// Read returns the entry with id, whichever project it belongs to and whether or not it is superseded.
func (s *Store) Read(ctx context.Context, id int64) (Entry, error) {
	var e Entry
	var tags, at string
	var supersededBy sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, project, author, session, title, body, tags, at, superseded_by
		FROM memories WHERE id = ?`, id).
		Scan(&e.ID, &e.Project, &e.Author, &e.Session, &e.Title, &e.Body, &tags, &at, &supersededBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("read entry %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("read entry %d: %w", id, err)
	}
	if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return Entry{}, fmt.Errorf("read entry %d: %w", id, err)
	}
	e.Tags = strings.Fields(tags)
	e.SupersededBy = supersededBy.Int64
	return e, nil
}

func validate(e Entry) error {
	err := CheckNote(e.Project, e.Author, e.Title, e.Body)
	if err == nil {
		err = CheckTags(e.Tags)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// CheckNote reports why a note cannot be kept: no project or author, an empty title or one over MaxTitle
// characters, or a blank body or one over MaxBody bytes. The title is checked as given, so a caller trims
// it first. Handoffs are checked by the same rule.
func CheckNote(project, author, title, body string) error {
	switch {
	case project == "" || author == "":
		return errors.New("project and author are required")
	case title == "":
		return errors.New("the title is empty")
	case utf8.RuneCountInString(title) > MaxTitle:
		return fmt.Errorf("the title is longer than %d characters", MaxTitle)
	case strings.TrimSpace(body) == "":
		return errors.New("the body is empty")
	case len(body) > MaxBody:
		return fmt.Errorf("the body is larger than %d bytes", MaxBody)
	}
	return nil
}

// CheckTags reports why tags cannot be kept: more than MaxTags, or a tag that is not 1 to 40 letters,
// digits, dashes and underscores. Handoffs take tags by the same rule.
func CheckTags(tags []string) error {
	if len(tags) > MaxTags {
		return fmt.Errorf("more than %d tags", MaxTags)
	}
	for _, tag := range tags {
		if !tagPattern.MatchString(tag) {
			return fmt.Errorf("tag %q may hold only letters, digits, dash and underscore, up to 40 characters", tag)
		}
	}
	return nil
}

// matchExpr turns free text into an FTS5 expression that cannot be a syntax error: every run of
// letters and digits becomes a quoted term, and the terms are joined with OR.
func matchExpr(query string) string {
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " OR ")
}
