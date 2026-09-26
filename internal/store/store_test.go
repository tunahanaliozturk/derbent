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

	"github.com/tunahanaliozturk/portcullis/internal/store"
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
	for _, name := range []string{"receipts", "memories", "memories_fts"} {
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
	if err := db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&v); err != nil || v != 1 {
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
