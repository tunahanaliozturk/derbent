package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

// reportLatency adds p50, p99 and calls per second to a benchmark's result, so benchstat can compare
// them across runs as well as ns/op.
func reportLatency(b *testing.B, latencies []time.Duration, total time.Duration) {
	b.Helper()
	if len(latencies) == 0 {
		return
	}
	slices.Sort(latencies)
	b.ReportMetric(float64(latencies[len(latencies)/2].Microseconds()), "p50-us")
	b.ReportMetric(float64(latencies[len(latencies)*99/100].Microseconds()), "p99-us")
	b.ReportMetric(float64(len(latencies))/total.Seconds(), "calls/s")
}

// timeRuns calls do b.N times, timing each call, and reports the latencies.
func timeRuns(b *testing.B, do func() error) {
	b.Helper()
	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	sinceBegan := stopwatch()
	for range b.N {
		sinceStart := stopwatch()
		if err := do(); err != nil {
			b.Fatal(err)
		}
		latencies = append(latencies, sinceStart())
	}
	total := sinceBegan()
	b.StopTimer()
	reportLatency(b, latencies, total)
}

// benchSession connects to the echo stand-in directly, or to a real gate process with the echo
// stand-in behind it and one allow rule, and returns the session and the tool to call. Every process
// has started and answered once before the benchmark's timer starts.
func benchSession(b *testing.B, dir, path string) (*mcp.ClientSession, string) {
	b.Helper()
	cmd := exec.CommandContext(b.Context(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "DERBENT_TEST_ECHO=1")
	tool := "echo"
	if path == "gate" {
		cfg := "[servers.echo]\ncommand = ['" + os.Args[0] + "', '-test.run=^$']\nenv = { DERBENT_TEST_ECHO = \"1\" }\n\n[[rule]]\naction = \"allow\"\n"
		cfgPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			b.Fatal(err)
		}
		cmd, tool = gateCommand(b, dir, "bench", cfgPath), "echo__echo"
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "0"}, nil).
		Connect(b.Context(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cs.Close() })
	if res, err := cs.CallTool(b.Context(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"text": "warm"}}); err != nil || res.IsError {
		b.Fatalf("warm-up call: err %v, result %q", err, resultText(res))
	}
	return cs, tool
}

// BenchmarkEcho compares one tool call to the echo stand-in made directly with the same call made
// through the gate, which decides it with a rule, forwards it, and appends a receipt before answering.
func BenchmarkEcho(b *testing.B) {
	for _, path := range []string{"direct", "gate"} {
		b.Run("path="+path, func(b *testing.B) {
			dir := b.TempDir()
			cs, tool := benchSession(b, dir, path)
			params := &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"text": "hello"}}
			timeRuns(b, func() error {
				res, err := cs.CallTool(b.Context(), params)
				if err == nil && res.IsError {
					b.Fatalf("call: %q", resultText(res))
				}
				return err
			})
			if path == "gate" {
				checkReceipts(b, filepath.Join(dir, "p.db"), b.N+1)
			}
		})
	}
}

// checkReceipts proves a gate run did the work it is measured for: one receipt per call.
func checkReceipts(b *testing.B, dbPath string, want int) {
	b.Helper()
	db, err := store.OpenExisting(b.Context(), dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRowContext(b.Context(), `SELECT count(*) FROM receipts`).Scan(&n); err != nil || n != want {
		b.Fatalf("receipts = %d, err %v; want %d", n, err, want)
	}
}

// BenchmarkHook's call=hook measures one pre-tool hook call, `derbent gate`, allowed by a rule: a
// process start, a config load, a database open, a decision and a receipt. call=spawn starts the same
// binary as `derbent version` and exits at once, which is the part of the cost that any hook command pays.
// call=hook-beside-gate is call=hook while another connection holds the database open, as a running
// `derbent mcp` does: the -wal file is there, so each hook call checks the file on a read-only open first.
func BenchmarkHook(b *testing.B) {
	for _, name := range []string{"call=spawn", "call=hook", "call=hook-beside-gate"} {
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[[rule]]\naction = \"allow\"\n"), 0o600); err != nil {
				b.Fatal(err)
			}
			in := claudeHookInput(dir, "go test ./...")
			// The first hook call creates the database outside the timer, and shows the call is allowed
			// with nothing on stdout, so the loop measures that path and not a deny.
			if out, errOut, code := runHook(b, dir, in, "--agent", "claude"); code != 0 || out != "" {
				b.Fatalf("code %d, stdout %q, stderr %q; want exit 0 and no output", code, out, errOut)
			}
			args := []string{"gate", "--db", filepath.Join(dir, "p.db"), "--config", filepath.Join(dir, "config.toml"), "--agent", "claude"}
			if name == "call=spawn" {
				args = []string{"version"}
			}
			if name == "call=hook-beside-gate" {
				db, err := store.Open(b.Context(), filepath.Join(dir, "p.db"))
				if err != nil {
					b.Fatal(err)
				}
				conn, err := db.Conn(b.Context()) // held for the whole run, so the -wal stays
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close()
				defer conn.Close()
				if _, err = os.Stat(filepath.Join(dir, "p.db-wal")); err != nil {
					b.Fatalf("want a -wal beside the database while it is open: %v", err)
				}
			}
			timeRuns(b, func() error {
				cmd := exec.CommandContext(b.Context(), os.Args[0], args...)
				cmd.Env = append(os.Environ(), "DERBENT_TEST_MAIN=1")
				cmd.Stdin = strings.NewReader(in)
				return cmd.Run()
			})
			if name != "call=spawn" {
				checkReceipts(b, filepath.Join(dir, "p.db"), b.N+1)
			}
		})
	}
}
