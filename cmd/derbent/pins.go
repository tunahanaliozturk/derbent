package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/pin"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// pinLine is one pin as `derbent pins --json` prints it.
type pinLine struct {
	Tool    string `json:"tool"` // <server>__<tool>
	State   string `json:"state"`
	Pinned  string `json:"pinned"`
	Changed string `json:"changed,omitempty"`
	SHA256  string `json:"sha256"`
}

// runPins lists the tool pins, shows one tool's change, or accepts it:
// derbent pins [--json], derbent pins show <server>__<tool>, derbent pins accept <server>__<tool>.
func runPins(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 && (args[0] == "show" || args[0] == "accept") {
		sub, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(strings.TrimSpace("pins "+sub), flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbFlag := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	asJSON := false
	if sub == "" {
		flags.BoolVar(&asJSON, "json", false, "print JSON lines instead of rows")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	path, err := databasePath(*dbFlag)
	if err != nil {
		return err
	}
	if sub == "" {
		if flags.NArg() > 0 {
			return fmt.Errorf("pins: unexpected argument %q: use derbent pins show or derbent pins accept", visible.Escape(flags.Arg(0)))
		}
		return listPins(ctx, path, asJSON, stdout)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("pins %s: give one tool as <server>__<tool>, as derbent pins lists it", sub)
	}
	server, tool, ok := strings.Cut(flags.Arg(0), "__")
	if !ok || server == "" || tool == "" {
		return fmt.Errorf("pins %s: %q is not <server>__<tool>", sub, visible.Escape(flags.Arg(0)))
	}
	if sub == "show" {
		return showPin(ctx, path, server, tool, stdout)
	}
	return acceptPin(ctx, path, server, tool, stdout)
}

// listPins prints every pin with its state, as rows or JSON lines. It opens the database read-only.
func listPins(ctx context.Context, path string, asJSON bool, stdout io.Writer) error {
	db, err := store.OpenExisting(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	list, err := pin.NewStore(db).List(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		for _, p := range list {
			l := pinLine{Tool: p.Server + "__" + p.Tool, State: string(p.State()), Pinned: p.PinnedAt.Format(time.RFC3339Nano), SHA256: p.SHA256}
			if !p.ChangedAt.IsZero() {
				l.Changed = p.ChangedAt.Format(time.RFC3339Nano)
			}
			var line []byte
			if line, err = json.Marshal(l); err != nil {
				return err
			}
			if _, err = fmt.Fprintln(stdout, escapeRaw(string(line))); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		_, err = fmt.Fprintln(stdout, "no pins")
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tSTATE\tPINNED\tCHANGED")
	for _, p := range list {
		changed := "-"
		if !p.ChangedAt.IsZero() {
			changed = p.ChangedAt.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", visible.Escape(p.Server+"__"+p.Tool), p.State(),
			p.PinnedAt.Local().Format("2006-01-02 15:04:05"), changed)
	}
	return w.Flush()
}

// showPin prints a tool's pinned definition and, when its server has since sent another, that one and
// the lines that differ. Definitions come from a server, so every line is escaped.
func showPin(ctx context.Context, path, server, tool string, stdout io.Writer) error {
	db, err := store.OpenExisting(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	name := visible.Escape(server + "__" + tool)
	p, err := pin.NewStore(db).Get(ctx, server, tool)
	if errors.Is(err, pin.ErrNoPin) {
		return fmt.Errorf("pins show: %s has no pin", name)
	}
	if err != nil {
		return err
	}
	var b strings.Builder
	pinned := indentJSON(p.Definition)
	fmt.Fprintf(&b, "%s: %s\npinned %s, sha256 %s\n", name, p.State(), p.PinnedAt.Local().Format("2006-01-02 15:04:05"), visible.Escape(p.SHA256))
	writeLines(&b, "  ", pinned)
	if p.State() == pin.Changed {
		changed := indentJSON(p.NewDefinition)
		fmt.Fprintf(&b, "new, seen %s, sha256 %s\n", p.ChangedAt.Local().Format("2006-01-02 15:04:05"), visible.Escape(p.NewSHA256))
		writeLines(&b, "  ", changed)
		b.WriteString("lines that differ:\n")
		removed, added := differ(pinned, changed)
		writeLines(&b, "- ", removed)
		writeLines(&b, "+ ", added)
	}
	_, err = io.WriteString(stdout, b.String())
	return err
}

// acceptPin makes the definition last recorded for a tool its pin, and prints the hash it accepted.
func acceptPin(ctx context.Context, path, server, tool string, stdout io.Writer) error {
	db, err := store.OpenExistingWritable(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	name := visible.Escape(server + "__" + tool)
	p, err := pin.NewStore(db).Accept(ctx, server, tool)
	switch {
	case errors.Is(err, pin.ErrNoPin):
		return fmt.Errorf("pins accept: %s has no pin", name)
	case errors.Is(err, pin.ErrNotChanged):
		return fmt.Errorf("pins accept: %s has no change to accept", name)
	case err != nil:
		return err
	}
	_, err = fmt.Fprintf(stdout, "accepted %s: its pin is now the definition with sha256 %s; running gates serve it within two seconds\n",
		name, visible.Escape(p.SHA256))
	return err
}

// indentJSON splits a stored definition into indented lines. A definition that is not JSON, which only
// a database edited by hand holds, is shown as it is.
func indentJSON(def string) []string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(def), "", "  "); err != nil {
		return strings.Split(def, "\n")
	}
	return strings.Split(buf.String(), "\n")
}

func writeLines(b *strings.Builder, prefix string, lines []string) {
	for _, l := range lines {
		b.WriteString(prefix + visible.Escape(l) + "\n")
	}
}

// differ returns the lines only in a and the lines only in b, each in its own order, counting repeated
// lines.
// ponytail: a multiset difference, not a minimal diff, so a line that only moved is not shown; an LCS
// diff if reviews need one.
func differ(a, b []string) (onlyA, onlyB []string) {
	only := func(from, other []string) []string {
		count := map[string]int{}
		for _, l := range other {
			count[l]++
		}
		var out []string
		for _, l := range from {
			if count[l] > 0 {
				count[l]--
				continue
			}
			out = append(out, l)
		}
		return out
	}
	return only(a, b), only(b, a)
}
