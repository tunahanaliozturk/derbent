package store_test

import (
	"bytes"
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
	for _, name := range []string{"receipts", "memories", "memories_fts", "approvals", "grants", "receipts_agent_at", "pins"} {
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
	if err := db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&v); err != nil || v != 6 {
		t.Fatalf("user_version = %d, err %v", v, err)
	}
}

// Grants are keyed on the rule that asked (ADR 0011). A database from before that keeps its approvals,
// each with no rule key, and loses its grants, which lasted one agent session anyway.
func TestOpenMigratesGrantsToRuleKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_approvals.sql"} {
		script, readErr := os.ReadFile(filepath.Join("migrations", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = old.ExecContext(t.Context(), string(script)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err = old.ExecContext(t.Context(), `INSERT INTO approvals
		(id, created_ms, deadline_ms, project, agent, session, tool, args, rule, state) VALUES (7, 1, 2, 'p', 'codex', 's1', 'native__Bash', '{}', 1, 'approved');
		INSERT INTO grants (agent, session, tool, approval_id) VALUES ('codex', 's1', 'native__Bash', 7);
		PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db := open(t, path)
	var key string
	if err = db.QueryRowContext(t.Context(), `SELECT rule_key FROM approvals WHERE id = 7`).Scan(&key); err != nil || key != "" {
		t.Fatalf("rule_key = %q, err %v; want the old approval kept with no rule key", key, err)
	}
	var n int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM grants`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("grants = %d, err %v; want the old grants dropped", n, err)
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO grants (agent, session, tool, rule_key, approval_id)
		VALUES ('codex', 's1', 'native__Bash', 'k1', 7), ('codex', 's1', 'native__Bash', 'k2', 7)`); err != nil {
		t.Fatalf("two rules' grants for one tool in one session: %v", err)
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

// A --db that names another program's SQLite file is a mistake. Open refuses it with the error
// OpenExisting gives, before the WAL pragma or a migration writes a byte of it.
func TestOpenRefusesAnotherProgramsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.db")
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.ExecContext(t.Context(), `CREATE TABLE notes (body TEXT)`)
	other.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "not a Derbent database") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("err = %v, want the file refused as not a Derbent database", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the other program's database changed: err %v", err)
	}
}

// Check refuses what Open refuses, and leaves a database as it was: the file byte for byte, and no -wal
// or -shm file beside one that had none, which OpenExisting's read-only open would leave.
func TestCheckRefusesWhatOpenRefusesAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Check(t.Context(), path); err != nil {
		t.Fatalf("a current database: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Check changed the database: err %v", err)
	}
	if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) != 1 {
		t.Fatalf("Check left files beside the database: %v, err %v", entries, readErr)
	}

	newer, err := store.Open(t.Context(), filepath.Join(dir, "newer.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = newer.ExecContext(t.Context(), `PRAGMA user_version = 99`)
	newer.Close()
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", filepath.Join(dir, "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.ExecContext(t.Context(), `CREATE TABLE notes (body TEXT)`)
	other.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a database, just some text"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"newer.db": "newer than this binary knows", "other.db": "not a Derbent database", "notes.txt": "not a database", "none.db": "none.db",
	} {
		if err = store.Check(t.Context(), filepath.Join(dir, name)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

// Check on a database another connection is writing and checkpointing, as a gate does while doctor
// runs, reports no error: it must not call a live database malformed, or the user may move it away.
func TestCheckPassesADatabaseBeingWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db := open(t, path)
	insert := func(ctx context.Context) error {
		_, err := db.ExecContext(ctx, `INSERT INTO memories (project, author, session, title, body, tags, at)
			VALUES ('p', 'a', 's', 't', hex(randomblob(1000)), '', 'now')`)
		return err
	}
	if err := insert(t.Context()); err != nil { // the -wal file exists from here on
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	written := make(chan error, 1)
	go func() {
		var err error
		for i := 1; err == nil && ctx.Err() == nil; i++ {
			if err = insert(ctx); err == nil && i%4 == 0 {
				_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
			}
		}
		if ctx.Err() != nil {
			err = nil
		}
		written <- err
	}()
	failed := 0
	for range 200 {
		if err := store.Check(t.Context(), path); err != nil {
			if failed++; failed <= 3 {
				t.Errorf("Check: %v", err)
			}
		}
	}
	stop()
	if err := <-written; err != nil {
		t.Fatalf("the writer failed: %v", err)
	}
	if failed > 0 {
		t.Errorf("%d of 200 checks failed on a database being written", failed)
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
