package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
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
	for _, name := range []string{"receipts", "memories", "memories_fts", "approvals", "grants", "receipts_agent_at", "pins", "handoffs"} {
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

// journal_mode is set once, not in the DSN, so a connection the pool opens later must still be in WAL
// mode, and every connection must still get the DSN's pragmas.
func TestEveryPooledConnectionIsInWALWithForeignKeys(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "p.db"))
	var conns []*sql.Conn
	for range 3 {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	for i, conn := range conns {
		var mode string
		var fk int
		if err := conn.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Errorf("connection %d: journal_mode = %q, err %v", i, mode, err)
		}
		if err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Errorf("connection %d: foreign_keys = %d, err %v", i, fk, err)
		}
		conn.Close()
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
	if err := db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&v); err != nil || v != 7 {
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

// copyFiles copies each of names from dir to a new directory and returns it.
func copyFiles(t *testing.T, dir string, names ...string) string {
	t.Helper()
	to := t.TempDir()
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(to, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return to
}

// assertUnchanged fails when any of names in dir differs from what it held in before.
func assertUnchanged(t *testing.T, dir string, before map[string][]byte) {
	t.Helper()
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s changed (err %v): %d bytes before, %d after", name, err, len(want), len(got))
		}
	}
}

func snapshot(t *testing.T, dir string, names ...string) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		m[name] = b
	}
	return m
}

// Another program's WAL database copied while it was open has rows only in its -wal. A read-write
// connection would fold them into the file when it closes; Open must refuse the file and leave both as
// they were.
func TestOpenRefusesAnotherProgramsWALDatabaseWithoutCheckpointingIt(t *testing.T) {
	src := t.TempDir()
	other, err := sql.Open("sqlite", filepath.Join(src, "other.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA wal_autocheckpoint = 0`, `CREATE TABLE notes (body TEXT)`, `INSERT INTO notes VALUES ('kept in the wal')`} {
		if _, err = other.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	dir := copyFiles(t, src, "other.db", "other.db-wal")
	before := snapshot(t, dir, "other.db", "other.db-wal")
	db, err := store.Open(t.Context(), filepath.Join(dir, "other.db"))
	if err == nil || !strings.Contains(err.Error(), "not a Derbent database") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("err = %v, want the file refused as not a Derbent database", err)
	}
	assertUnchanged(t, dir, before)
}

// A -wal left behind after the database was deleted would be replayed into a new file, so Open refuses
// it and names the files to remove.
func TestOpenRefusesALeftoverLogWithoutItsDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "derbent.db")
	if err := os.WriteFile(path+"-wal", []byte("left behind"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "remove "+path+"-wal") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("err = %v, want the leftover -wal named", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %s: %v, want no database created", path, err)
	}
}

// A rollback-journal database copied in the middle of a write has a hot -journal. A read-write
// connection would roll it back into the file; Open must refuse the file and leave both as they were.
func TestOpenRefusesAnotherProgramsDatabaseWithAHotJournalWithoutRollingItBack(t *testing.T) {
	src := t.TempDir()
	other, err := sql.Open("sqlite", filepath.Join(src, "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, q := range []string{
		`PRAGMA cache_size = 1`, `CREATE TABLE notes (body TEXT)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 200) INSERT INTO notes SELECT hex(randomblob(500)) FROM n`,
		`BEGIN`, `UPDATE notes SET body = hex(randomblob(500))`, // spills pages to the file mid-transaction
	} {
		if _, err = conn.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer conn.ExecContext(context.WithoutCancel(t.Context()), "ROLLBACK") //nolint:errcheck // cleanup
	dir := copyFiles(t, src, "other.db", "other.db-journal")
	before := snapshot(t, dir, "other.db", "other.db-journal")
	db, err := store.Open(t.Context(), filepath.Join(dir, "other.db"))
	if err == nil {
		db.Close()
		t.Fatal("Open took a database with another program's hot journal")
	}
	assertUnchanged(t, dir, before)
}

// Parallel tool calls can fire several hooks while the MCP gate starts on a new install, so several
// processes open a new database at once. Every one must succeed.
func TestOpenANewDatabaseFromManyConnectionsAtOnce(t *testing.T) {
	for round := range 5 {
		path := filepath.Join(t.TempDir(), "p.db")
		const n = 8
		errs := make(chan error, n)
		for range n {
			go func() {
				db, err := store.Open(t.Context(), path)
				if err == nil {
					err = db.Close()
				}
				errs <- err
			}()
		}
		// Every result is read before failing, so no goroutine still uses the directory when TempDir's
		// cleanup removes it.
		var failed []error
		for range n {
			if err := <-errs; err != nil {
				failed = append(failed, err)
			}
		}
		if len(failed) > 0 {
			t.Fatalf("round %d: %d of %d opens failed, first: %v", round, len(failed), n, failed[0])
		}
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
