package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// runDecide approves or denies one pending approval, as the UI's a, A and d keys do.
func runDecide(ctx context.Context, command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbFlag := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	session := false
	if command == "approve" {
		flags.BoolVar(&session, "session", false, "approve this tool's calls that the same rule asks about under the same rules above it, for the rest of the agent's session")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 && strings.HasPrefix(flags.Arg(1), "-") {
		return fmt.Errorf("%s: flags go before the id: move %s in front of %s", command, flags.Arg(1), flags.Arg(0))
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%s: give one approval id, as the UI shows it", command)
	}
	// The UI shows an id as #12, and either form is accepted.
	id, err := strconv.ParseInt(strings.TrimPrefix(flags.Arg(0), "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("%s: %q is not an approval id", command, flags.Arg(0))
	}
	verdict := approval.Deny
	switch {
	case command == "approve" && session:
		verdict = approval.ApproveSession
	case command == "approve":
		verdict = approval.ApproveOnce
	}
	path, err := databasePath(*dbFlag)
	if err != nil {
		return err
	}
	// Deciding writes, but it must never create a database: a mistyped path would only ever say "not
	// pending".
	db, err := store.OpenExistingWritable(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	q := approval.NewQueue(db)
	notPending := fmt.Errorf("approval %d is not pending: it was already decided, timed out, or its agent gave up", id)
	// Say what was decided, from the approval's row: the tool and the agent, and for a session approval
	// the rule, since the grant covers the tool's calls that the rule which asked holds, under the same
	// rules above it, in one agent's session.
	list, err := q.Pending(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(p approval.Pending) bool { return p.ID == id })
	if i < 0 {
		return notPending
	}
	p := list[i]
	agent, tool := visible.Escape(p.Agent), visible.Escape(p.Tool)
	done := fmt.Sprintf("denied #%d: %s for %s", id, tool, agent)
	switch verdict {
	case approval.ApproveOnce:
		done = fmt.Sprintf("approved #%d once: %s for %s", id, tool, agent)
	case approval.ApproveSession:
		// An approval with no rule key grants nothing (ADR 0011): its rule matched on an argument it could not
		// read, or a gate from before grants followed the rule, still running after the upgrade, asked. There
		// is no question to confirm here, so refuse rather than approve once when the user asked for the session.
		if p.RuleKey == "" {
			return fmt.Errorf("%s: approval %d cannot be approved for the session: its rule could not read one of its arguments, "+
				"or the gate that asked predates rule-scoped grants; approve it once with derbent approve %d", command, id, id)
		}
		done = fmt.Sprintf("approved %s calls that rule %d asks about, for the rest of %s's session: %d", tool, p.Rule, agent, id)
	case approval.Deny:
	}
	if err = q.Decide(ctx, id, verdict); err != nil {
		if errors.Is(err, approval.ErrNotPending) {
			return notPending
		}
		return err
	}
	_, err = fmt.Fprintln(stdout, done)
	return err
}
