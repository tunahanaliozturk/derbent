package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/suggest"
)

// suggestionLine is one suggestion as derbent suggest --json prints it.
type suggestionLine struct {
	Agent    string `json:"agent"`
	Tool     string `json:"tool"`
	Action   string `json:"action"`
	Arg      string `json:"arg,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Approved int    `json:"approved"`
	Denied   int    `json:"denied"`
	Rules    []int  `json:"rules"`
	TOML     string `json:"toml"`
}

// runSuggest prints the rules the user's answers point to, as TOML snippets with the counts behind them
// and where each goes, or as JSON lines (ADR 0019). It changes no file, and opens the database read-only.
func runSuggest(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("suggest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	least := flags.Int("min", suggest.Min, "how many approvals, or denials, of one agent's calls to one tool make a suggestion")
	asJSON := flags.Bool("json", false, "print JSON lines instead of TOML")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch {
	case flags.NArg() > 0:
		return fmt.Errorf("suggest: unexpected argument %q", flags.Arg(0))
	case *least < 1:
		return fmt.Errorf("suggest: --min must be at least 1, got %d", *least)
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
	calls, err := approval.NewQueue(db).Answered(ctx, "", "")
	if err != nil {
		return err
	}
	list := suggest.From(calls, *least)
	if *asJSON {
		for _, s := range list {
			var line []byte
			if line, err = json.Marshal(suggestionLine{
				Agent: s.Agent, Tool: s.Tool, Action: string(s.Action), Arg: s.Key, Prefix: s.Prefix,
				Approved: s.Approved, Denied: s.Denied, Rules: s.Rules, TOML: s.TOML(),
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
		_, err = fmt.Fprintf(stdout, "no suggestions: no agent's calls to one tool were approved %d %s with no denial, or denied %d %s with no approval\n",
			*least, plural(*least, "time", "times"), *least, plural(*least, "time", "times"))
		return err
	}
	var b strings.Builder
	b.WriteString("# Rules your answers point to. Derbent changes no file: copy the ones you want into your config.\n")
	for _, s := range list {
		b.WriteString("\n")
		b.WriteString(s.TOML())
	}
	_, err = io.WriteString(stdout, b.String())
	return err
}
