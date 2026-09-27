package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCreatesDirectoryAndSchema(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "dir with space", "nested", "p.db"))
	for _, name := range []string{"receipts", "memories", "memories_fts", "approvals", "grants"} {
		var n int
		err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n)
		if err != nil || n != 1 {
			t.Fatalf("table %s: n=%d err=%v", name, n, err)
		}
	}
	var mode string
	if err := db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, err %v", mode, err)
	}
}

// A path is a path, not a URI: # and % must not cut it short or be decoded.
func TestOpenKeepsPathCharactersThatURIsTreatSpecially(t *testing.T) {
	for _, dir := range []string{"a#b", "c%41d", "e?f=g"} {
		if runtime.GOOS == "windows" && strings.Contains(dir, "?") {
			continue // not a legal file name character on Windows
		}
		path := filepath.Join(t.TempDir(), dir, "p.db")
		open(t, path)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: database not at %s: %v", dir, path, err)
		}
	}
}

func TestOpenTwiceKeepsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	first, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	db := open(t, path)
	var v int
	if err := db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version = %d, err %v", v, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := store.Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err = %v, want a newer-schema error", err)
	}
}

// Deciding an approval writes, but a mistyped path must fail and name itself rather than create an
// empty database where nothing is ever pending.
func TestOpenExistingWritableNeedsTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	path := filepath.Join(dir, "p.db")
	if db, err := store.OpenExistingWritable(t.Context(), path); err == nil || !strings.Contains(err.Error(), path) {
		if db != nil {
			db.Close()
		}
		t.Fatalf("err = %v, want an error naming %s", err, path)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenExistingWritable created %s", dir)
	}
}

func TestOpenExistingWritableMigratesAndWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil { // an empty file is an empty SQLite database
		t.Fatal(err)
	}
	db, err := store.OpenExistingWritable(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(t.Context(), `INSERT INTO approvals
		(created_ms, deadline_ms, project, agent, session, tool, args, rule) VALUES (1, 2, 'p', 'a', 's', 't', '{}', 1)`); err != nil {
		t.Fatalf("write after opening: %v", err)
	}
}

func TestImmediateRollsBackOnError(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "p.db"))
	boom := errors.New("boom")
	err := store.Immediate(t.Context(), db, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO memories (project, author, session, title, body, tags, at)
			VALUES ('p', 'a', 's', 't', 'b', '', 'now')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM memories`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = %d, err %v, want the insert rolled back", n, err)
	}
}

// Every hook call opens the database in a new process. Opening a current database must not wait for
// the write lock another gate holds, or a hook call could stall past its CLI's timeout.
func TestOpenACurrentDatabaseDoesNotWaitForTheWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	holder := open(t, path)
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.WithoutCancel(t.Context()), "ROLLBACK") //nolint:errcheck // cleanup
	began := time.Now()
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("Open waited %s for a lock it did not need", took)
	}
}
