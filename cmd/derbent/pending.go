package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// pendingLine is one waiting call as `derbent pending --json` prints it.
type pendingLine struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	Agent       string `json:"agent"`
	Session     string `json:"session"`
	Tool        string `json:"tool"`
	Rule        int    `json:"rule"`
	ProjectRule bool   `json:"project_rule"`
	Args        string `json:"args"`
	Created     string `json:"created"`
	Deadline    string `json:"deadline"`
}

// runPending lists the calls waiting for the user, oldest first, each with the rule that asked, its
// project and its whole arguments, so they can be read before an approve or deny from a shell. It opens
// the database read-only.
func runPending(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pending", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	asJSON := flags.Bool("json", false, "print JSON lines instead of rows")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("pending: unexpected argument %q", flags.Arg(0))
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
	list, err := approval.NewQueue(db).Pending(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, p := range list {
		if *asJSON {
			var line []byte
			if line, err = json.Marshal(pendingLine{
				ID: p.ID, Project: p.Project, Agent: p.Agent, Session: p.Session, Tool: p.Tool, Rule: p.Rule, ProjectRule: p.ProjectRule, Args: p.Args,
				Created: p.Created.Format(time.RFC3339Nano), Deadline: p.Deadline.Format(time.RFC3339Nano),
			}); err != nil {
				return err
			}
			if _, err = fmt.Fprintln(stdout, escapeRaw(string(line))); err != nil {
				return err
			}
			continue
		}
		left := max(p.Deadline.Sub(now), 0).Round(time.Second)
		if _, err = fmt.Fprintf(stdout, "#%d  %s  %s  %s left  %s  %s\n    %s\n", p.ID, visible.Escape(p.Agent), visible.Escape(p.Tool),
			left, approval.RuleName(p.Rule, p.ProjectRule), visible.Escape(p.Project), visible.Escape(p.Args)); err != nil {
			return err
		}
	}
	return nil
}
