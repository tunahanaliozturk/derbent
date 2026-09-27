package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
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

var (
	// wholeSHA256 is a hash as derbent pins accept takes it: all 64 hex digits, never a prefix (see
	// pin.Store.Accept).
	wholeSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// plainArg is a command-line argument that bash, PowerShell and cmd all take as it is.
	plainArg = regexp.MustCompile(`^[A-Za-z0-9_./\\:-]+$`)
)

// runPins lists the tool pins, shows one tool's change, or accepts it: derbent pins [--json],
// derbent pins show <server>__<tool>, derbent pins accept <server>__<tool> <sha256>.
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
	switch {
	case sub == "show" && flags.NArg() != 1:
		return errors.New("pins show: give one tool as <server>__<tool>, as derbent pins lists it")
	case sub == "accept" && flags.NArg() != 2:
		return errors.New("pins accept: give the tool and the sha256 of its new definition, as derbent pins show prints them: " +
			"derbent pins accept <server>__<tool> <sha256>")
	}
	server, tool, ok := strings.Cut(flags.Arg(0), "__")
	if !ok || server == "" || tool == "" {
		return fmt.Errorf("pins %s: %q is not <server>__<tool>", sub, visible.Escape(flags.Arg(0)))
	}
	if sub == "show" {
		return showPin(ctx, path, *dbFlag, server, tool, stdout)
	}
	given := strings.ToLower(flags.Arg(1))
	if !wholeSHA256.MatchString(given) {
		return fmt.Errorf("pins accept: %q is not the sha256 of the new definition, which is 64 hex digits; copy it whole from derbent pins show",
			visible.Escape(flags.Arg(1)))
	}
	return acceptPin(ctx, path, server, tool, given, stdout)
}

// shellArg is s as it can be pasted into bash, PowerShell or cmd: as it is when it holds only letters,
// digits and _ . / \ : -, and in double quotes otherwise, which covers spaces.
// ponytail: a path holding a double quote, $ or a backtick still needs quoting by hand after pasting;
// per-shell quoting if one ever does.
func shellArg(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	return `"` + s + `"`
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

// showPin prints a tool's pinned definition and, when its server has since sent another, that one, the
// lines that differ and the command that accepts exactly that one, with dbFlag, the --db pins show was
// given, so the command works as pasted. Definitions come from a server, so every line is escaped.
func showPin(ctx context.Context, path, dbFlag, server, tool string, stdout io.Writer) error {
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
		if lines, ok := differ(pinned, changed); ok {
			writeLines(&b, "", lines)
		} else {
			b.WriteString("  too long to line up; compare the two definitions above in full\n")
		}
		db := ""
		if dbFlag != "" {
			db = "--db " + shellArg(visible.Escape(dbFlag)) + " "
		}
		fmt.Fprintf(&b, "to accept this change: derbent pins accept %s%s %s\n", db, name, visible.Escape(p.NewSHA256))
	}
	_, err = io.WriteString(stdout, b.String())
	return err
}

// acceptPin makes the change recorded for a tool its pin when its whole hash equals given, the hash
// derbent pins show printed, and prints the hash it accepted.
func acceptPin(ctx context.Context, path, server, tool, given string, stdout io.Writer) error {
	db, err := store.OpenExistingWritable(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	name := visible.Escape(server + "__" + tool)
	p, err := pin.NewStore(db).Accept(ctx, server, tool, given)
	switch {
	case errors.Is(err, pin.ErrNoPin):
		return fmt.Errorf("pins accept: %s has no pin", name)
	case errors.Is(err, pin.ErrNotChanged):
		return fmt.Errorf("pins accept: %s has no change to accept", name)
	case errors.Is(err, pin.ErrOtherChange):
		return fmt.Errorf("pins accept: %s: the change on record is not the one given; run derbent pins show again", name)
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

// maxDiffCells caps the table differ builds, len(a)+1 by len(b)+1 int32s: 16 MiB at most.
const maxDiffCells = 1 << 22

// differ returns the lines that turn a into b, in order, "- " for a line of a that goes and "+ " for a
// line of b that comes, lined up on a longest common subsequence. A line that only moved, such as a
// description swapped from one property to another, shows where it went from and where it arrived, even
// beside other changes. For definitions too long to line up within maxDiffCells it returns false, and
// the reader compares the two in full.
func differ(a, b []string) ([]string, bool) {
	if (len(a)+1)*(len(b)+1) > maxDiffCells {
		return nil, false
	}
	// common[i][j] is the length of the longest common subsequence of a[i:] and b[j:].
	common := make([][]int32, len(a)+1)
	for i := range common {
		common[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			i, j = i+1, j+1
		case common[i+1][j] >= common[i][j+1]:
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, "+ "+b[j])
	}
	return out, true
}
