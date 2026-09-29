package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// receiptsDB creates a database with n receipts, claude and codex taking turns, and returns its path.
func receiptsDB(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := receipt.NewLog(db)
	for i := range n {
		agent := "claude"
		if i%2 == 1 {
			agent = "codex"
		}
		if _, err = log.Append(t.Context(), receipt.Receipt{
			Project: "/work/shop", Agent: agent, Session: "s", Tool: "memory_write", Args: fmt.Sprintf(`{"title":"note %d"}`, i),
			ArgsSHA256: "a", Decision: "allow", DecidedBy: "rule:1", Outcome: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// headOf returns the head hash that derbent verify printed.
func headOf(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "head:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no head in %q", out)
	return ""
}

// verifyBytes writes data to a file and runs derbent verify --file on it with extra flags.
func verifyBytes(t *testing.T, data []byte, extra ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run(t.Context(), append([]string{"verify", "--file", path}, extra...), strings.NewReader(""), &out, io.Discard)
	return out.String(), err
}

func exportLines(s string) []string {
	return strings.SplitAfter(strings.TrimSuffix(s, "\n"), "\n")
}

// The milestone's evidence for export: a whole chain exported with --limit 0 verifies without the
// database, reaching the head verify printed; a filtered export reports its gaps and verifies; a line taken
// out of the middle is a gap, not a failure; and an edited line, a reordered one, a line that is not exactly
// one receipt, a head that is not the kept one and an empty file each fail, naming the line. The output
// never claims more than the hashes show: only the run that ends at a kept head is tied to it, and without
// --head nothing is.
func TestAnExportVerifiesWithoutTheDatabase(t *testing.T) {
	db := receiptsDB(t, 6)
	head := headOf(t, runOK(t, "verify", "--db", db))
	whole := runOK(t, "receipts", "--json", "--limit", "0", "--db", db)
	out, err := verifyBytes(t, []byte(whole), "--head", head)
	if err != nil {
		t.Fatalf("verify --file: %v\n%s", err, out)
	}
	for _, want := range []string{
		"receipts: 6 lines, 1 run\n", "runs:     1-6\n", "anchor:   " + receipt.Genesis + " (the start of the chain)\n",
		"head:     " + head + "\n", "kept:     the head is the hash you kept\n",
		"export:   every line's hash matches its fields, and the lines of each run are linked\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tied:") {
		t.Errorf("one run ending at the kept head needs no word on the others:\n%s", out)
	}

	codex := runOK(t, "receipts", "--json", "--limit", "0", "--agent", "codex", "--db", db)
	if out, err = verifyBytes(t, []byte(codex), "--head", head); err != nil ||
		!strings.Contains(out, "receipts: 3 lines, 3 runs\n") || !strings.Contains(out, "runs:     2, 4, 6\n") ||
		!strings.Contains(out, "gaps:     3, 5, not in the export\n") ||
		!strings.Contains(out, "tied:     only the last run, 6, is tied to the kept head; the 2 runs before it are tied to no kept hash\n") {
		t.Fatalf("filtered export: %v\n%s", err, out)
	}

	l := exportLines(whole)
	gap := strings.Join(append(append([]string{}, l[:2]...), l[3:]...), "")
	if out, err = verifyBytes(t, []byte(gap)); err != nil || !strings.Contains(out, "gaps:     3, not in the export\n") ||
		!strings.Contains(out, "kept:     no --head given, so nothing ties the export to the database: anyone can recompute every hash\n") {
		t.Fatalf("a line taken out: %v\n%s", err, out)
	}

	const notAReceipt = "line 1 is not a receipt as derbent receipts --json prints it"
	for name, tc := range map[string]struct {
		text  string
		extra []string
		want  string
	}{
		"an edited line":   {strings.Replace(whole, "note 2", "note X", 1), nil, "at line 3 (receipt 3): its hash does not match its fields"},
		"a reordered line": {l[0] + l[2] + l[1], nil, "at line 3 (receipt 2): its sequence number 2 does not come after 3"},
		"an unknown field": {strings.Replace(l[0], `{"seq":1,`, `{"seq":1,"extra":1,`, 1), nil, notAReceipt},
		// Go's decoder matches a name in any case and keeps the last copy, so without the key check jq
		// would show the first args and the hash would cover the second.
		"a field in capitals": {strings.Replace(l[0], `"args":`, `"args":"shown to jq","ARGS":`, 1), nil, notAReceipt + `: it holds "ARGS"`},
		"a repeated field":    {strings.Replace(l[0], `"args":`, `"args":"first","args":`, 1), nil, notAReceipt + `: it holds "args" twice`},
		"a trailing ]":        {strings.TrimSuffix(l[0], "\n") + "]\n", nil, notAReceipt + ": it holds more after the object"},
		"a trailing }":        {strings.TrimSuffix(l[0], "\n") + "}\n", nil, notAReceipt + ": it holds more after the object"},
		// A field left out decodes as its zero value, which is what these two hold, so the hash still matches.
		"a zero duration left out": {strings.Replace(l[0], `,"duration_ms":0`, "", 1), nil, notAReceipt + `: it lacks "duration_ms"`},
		"an empty result left out": {strings.Replace(l[0], `,"result_sha256":""`, "", 1), nil, notAReceipt + `: it lacks "result_sha256"`},
		"a wrong head":             {whole, []string{"--head", strings.Repeat("0", 63) + "1"}, "its head is not the hash you kept"},
		"an empty file":            {"\n", nil, "holds no receipts"},
	} {
		out, err = verifyBytes(t, []byte(tc.text), tc.extra...)
		if !errors.Is(err, errExportBroken) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q\n%s", name, err, tc.want, out)
		}
	}
}

// Windows PowerShell 5.1 writes a redirected command's output as UTF-16 with a byte order mark, and CRLF
// line endings, and other tools start UTF-8 with a byte order mark. All of them verify.
func TestVerifyFileReadsWhatWindowsShellsWrite(t *testing.T) {
	whole := strings.ReplaceAll(runOK(t, "receipts", "--json", "--limit", "0", "--db", receiptsDB(t, 4)), "\n", "\r\n")
	utf16Of := func(order binary.AppendByteOrder, bom []byte) []byte {
		out := append([]byte{}, bom...)
		for _, u := range utf16.Encode([]rune(whole)) {
			out = order.AppendUint16(out, u)
		}
		return out
	}
	for name, data := range map[string][]byte{
		"UTF-8 with CRLF":                 []byte(whole),
		"UTF-8 with a byte order mark":    append([]byte("\xef\xbb\xbf"), whole...),
		"UTF-16LE with a byte order mark": utf16Of(binary.LittleEndian, []byte{0xff, 0xfe}),
		"UTF-16BE with a byte order mark": utf16Of(binary.BigEndian, []byte{0xfe, 0xff}),
	} {
		if out, err := verifyBytes(t, data); err != nil || !strings.Contains(out, "receipts: 4 lines, 1 run\n") {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
}

// A hook call's arguments can run to 16 MiB, so a line can be far longer than a bufio.Scanner token.
func TestVerifyFileReadsALongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = receipt.NewLog(db).Append(t.Context(), receipt.Receipt{
		Project: "p", Agent: "claude", Session: "s", Tool: "native__Bash",
		Args: `{"command":"` + strings.Repeat("a", 1<<20+17) + `"}`, Decision: "allow", DecidedBy: "rule:1", Outcome: "gated",
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	export := runOK(t, "receipts", "--json", "--db", path)
	if out, verifyErr := verifyBytes(t, []byte(export)); verifyErr != nil || !strings.Contains(out, "receipts: 1 line, 1 run\n") {
		t.Fatalf("a line of %d bytes: %v\n%s", len(export), verifyErr, out)
	}
}

// --file - reads the export from standard input, so it can come straight from derbent receipts in a pipe.
func TestVerifyFileReadsStandardInput(t *testing.T) {
	whole := runOK(t, "receipts", "--json", "--limit", "0", "--db", receiptsDB(t, 3))
	var out bytes.Buffer
	if err := run(t.Context(), []string{"verify", "--file", "-"}, strings.NewReader(whole), &out, io.Discard); err != nil ||
		!strings.Contains(out.String(), "receipts: 3 lines, 1 run\n") {
		t.Fatalf("verify --file -: %v\n%s", err, out.String())
	}
}

// cancelOnRead cancels a context when it is read, and holds nothing.
type cancelOnRead context.CancelFunc

func (c cancelOnRead) Read([]byte) (int, error) {
	c()
	return 0, io.EOF
}

// Ctrl-C cancels the command's context, and verify --file stops at the next line instead of reading on.
// The line after the cancel is not JSON, so reading it would fail differently.
func TestVerifyFileStopsWhenCancelled(t *testing.T) {
	first := runOK(t, "receipts", "--json", "--db", receiptsDB(t, 1)) // one line, with its newline
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	in := io.MultiReader(strings.NewReader(first), cancelOnRead(cancel), strings.NewReader("not a receipt\n"))
	if err := run(ctx, []string{"verify", "--file", "-"}, in, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancel", err)
	}
}

// --limit 0 lists every receipt, past the 100 that receipt.Filter reads a zero limit as, so a whole chain
// can be exported; the default stays the newest 50.
func TestReceiptsLimitZeroListsEveryReceipt(t *testing.T) {
	db := receiptsDB(t, 101)
	if got := len(exportLines(runOK(t, "receipts", "--json", "--db", db))); got != 50 {
		t.Fatalf("the default lists %d, want the newest 50", got)
	}
	whole := runOK(t, "receipts", "--json", "--limit", "0", "--db", db)
	if got := len(exportLines(whole)); got != 101 {
		t.Fatalf("--limit 0 lists %d, want 101", got)
	}
	export := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(export, []byte(whole), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"receipts", "--limit", "-1", "--db", db}, "--limit must be 0, for every receipt, or more"},
		{[]string{"verify", "--file", export, "--db", db}, "give --file or --db, not both"},
		{[]string{"verify", "--head", strings.Repeat("0", 64), "--db", db}, "--head goes with --file"},
	} {
		if err := run(t.Context(), tc.args, strings.NewReader(""), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}
