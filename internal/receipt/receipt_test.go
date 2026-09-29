package receipt_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// TestMain doubles as the helper process for TestConcurrentProcessesKeepOneChain.
func TestMain(m *testing.M) {
	if path := os.Getenv("DERBENT_TEST_APPEND_DB"); path != "" {
		if err := appendFromHelper(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func appendFromHelper(path string) error {
	n, err := strconv.Atoi(os.Getenv("DERBENT_TEST_APPEND_N"))
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	log := receipt.NewLog(db)
	for i := range n {
		r := sample(i)
		r.Agent = os.Getenv("DERBENT_TEST_APPEND_AGENT")
		if _, err := log.Append(ctx, r); err != nil {
			return fmt.Errorf("append %d: %w", i, err)
		}
	}
	return nil
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func sample(i int) receipt.Receipt {
	return receipt.Receipt{
		Project: "/work/shop", Agent: "claude", Session: "s1", Tool: "memory_write",
		Args: fmt.Sprintf(`{"title":"note %d"}`, i), ArgsSHA256: "a", Decision: "allow", DecidedBy: "rule:1",
		Outcome: "ok", ResultSize: 10, ResultSHA256: "r", Duration: time.Duration(i) * time.Millisecond,
	}
}

func appendN(t *testing.T, log *receipt.Log, n int) []receipt.Receipt {
	t.Helper()
	out := make([]receipt.Receipt, 0, n)
	for i := range n {
		r, err := log.Append(t.Context(), sample(i))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func verify(t *testing.T, log *receipt.Log) receipt.Result {
	t.Helper()
	res, err := log.Verify(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAppendChains(t *testing.T) {
	log := receipt.NewLog(openDB(t, filepath.Join(t.TempDir(), "p.db")))
	got := appendN(t, log, 2)
	if got[0].Seq != 1 || got[0].PrevHash != receipt.Genesis {
		t.Fatalf("first = seq %d prev %s", got[0].Seq, got[0].PrevHash)
	}
	if got[1].Seq != 2 || got[1].PrevHash != got[0].Hash {
		t.Fatalf("second = seq %d prev %s, want prev %s", got[1].Seq, got[1].PrevHash, got[0].Hash)
	}
	res := verify(t, log)
	if res.Count != 2 || res.FirstBad != 0 || res.Head != got[1].Hash {
		t.Fatalf("Verify = %+v", res)
	}
}

func TestVerifyEmptyLog(t *testing.T) {
	res := verify(t, receipt.NewLog(openDB(t, filepath.Join(t.TempDir(), "p.db"))))
	if res.Count != 0 || res.FirstBad != 0 || res.Head != receipt.Genesis {
		t.Fatalf("Verify = %+v", res)
	}
}

func TestVerifyFindsTampering(t *testing.T) {
	const cols = `at, project, agent, session, tool, args, args_sha256, decision, decided_by, outcome,
		result_size, result_sha256, duration_ms, prev_hash, hash`
	tests := map[string]struct {
		tamper   string
		firstBad int64
	}{
		"edited arguments": {`UPDATE receipts SET args = '{"title":"forged"}' WHERE seq = 7`, 7},
		"edited decision":  {`UPDATE receipts SET decision = 'deny' WHERE seq = 3`, 3},
		"deleted receipt":  {`DELETE FROM receipts WHERE seq = 5`, 6},
		"swapped receipts": {`UPDATE receipts SET args = CASE seq
			WHEN 4 THEN (SELECT args FROM receipts WHERE seq = 5)
			ELSE (SELECT args FROM receipts WHERE seq = 4) END WHERE seq IN (4, 5)`, 4},
		"inserted receipt": {`UPDATE receipts SET seq = seq + 100 WHERE seq >= 6;
			UPDATE receipts SET seq = seq - 99 WHERE seq >= 106;
			INSERT INTO receipts (seq, ` + cols + `) SELECT 6, ` + cols + ` FROM receipts WHERE seq = 5;`, 6},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			db := openDB(t, filepath.Join(t.TempDir(), "p.db"))
			log := receipt.NewLog(db)
			appendN(t, log, 10)
			if _, err := db.ExecContext(t.Context(), tc.tamper); err != nil {
				t.Fatal(err)
			}
			if res := verify(t, log); res.FirstBad != tc.firstBad || res.Reason == "" {
				t.Fatalf("Verify = %+v, want first bad %d", res, tc.firstBad)
			}
		})
	}
}

// A deleted tail leaves a chain that is consistent on its own. Only a copy of the head hash kept
// elsewhere shows it, which is why verify prints the head.
func TestTruncatedTailIsOnlyCaughtByTheHead(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "p.db"))
	log := receipt.NewLog(db)
	head := appendN(t, log, 10)[9].Hash
	if _, err := db.ExecContext(t.Context(), `DELETE FROM receipts WHERE seq = 10`); err != nil {
		t.Fatal(err)
	}
	res := verify(t, log)
	if res.FirstBad != 0 {
		t.Fatalf("Verify = %+v, want the shortened chain to look intact", res)
	}
	if res.Head == head {
		t.Fatal("head did not change")
	}
}

func TestConcurrentProcessesKeepOneChain(t *testing.T) {
	if testing.Short() {
		t.Skip("starts four processes")
	}
	path := filepath.Join(t.TempDir(), "p.db")
	db := openDB(t, path)
	const procs, each = 4, 2500
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for i := range cmds {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(),
			"DERBENT_TEST_APPEND_DB="+path,
			"DERBENT_TEST_APPEND_N="+strconv.Itoa(each),
			"DERBENT_TEST_APPEND_AGENT=agent"+strconv.Itoa(i),
		)
		outs[i] = &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = outs[i], outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", i, err, outs[i])
		}
	}
	res := verify(t, receipt.NewLog(db))
	if res.Count != procs*each || res.FirstBad != 0 {
		t.Fatalf("Verify = %+v, want %d intact receipts", res, procs*each)
	}
	rows, err := db.QueryContext(t.Context(), `SELECT agent, count(*) FROM receipts GROUP BY agent`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		var n int
		if err := rows.Scan(&agent, &n); err != nil {
			t.Fatal(err)
		}
		if n != each {
			t.Errorf("%s appended %d, want %d", agent, n, each)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestListFiltersAndKeepsTheNewest(t *testing.T) {
	log := receipt.NewLog(openDB(t, filepath.Join(t.TempDir(), "p.db")))
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	for i, r := range []struct{ agent, tool string }{
		{"claude", "memory_write"},
		{"codex", "github__get_me"},
		{"codex", "memory_search"},
		{"claude", "github__create_issue"},
		{"codex", "github__create_issue"},
	} {
		rec := sample(i)
		rec.Agent, rec.Tool = r.agent, r.tool
		rec.At = base.Add(time.Duration(i)*time.Minute + 500*time.Millisecond)
		if _, err := log.Append(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	seqs := func(f receipt.Filter) []int64 {
		t.Helper()
		got, err := log.List(t.Context(), f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]int64, 0, len(got))
		for _, r := range got {
			out = append(out, r.Seq)
		}
		return out
	}
	tests := map[string]struct {
		f    receipt.Filter
		want []int64
	}{
		"everything, oldest first":  {receipt.Filter{}, []int64{1, 2, 3, 4, 5}},
		"by agent":                  {receipt.Filter{Agent: "codex"}, []int64{2, 3, 5}},
		"by tool glob":              {receipt.Filter{Tool: "github__*"}, []int64{2, 4, 5}},
		"agent and tool":            {receipt.Filter{Agent: "claude", Tool: "github__*"}, []int64{4}},
		"newest two":                {receipt.Filter{Limit: 2}, []int64{4, 5}},
		"after a sequence number":   {receipt.Filter{AfterSeq: 3}, []int64{4, 5}},
		"since includes its second": {receipt.Filter{Since: base.Add(2 * time.Minute)}, []int64{3, 4, 5}},
		"by project":                {receipt.Filter{Project: "/elsewhere"}, nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := seqs(tc.f); !slices.Equal(got, tc.want) {
				t.Fatalf("List = %v, want %v", got, tc.want)
			}
		})
	}
	got, err := log.List(t.Context(), receipt.Filter{AfterSeq: 4})
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if r := got[0]; r.Agent != "codex" || r.Tool != "github__create_issue" || !r.At.Equal(base.Add(4*time.Minute+500*time.Millisecond)) ||
		r.Duration != 4*time.Millisecond || r.Hash == "" {
		t.Fatalf("receipt read back as %+v", r)
	}
}

// An export carries the time as the database stores it, which is what the hash reads. A time whose
// fraction Go would print shorter, such as .120, must come back as it is, or a line from another writer,
// or an older one, would fail its check.
func TestRowsKeepTheStoredTimeText(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "p.db"))
	r := receipt.Row{
		Seq: 1, At: "2026-09-27T10:00:00.120Z", Project: "p", Agent: "claude", Session: "s", Tool: "t", Args: "{}",
		Decision: "allow", DecidedBy: "rule:1", Outcome: "ok", PrevHash: receipt.Genesis,
	}
	r.Hash = r.Sum()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO receipts (seq, at, project, agent, session, tool, args,
		args_sha256, decision, decided_by, outcome, result_size, result_sha256, duration_ms, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Seq, r.At, r.Project, r.Agent, r.Session, r.Tool, r.Args, r.ArgsSHA256, r.Decision, r.DecidedBy,
		r.Outcome, r.ResultSize, r.ResultSHA256, r.DurationMS, r.PrevHash, r.Hash); err != nil {
		t.Fatal(err)
	}
	log := receipt.NewLog(db)
	rows, err := log.Rows(t.Context(), receipt.Filter{})
	if err != nil || len(rows) != 1 || rows[0] != r {
		t.Fatalf("Rows = %+v, %v; want %+v", rows, err, r)
	}
	var c receipt.ExportCheck
	if err = c.Add(rows[0]); err != nil {
		t.Fatalf("the stored row fails the export check: %v", err)
	}
	if res := verify(t, log); res.FirstBad != 0 || res.Head != r.Hash {
		t.Fatalf("Verify = %+v", res)
	}
}

// exportOf checks rows as an export and returns what the check found, or the first failure with the
// number of the row that failed.
func exportOf(rows ...receipt.Row) (receipt.Export, error) {
	var c receipt.ExportCheck
	for i, r := range rows {
		if err := c.Add(r); err != nil {
			return c.Result(), fmt.Errorf("row %d: %w", i+1, err)
		}
	}
	return c.Result(), nil
}

// The export check needs no database: it recomputes each row's hash, links rows whose sequence numbers
// follow on, reports a gap as a new run, and fails a row that was edited, a broken link, a number that
// does not rise and a first receipt that does not follow the genesis hash.
func TestExportCheck(t *testing.T) {
	log := receipt.NewLog(openDB(t, filepath.Join(t.TempDir(), "p.db")))
	appendN(t, log, 6)
	all, err := log.Rows(t.Context(), receipt.Filter{Limit: -1})
	if err != nil || len(all) != 6 {
		t.Fatalf("Rows = %d rows, %v", len(all), err)
	}
	res, err := exportOf(all...)
	if err != nil || res.Lines != 6 || len(res.Runs) != 1 || res.Runs[0] != [2]int64{1, 6} ||
		res.Anchor != receipt.Genesis || res.Head != all[5].Hash {
		t.Fatalf("whole chain: %+v, %v", res, err)
	}
	res, err = exportOf(all[1], all[2], all[4], all[5])
	if err != nil || res.Lines != 4 || len(res.Runs) != 2 || res.Runs[0] != [2]int64{2, 3} || res.Runs[1] != [2]int64{5, 6} ||
		res.Anchor != all[0].Hash || res.Head != all[5].Hash {
		t.Fatalf("filtered: %+v, %v", res, err)
	}

	edited := all[2]
	edited.Args = `{"title":"forged"}`
	relinked := all[2]
	relinked.PrevHash = strings.Repeat("1", 64)
	relinked.Hash = relinked.Sum() // its own hash passes; the link to row 2 does not
	rewound := all[0]
	rewound.PrevHash = strings.Repeat("2", 64)
	rewound.Hash = rewound.Sum()
	for name, tc := range map[string]struct {
		rows []receipt.Row
		want string
	}{
		"an edited field":     {[]receipt.Row{all[0], all[1], edited}, "row 3: its hash does not match its fields"},
		"a broken link":       {[]receipt.Row{all[0], all[1], relinked}, "row 3: its prev_hash is not the hash of the line before it"},
		"a number going back": {[]receipt.Row{all[0], all[2], all[1]}, "row 3: its sequence number 2 does not come after 3"},
		"a repeated line":     {[]receipt.Row{all[0], all[0]}, "row 2: its sequence number 1 does not come after 1"},
		"a false start":       {[]receipt.Row{rewound}, "row 1: it is receipt 1, and its prev_hash is not the genesis hash"},
	} {
		if _, err = exportOf(tc.rows...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if res, err = exportOf(); err != nil || res.Lines != 0 || len(res.Runs) != 0 {
		t.Fatalf("no rows: %+v, %v", res, err)
	}
}

func TestAgentsSeenSince(t *testing.T) {
	log := receipt.NewLog(openDB(t, filepath.Join(t.TempDir(), "p.db")))
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for i, r := range []struct {
		agent string
		ago   time.Duration
	}{{"copilot", 3 * time.Hour}, {"codex", 30 * time.Minute}, {"claude", 10 * time.Minute}, {"codex", time.Minute}} {
		rec := sample(i)
		rec.Agent, rec.At = r.agent, now.Add(-r.ago)
		if _, err := log.Append(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	seen, err := log.Agents(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []receipt.AgentSeen{{Agent: "claude", Last: now.Add(-10 * time.Minute)}, {Agent: "codex", Last: now.Add(-time.Minute)}}
	if len(seen) != len(want) {
		t.Fatalf("Agents = %+v, want %+v", seen, want)
	}
	for i := range want {
		if seen[i].Agent != want[i].Agent || !seen[i].Last.Equal(want[i].Last) {
			t.Fatalf("Agents = %+v, want %+v", seen, want)
		}
	}
}

// Stored times are RFC 3339 text with fractions of varying length, which do not sort correctly as text
// within one second, so AllowedSince reads from the second its bound falls in and keeps exactly the calls
// at or after the bound. It keeps the given agent's allowed calls only.
func TestAllowedSinceCountsExactlyFromItsBound(t *testing.T) {
	log := receipt.NewLog(openDB(t, filepath.Join(t.TempDir(), "p.db")))
	since := time.Date(2026, 9, 27, 12, 0, 0, 500_000_000, time.UTC)
	for _, r := range []struct {
		agent, tool, decision string
		at                    time.Time
	}{
		{"claude", "native__Bash", "allow", since.Add(-2 * time.Hour)},
		{"claude", "native__Bash", "allow", since.Add(-200 * time.Millisecond)}, // same second, before the bound
		{"claude", "native__Bash", "allow", since},
		{"claude", "native__Bash", "allow", since.Add(50 * time.Millisecond)}, // stored as .55Z, which sorts before .5Z as text
		{"claude", "native__Bash", "deny", since.Add(time.Second)},
		{"codex", "native__Bash", "allow", since.Add(time.Second)},
		{"claude", "memory_write", "allow", since.Add(time.Minute)},
	} {
		if _, err := log.Append(t.Context(), receipt.Receipt{
			At: r.at, Project: "p", Agent: r.agent, Session: "s", Tool: r.tool, Args: "{}", Decision: r.decision,
			DecidedBy: "rule:1", Outcome: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := log.AllowedSince(t.Context(), "claude", since)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b receipt.Allowed) int { return a.At.Compare(b.At) })
	want := []receipt.Allowed{
		{Tool: "native__Bash", At: since},
		{Tool: "native__Bash", At: since.Add(50 * time.Millisecond)},
		{Tool: "memory_write", At: since.Add(time.Minute)},
	}
	if len(got) != len(want) {
		t.Fatalf("AllowedSince = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Tool != want[i].Tool || !got[i].At.Equal(want[i].At) {
			t.Fatalf("AllowedSince[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
