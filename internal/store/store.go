// Package store opens the SQLite database that every gate process and the UI share, and keeps its
// schema current.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite" // also registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens the database at path, creating it and its directory if needed, and applies any
// migrations it has not seen. The per-connection pragmas are set in the DSN so that every pooled
// connection gets them. Another program's SQLite file, or a schema newer than this binary knows, is
// refused before the WAL pragma or a migration writes to it. Opening a current database takes no write
// lock.
//
// A file with a -wal or -journal beside it is checked on a read-only open first, as OpenExisting checks
// it: a read-write connection would fold that log into another program's file (a checkpoint when it
// closes, or the rollback of a hot journal) before the check could refuse it. Any other file is checked
// on the read-write connection, which reads it and writes nothing until the check has passed. That
// matters for the hook, which opens the database afresh on every call: a read-only open of a WAL
// database whose last writer has closed retries with short sleeps, which cost 10 ms of a 17 ms hook call
// on Windows.
// ponytail: a program that creates a -wal or -journal between the Stat and the open is not probed; that
// takes a --db pointed at another program's file while that program writes it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if hasLog(path) {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			// SQLite would replay the log into a new file, so refuse and say which files to remove.
			return nil, fmt.Errorf("open database %s: it is missing but its -wal or -journal file is still there; "+
				"remove %s-wal, %s-shm and %s-journal to start a new database", path, path, path, path)
		}
		probe, err := OpenExisting(ctx, path)
		if err != nil {
			return nil, err
		}
		if err = probe.Close(); err != nil {
			return nil, fmt.Errorf("open database %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	// journal_mode is not in the DSN: set there, it would switch another program's file to WAL as the
	// connection opens, before checkSchema could refuse it. WAL is kept in the file once set, so every
	// later connection opens in it.
	dsn := fileURI(path) + "?_pragma=busy_timeout(30000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err = checkSchema(ctx, db, path); err != nil {
		db.Close()
		return nil, err
	}
	if err = useWAL(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err = migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return db, nil
}

// hasLog reports whether a -wal or -journal file sits beside the database at path.
func hasLog(path string) bool {
	for _, suffix := range []string{"-wal", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true
		}
	}
	return false
}

// walRetry is how long useWAL waits between tries while another connection holds the lock it needs.
const walRetry = 10 * time.Millisecond

// useWAL switches the database to write-ahead logging unless it is in it already, which it is after its
// first open, so a current database is only read here. Switching needs an exclusive lock and SQLite
// does not wait for it under busy_timeout, so when several processes open a new database at once the
// switch is tried again every walRetry until it holds or ctx or walWait runs out.
func useWAL(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read journal mode: %w", err)
	}
	if strings.EqualFold(mode, "wal") {
		return nil
	}
	deadline := time.Now().Add(walWait)
	for {
		err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
		if err == nil {
			break
		}
		if !busy(err) || time.Now().After(deadline) {
			return fmt.Errorf("set journal mode: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("set journal mode: %w", errors.Join(err, ctx.Err()))
		case <-time.After(walRetry):
		}
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("set journal mode: SQLite kept %q instead of wal", mode)
	}
	return nil
}

// walWait bounds useWAL's retries, as busy_timeout bounds every other wait for a lock.
const walWait = 30 * time.Second

// sqliteBusy is SQLite's primary result code SQLITE_BUSY; extended codes keep it in their low byte.
const sqliteBusy = 5

// busy reports whether err is SQLite's "database is locked".
func busy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqliteBusy
}

// OpenExistingWritable opens the database at path as Open does, migrating it if needed, but never
// creates it: a path that does not exist is an error naming it, so a mistyped path fails instead of
// showing an empty database where nothing ever waits.
func OpenExistingWritable(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return Open(ctx, path)
}

// OpenExisting opens the database at path read-only. It never creates the file and never migrates it.
// SQLite opens the file itself read-only, so no statement can write it and closing never folds the
// write-ahead log into it: a copy kept as evidence stays byte for byte as it was, and so does its -wal
// file. SQLite may add its -shm index file beside them, and beside a copy that has no -wal an empty
// -wal file as well; both stay after the connection closes. A schema newer than this binary knows is
// refused, and so is a file with tables but no Derbent schema version, which is another program's.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	return openReadOnly(ctx, path, "mode=ro&_pragma=busy_timeout(5000)")
}

// Check reports whether Open would take the existing database at path, making the checks OpenExisting
// makes, and leaves no trace. A database with a -wal file beside it is live, or was left so: it is
// opened read-only as OpenExisting opens it, which reads the -wal, so it sees what a writer has not yet
// checkpointed, and uses the -shm already there. Any other database is opened immutable: SQLite takes
// no lock and adds no -shm or -wal file, and with no -wal the main file holds everything.
// ponytail: a last writer that closes between the -wal check and the open removes the -wal and -shm,
// which the read-only open then makes again; doctor runs by hand, so that window is left open.
func Check(ctx context.Context, path string) error {
	params := "mode=ro&immutable=1"
	if _, err := os.Stat(path + "-wal"); err == nil {
		params = "mode=ro&_pragma=busy_timeout(5000)"
	}
	db, err := openReadOnly(ctx, path, params)
	if err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return fmt.Errorf("close database %s: %w", path, err)
	}
	return nil
}

// openReadOnly opens the existing database at path with the URI parameters params, and refuses another
// program's file or a schema newer than this binary knows.
func openReadOnly(ctx context.Context, path, params string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", fileURI(path)+"?"+params)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err = checkSchema(ctx, db, path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// checkSchema refuses another program's file, one with tables but no Derbent schema version, or with a
// version but no receipts table holding the hash chain, and a schema newer than this binary knows. It
// only reads: an empty or new file passes.
func checkSchema(ctx context.Context, db *sql.DB, path string) error {
	var version, objects int
	if err := db.QueryRowContext(ctx, "SELECT (SELECT user_version FROM pragma_user_version), (SELECT count(*) FROM sqlite_master)").
		Scan(&version, &objects); err != nil {
		return fmt.Errorf("open database %s: read schema version: %w", path, err)
	}
	if version == 0 && objects > 0 {
		return fmt.Errorf("open database %s: it is not a Derbent database: it has tables but no Derbent schema version", path)
	}
	if version < 0 {
		return fmt.Errorf("open database %s: it is not a Derbent database: schema version %d", path, version)
	}
	if version > 0 {
		// Another program can use user_version too, so a version alone does not make the file Derbent's.
		var chain int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('receipts') WHERE name IN ('prev_hash', 'hash')").
			Scan(&chain); err != nil {
			return fmt.Errorf("open database %s: read schema: %w", path, err)
		}
		if chain != 2 {
			return fmt.Errorf("open database %s: it is not a Derbent database: schema version %d but no receipt chain", path, version)
		}
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	if version > len(names) {
		return fmt.Errorf("open database %s: schema version %d is newer than this binary knows (%d): upgrade derbent", path, version, len(names))
	}
	return nil
}

// uriEscaper escapes the characters that end or encode part of an SQLite file URI, so a directory
// named "a#b" or "c%41d" is opened as written.
var uriEscaper = strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F")

func fileURI(path string) string {
	return "file:" + uriEscaper.Replace(filepath.ToSlash(path))
}

// Immediate runs fn inside BEGIN IMMEDIATE on a single connection. The write lock is taken before fn
// reads anything, so a read-then-write in fn cannot interleave with a writer in another process. An
// error from fn rolls the transaction back and is returned as it is.
func Immediate(ctx context.Context, db *sql.DB, fn func(ctx context.Context, conn *sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err = fn(ctx, conn); err != nil {
		if _, rbErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current == len(names) {
		return nil // current already: no write lock needed, which matters for a hook call per tool call
	}
	return Immediate(ctx, db, func(ctx context.Context, conn *sql.Conn) error {
		var current int
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if current > len(names) {
			return fmt.Errorf("schema version %d is newer than this binary knows (%d): upgrade derbent", current, len(names))
		}
		if current == len(names) {
			return nil
		}
		for _, name := range names[current:] {
			script, err := migrations.ReadFile(name)
			if err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			}
			if _, err := conn.ExecContext(ctx, string(script)); err != nil {
				return fmt.Errorf("apply %s: %w", name, err)
			}
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(names))); err != nil {
			return fmt.Errorf("record schema version: %w", err)
		}
		return nil
	})
}
