package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf16"

	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// runReceipts lists receipts, newest last, as a table or as JSON lines. It opens the database read-only.
func runReceipts(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("receipts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	var f receipt.Filter
	flags.StringVar(&f.Agent, "agent", "", "only this agent")
	flags.StringVar(&f.Tool, "tool", "", "only tools matching this glob, such as github__*")
	flags.StringVar(&f.Project, "project", "", "only this project")
	since := flags.String("since", "", "only receipts from this long ago (1h) or this time (RFC 3339) on")
	flags.IntVar(&f.Limit, "limit", 50, "at most this many, the newest; 0 for every receipt")
	asJSON := flags.Bool("json", false, "print JSON lines instead of a table")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("receipts: unexpected argument %q: filters are flags, such as --agent codex", flags.Arg(0))
	}
	switch {
	case f.Limit < 0:
		return errors.New("receipts: --limit must be 0, for every receipt, or more")
	case f.Limit == 0:
		f.Limit = -1 // every match, as receipt.Filter reads a negative limit
	}
	var err error
	if f.Since, err = parseSince(*since, time.Now()); err != nil {
		return err
	}
	path, err := databasePath(*dbPath)
	if err != nil {
		return err
	}
	db, err := store.OpenExisting(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	log := receipt.NewLog(db)
	if *asJSON {
		// Each line is a stored row, every field the hash covers as it is stored, so that derbent verify
		// --file can check it without the database (ADR 0017).
		var rows []receipt.Row
		if rows, err = log.Rows(ctx, f); err != nil {
			return err
		}
		for _, r := range rows {
			var line []byte
			if line, err = json.Marshal(r); err != nil {
				return err
			}
			if _, err = fmt.Fprintln(stdout, escapeRaw(string(line))); err != nil {
				return err
			}
		}
		return nil
	}
	list, err := log.List(ctx, f)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEQ\tTIME\tAGENT\tTOOL\tDECISION\tBY\tOUTCOME\tMS")
	for _, r := range list {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", r.Seq, r.At.Local().Format("2006-01-02 15:04:05"),
			visible.Escape(r.Agent), visible.Escape(r.Tool), visible.Escape(r.Decision), visible.Escape(r.DecidedBy),
			visible.Escape(r.Outcome), r.Duration.Milliseconds())
	}
	return w.Flush()
}

// escapeRaw writes \u escapes for the runes encoding/json leaves raw that visible.Unsafe says a
// terminal must not see raw: DEL, the C1 controls, bidirectional overrides, invisible format
// characters and variation selectors. In a JSON line they can only stand inside a string, where the
// escape decodes to the same rune; one above U+FFFF is written as a UTF-16 surrogate pair, as JSON
// requires.
func escapeRaw(line string) string {
	var b strings.Builder
	for _, r := range line {
		switch {
		case !visible.Unsafe(r):
			b.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// parseSince reads --since as a duration back from now or as an RFC 3339 time. Empty means no bound.
func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("receipts: --since %q is neither a duration such as 1h nor an RFC 3339 time", s)
	}
	return t, nil
}
