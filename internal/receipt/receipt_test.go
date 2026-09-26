package receipt_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tunahanaliozturk/portcullis/internal/receipt"
	"github.com/tunahanaliozturk/portcullis/internal/store"
)

// TestMain doubles as the helper process for TestConcurrentProcessesKeepOneChain.
func TestMain(m *testing.M) {
	if path := os.Getenv("PORTCULLIS_TEST_APPEND_DB"); path != "" {
		if err := appendFromHelper(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func appendFromHelper(path string) error {
	n, err := strconv.Atoi(os.Getenv("PORTCULLIS_TEST_APPEND_N"))
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
		r.Agent = os.Getenv("PORTCULLIS_TEST_APPEND_AGENT")
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
			"PORTCULLIS_TEST_APPEND_DB="+path,
			"PORTCULLIS_TEST_APPEND_N="+strconv.Itoa(each),
			"PORTCULLIS_TEST_APPEND_AGENT=agent"+strconv.Itoa(i),
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
