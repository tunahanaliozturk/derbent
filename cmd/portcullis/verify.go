package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tunahanaliozturk/portcullis/internal/receipt"
)

var errChainBroken = errors.New("the receipt chain is broken")

// runVerify checks the whole receipt chain and prints the head hash, which is worth keeping somewhere
// else: it is what shows a chain that was cut short or rewritten as a whole.
func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: portcullis.db in the user state directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	db, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := receipt.NewLog(db).Verify(ctx)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(stdout, "receipts: %d\nhead:     %s\n", res.Count, res.Head); err != nil {
		return err
	}
	if res.FirstBad != 0 {
		return fmt.Errorf("%w at receipt %d: %s", errChainBroken, res.FirstBad, res.Reason)
	}
	_, err = fmt.Fprintln(stdout, "chain:    intact")
	return err
}
