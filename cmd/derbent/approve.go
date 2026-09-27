package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// runDecide approves or denies one pending approval, as the UI's a, A and d keys do.
func runDecide(ctx context.Context, command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbFlag := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	session := false
	if command == "approve" {
		flags.BoolVar(&session, "session", false, "approve this tool for the rest of the agent's session")
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
	verdict, done := approval.Deny, "denied"
	switch {
	case command == "approve" && session:
		verdict, done = approval.ApproveSession, "approved for the rest of the session:"
	case command == "approve":
		verdict, done = approval.ApproveOnce, "approved"
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
	if err = approval.NewQueue(db).Decide(ctx, id, verdict); err != nil {
		if errors.Is(err, approval.ErrNotPending) {
			return fmt.Errorf("approval %d is not pending: it was already decided, timed out, or its agent gave up", id)
		}
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s %d\n", done, id)
	return err
}
