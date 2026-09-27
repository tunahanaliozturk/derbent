package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// grantLine is one session grant as `derbent grants --json` prints it.
type grantLine struct {
	ID          int64  `json:"id"`
	Agent       string `json:"agent"`
	Session     string `json:"session"`
	Tool        string `json:"tool"`
	Rule        int    `json:"rule"`
	ProjectRule bool   `json:"project_rule"`
	Granted     string `json:"granted"`
}

// runGrants lists the session grants, oldest first, as rows or JSON lines. It opens the database
// read-only.
func runGrants(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("grants", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	asJSON := flags.Bool("json", false, "print JSON lines instead of rows")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("grants: unexpected argument %q", flags.Arg(0))
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
	list, err := approval.NewQueue(db).Grants(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		for _, g := range list {
			var line []byte
			if line, err = json.Marshal(grantLine{
				ID: g.ID, Agent: g.Agent, Session: g.Session, Tool: g.Tool, Rule: g.Rule, ProjectRule: g.ProjectRule,
				Granted: g.Granted.Format(time.RFC3339Nano),
			}); err != nil {
				return err
			}
			if _, err = fmt.Fprintln(stdout, escapeRaw(string(line))); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		_, err = fmt.Fprintln(stdout, "no session grants")
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGENT\tSESSION\tTOOL\tASKED BY\tGRANTED")
	for _, g := range list {
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\n", g.ID, visible.Escape(g.Agent), visible.Escape(g.Session),
			visible.Escape(g.Tool), approval.RuleName(g.Rule, g.ProjectRule), g.Granted.Local().Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

// runRevoke deletes one session grant, named by the approval that made it, or every grant, and says
// what it deleted. It never creates a database.
func runRevoke(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbFlag := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	all := flags.Bool("all", false, "revoke every session grant")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch {
	case flags.NArg() > 1 && strings.HasPrefix(flags.Arg(1), "-"):
		return fmt.Errorf("revoke: flags go before the id: move %s in front of %s", flags.Arg(1), flags.Arg(0))
	case *all && flags.NArg() > 0:
		return errors.New("revoke: give a grant id or --all, not both")
	case !*all && flags.NArg() != 1:
		return errors.New("revoke: give one grant id, as derbent grants shows it, or --all")
	}
	var id int64
	if !*all {
		// derbent grants shows an id as #12, and either form is accepted.
		parsed, parseErr := strconv.ParseInt(strings.TrimPrefix(flags.Arg(0), "#"), 10, 64)
		if parseErr != nil {
			return fmt.Errorf("revoke: %q is not a grant id", flags.Arg(0))
		}
		id = parsed
	}
	path, err := databasePath(*dbFlag)
	if err != nil {
		return err
	}
	db, err := store.OpenExistingWritable(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	q := approval.NewQueue(db)
	if *all {
		var n int64
		if n, err = q.RevokeAll(ctx); err != nil {
			return err
		}
		word := "grants"
		if n == 1 {
			word = "grant"
		}
		_, err = fmt.Fprintf(stdout, "revoked %d %s\n", n, word)
		return err
	}
	g, err := q.Revoke(ctx, id)
	if errors.Is(err, approval.ErrNoGrant) {
		return fmt.Errorf("revoke: no session grant #%d; derbent grants lists them", id)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "revoked #%d: %s for %s in session %s\n", g.ID, visible.Escape(g.Tool), visible.Escape(g.Agent),
		visible.Escape(g.Session))
	return err
}
