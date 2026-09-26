package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// receiptLine is one receipt as `derbent receipts --json` prints it.
type receiptLine struct {
	Seq          int64  `json:"seq"`
	At           string `json:"at"`
	Project      string `json:"project"`
	Agent        string `json:"agent"`
	Session      string `json:"session"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	Decision     string `json:"decision"`
	DecidedBy    string `json:"decided_by"`
	Outcome      string `json:"outcome"`
	ResultSize   int64  `json:"result_size"`
	ResultSHA256 string `json:"result_sha256"`
	DurationMS   int64  `json:"duration_ms"`
	Hash         string `json:"hash"`
}

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
	flags.IntVar(&f.Limit, "limit", 50, "at most this many, the newest")
	asJSON := flags.Bool("json", false, "print JSON lines instead of a table")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var err error
	if f.Since, err = parseSince(*since, time.Now()); err != nil {
		return err
	}
	path := *dbPath
	if path == "" {
		if path, err = config.DefaultDBPath(); err != nil {
			return err
		}
	}
	db, err := store.OpenExisting(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	list, err := receipt.NewLog(db).List(ctx, f)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		for _, r := range list {
			if err = enc.Encode(receiptLine{
				Seq: r.Seq, At: r.At.Format(time.RFC3339Nano), Project: r.Project, Agent: r.Agent, Session: r.Session,
				Tool: r.Tool, Args: r.Args, Decision: r.Decision, DecidedBy: r.DecidedBy, Outcome: r.Outcome,
				ResultSize: r.ResultSize, ResultSHA256: r.ResultSHA256, DurationMS: r.Duration.Milliseconds(), Hash: r.Hash,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEQ\tTIME\tAGENT\tTOOL\tDECISION\tBY\tOUTCOME\tMS")
	for _, r := range list {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", r.Seq, r.At.Local().Format("2006-01-02 15:04:05"),
			r.Agent, r.Tool, r.Decision, r.DecidedBy, r.Outcome, r.Duration.Milliseconds())
	}
	return w.Flush()
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
