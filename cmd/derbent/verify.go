package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

var (
	errChainBroken  = errors.New("the receipt chain is broken")
	errExportBroken = errors.New("the export does not verify")
)

// runVerify checks the whole receipt chain in the database and prints the head hash, which is worth
// keeping somewhere else: it is what shows a chain that was cut short or rewritten as a whole. With
// --file it checks an export of derbent receipts --json instead, without the database (ADR 0017), and
// --head checks the export's head against a hash kept earlier.
func runVerify(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	file := flags.String("file", "", "an export of derbent receipts --json to check without the database, or - for standard input")
	kept := flags.String("head", "", "with --file, a head hash you kept, which the export's last line must carry")
	var err error
	if err = flags.Parse(args); err != nil {
		return err
	}
	switch {
	case flags.NArg() > 0:
		return fmt.Errorf("verify: unexpected argument %q", flags.Arg(0))
	case *file != "" && *dbPath != "":
		return errors.New("verify: give --file or --db, not both")
	case *file != "":
		return verifyFile(ctx, *file, *kept, stdin, stdout)
	case *kept != "":
		// The database's head moves on with every call, so an older kept hash is no longer the head.
		return errors.New("verify: --head goes with --file; for the database, compare the head it prints with the one you kept")
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

// verifyFile checks an export of derbent receipts --json line by line without the database, and prints
// what it covers: the lines, the runs of sequence numbers that follow on, the gaps between them, the
// anchor, the head, and which runs a kept head ties to the database. name is a file, or - for stdin; kept,
// when set, is a head hash the user kept. A line that fails is named, and every value read from the file is
// escaped before it is printed.
func verifyFile(ctx context.Context, name, kept string, stdin io.Reader, stdout io.Writer) error {
	in := stdin
	if name != "-" {
		f, err := os.Open(name) //nolint:gosec // the user names the export to check
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		defer f.Close()
		in = f
	}
	text, err := exportText(ctx, in)
	if err != nil {
		return fmt.Errorf("verify: read %s: %w", name, err)
	}
	var check receipt.ExportCheck
	for n := 1; ; n++ {
		line, readErr := text.ReadBytes('\n')
		// Ctrl-C cancels ctx: stop here rather than check a line read after it.
		if err = ctx.Err(); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var row receipt.Row
			if err = decodeRow(line, &row); err != nil {
				return fmt.Errorf("%w: line %d is not a receipt as derbent receipts --json prints it: %s",
					errExportBroken, n, visible.Escape(err.Error()))
			}
			if err = check.Add(row); err != nil {
				return fmt.Errorf("%w at line %d (receipt %d): %w", errExportBroken, n, row.Seq, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("verify: read %s: %w", name, readErr)
		}
	}
	res := check.Result()
	if res.Lines == 0 {
		return fmt.Errorf("%w: %s holds no receipts", errExportBroken, name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "receipts: %d %s, %d %s\n", res.Lines, plural(res.Lines, "line", "lines"), len(res.Runs), plural(len(res.Runs), "run", "runs"))
	fmt.Fprintf(&b, "runs:     %s\n", spans(res.Runs))
	var gaps [][2]int64
	for i := 1; i < len(res.Runs); i++ {
		gaps = append(gaps, [2]int64{res.Runs[i-1][1] + 1, res.Runs[i][0] - 1})
	}
	if len(gaps) > 0 {
		fmt.Fprintf(&b, "gaps:     %s, not in the export\n", spans(gaps))
	}
	anchor := visible.Escape(res.Anchor)
	if res.Anchor == receipt.Genesis {
		anchor += " (the start of the chain)"
	}
	fmt.Fprintf(&b, "anchor:   %s\nhead:     %s\n", anchor, visible.Escape(res.Head))
	// The hash takes no key, so anyone holding the export can edit a line and compute its hash again. Only
	// a kept head ties lines to the database, and only those of the run that ends at it (ADR 0017).
	switch {
	case kept == "":
		b.WriteString("kept:     no --head given, so nothing ties the export to the database: anyone can recompute every hash\n")
	case res.Head != kept:
		if _, err = io.WriteString(stdout, b.String()); err != nil {
			return err
		}
		return fmt.Errorf("%w: its head is not the hash you kept, %s", errExportBroken, visible.Escape(kept))
	default:
		b.WriteString("kept:     the head is the hash you kept\n")
		if n := len(res.Runs) - 1; n > 0 {
			fmt.Fprintf(&b, "tied:     only the last run, %s, is tied to the kept head; the %d %s before it %s tied to no kept hash\n",
				spans(res.Runs[n:]), n, plural(n, "run", "runs"), plural(n, "is", "are"))
		}
	}
	b.WriteString("export:   every line's hash matches its fields, and the lines of each run are linked\n")
	_, err = io.WriteString(stdout, b.String())
	return err
}

// exportText returns r's text as UTF-8. Windows PowerShell 5.1 writes a program's output redirected with >
// as UTF-16 with a byte order mark, and some tools start UTF-8 with one; both are read.
// ponytail: a UTF-16 export is decoded whole in memory; stream it if such exports get large.
func exportText(ctx context.Context, r io.Reader) (*bufio.Reader, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(3)
	if err != nil && !errors.Is(err, io.EOF) { // io.EOF only says the input is shorter than a byte order mark
		return nil, err
	}
	switch {
	case bytes.HasPrefix(head, []byte("\xef\xbb\xbf")):
		_, err = br.Discard(3)
		return br, err
	case bytes.HasPrefix(head, []byte{0xff, 0xfe}), bytes.HasPrefix(head, []byte{0xfe, 0xff}):
		data, readErr := io.ReadAll(br)
		if readErr != nil {
			return nil, readErr
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		units := make([]uint16, 0, len(data)/2)
		for i := 2; i+1 < len(data); i += 2 {
			units = append(units, order.Uint16(data[i:]))
		}
		return bufio.NewReader(strings.NewReader(string(utf16.Decode(units)))), nil
	}
	return br, nil
}

// rowKeys are the JSON names of receipt.Row's fields, in order.
var rowKeys = func() []string {
	t := reflect.TypeFor[receipt.Row]()
	keys := make([]string, t.NumField())
	for i := range keys {
		keys[i], _, _ = strings.Cut(t.Field(i).Tag.Get("json"), ",")
	}
	return keys
}()

// decodeRow reads one export line into row. The line must be one JSON object that holds each of Row's
// fields once, under its exact name, none of them null, and nothing else. Go's decoder alone would match a
// name in any case, keep the last of two copies, read a missing or null field as its zero value and stop at
// the end of the object, so a line could show a reader one value while the hash covers another, or leave a
// field out unnoticed.
func decodeRow(line []byte, row *receipt.Row) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("it is not a JSON object")
	}
	seen := make(map[string]bool, len(rowKeys))
	for dec.More() {
		if tok, err = dec.Token(); err != nil {
			return err
		}
		key, _ := tok.(string) // inside an object, Token returns each key as a string
		switch {
		case !slices.Contains(rowKeys, key):
			return fmt.Errorf("it holds %q, which is not one of a receipt's fields", key)
		case seen[key]:
			return fmt.Errorf("it holds %q twice", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return err
		}
		if string(value) == "null" {
			return fmt.Errorf("it holds null for %q", key)
		}
	}
	if _, err = dec.Token(); err != nil { // the closing brace
		return err
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("it holds more after the object")
	}
	for _, key := range rowKeys {
		if !seen[key] {
			return fmt.Errorf("it lacks %q", key)
		}
	}
	return json.Unmarshal(line, row)
}

// spans writes each [first, last] pair as "first-last", or "first" when the two are the same, joined
// with commas.
func spans(list [][2]int64) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = strconv.FormatInt(s[0], 10)
		if s[1] != s[0] {
			parts[i] += "-" + strconv.FormatInt(s[1], 10)
		}
	}
	return strings.Join(parts, ", ")
}

// plural is one when n is 1, and many otherwise.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
