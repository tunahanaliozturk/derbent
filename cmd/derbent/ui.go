package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/tui"
)

// runUI runs the terminal UI until the user quits.
func runUI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("derbent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q: %w", flags.Arg(0), errUsage)
	}
	db, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	m := tui.New(ctx, approval.NewQueue(db), receipt.NewLog(db), memory.NewStore(db))
	// Bubble Tea sizes the screen from the terminal and starts at 0x0, drawing nothing, when stdout is
	// not one. A terminal's own size replaces this one.
	size := tea.WithWindowSize(100, 30)
	_, err = tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(stdin), tea.WithOutput(stdout), size).Run()
	switch {
	case errors.Is(err, tea.ErrInterrupted):
		return nil // a SIGINT reached the program: the user wants out, which is no error
	case errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil:
		return nil // the process was told to stop
	}
	return err
}
