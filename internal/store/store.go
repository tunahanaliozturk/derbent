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

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens the database at path, creating it and its directory if needed, and applies any
// migrations it has not seen. Pragmas are set in the DSN so that every pooled connection gets them.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := fileURI(path) +
		"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return db, nil
}

// OpenExisting opens the database at path for reading only. It never creates the file, never migrates
// it, and refuses to run a statement that writes, so a copy kept as evidence stays byte for byte as it
// was. A schema newer than this binary knows is refused.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", fileURI(path)+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: read schema version: %w", path, err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	if version > len(names) {
		db.Close()
		return nil, fmt.Errorf("open database %s: schema version %d is newer than this binary knows (%d): upgrade derbent", path, version, len(names))
	}
	return db, nil
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
