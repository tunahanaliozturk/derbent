package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/portcullis/internal/receipt"
	"github.com/tunahanaliozturk/portcullis/internal/store"
)

// TestMain lets the end-to-end tests start this test binary as the portcullis command.
func TestMain(m *testing.M) {
	if os.Getenv("PORTCULLIS_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), []string{"version"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "dev" {
		t.Fatalf("version = %q, want dev", got)
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	err := run(t.Context(), []string{"nope"}, strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func TestRunWithoutArgumentsShowsUsage(t *testing.T) {
	err := run(t.Context(), nil, strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func TestMCPNeedsAValidAgent(t *testing.T) {
	for _, args := range [][]string{{"mcp"}, {"mcp", "--agent", "Claude Code"}} {
		err := run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "--agent") {
			t.Errorf("run %v: err = %v, want an --agent error", args, err)
		}
	}
}

func gateCommand(t *testing.T, dir, agent string) *exec.Cmd {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "mcp", "--agent", agent,
		"--db", filepath.Join(dir, "p.db"), "--config", filepath.Join(dir, "absent.toml"), "--project", dir)
	cmd.Env = append(os.Environ(), "PORTCULLIS_TEST_MAIN=1")
	return cmd
}

func connectProcess(t *testing.T, dir, agent string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).
		Connect(t.Context(), &mcp.CommandTransport{Command: gateCommand(t, dir, agent)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestTwoAgentProcessesShareMemory(t *testing.T) {
	dir := t.TempDir()

	claude := connectProcess(t, dir, "claude")
	res, err := claude.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{
		"title": "Retry policy", "body": "Payment calls retry three times with jitter.",
	}})
	if err != nil || res.IsError {
		t.Fatalf("write: err %v, result %q", err, resultText(res))
	}
	if err = claude.Close(); err != nil {
		t.Fatal(err)
	}

	codex := connectProcess(t, dir, "codex")
	res, err = codex.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_search", Arguments: map[string]any{"query": "jitter"}})
	if err != nil || res.IsError {
		t.Fatalf("search: err %v, result %q", err, resultText(res))
	}
	if got := resultText(res); !strings.Contains(got, "Retry policy") || !strings.Contains(got, `"author":"claude"`) {
		t.Fatalf("search result = %s", got)
	}
	if err = codex.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run(t.Context(), []string{"verify", "--db", filepath.Join(dir, "p.db")}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("verify: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "receipts: 2") || !strings.Contains(out.String(), "intact") {
		t.Fatalf("verify output:\n%s", out.String())
	}
}

func TestVerifyDoesNotCreateADatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	err := run(t.Context(), []string{"verify", "--db", filepath.Join(dir, "p.db")}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("verify of a missing database succeeded")
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("verify created %s", dir)
	}
}

// Verify is run on evidence, so it must read the database without writing a byte of it.
func TestVerifyLeavesTheDatabaseUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = receipt.NewLog(db).Append(t.Context(), receipt.Receipt{Project: "p", Agent: "a", Session: "s", Tool: "t", Args: "{}"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = run(t.Context(), []string{"verify", "--db", path}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("verify changed the database file")
	}
}

func TestVerifyReportsABrokenChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	log := receipt.NewLog(db)
	for range 3 {
		if _, err = log.Append(t.Context(), receipt.Receipt{Project: "p", Agent: "a", Session: "s", Tool: "t", Args: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE receipts SET tool = 'forged' WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out bytes.Buffer
	err = run(t.Context(), []string{"verify", "--db", path}, strings.NewReader(""), &out, io.Discard)
	if !errors.Is(err, errChainBroken) || !strings.Contains(err.Error(), "receipt 2") {
		t.Fatalf("err = %v, want a broken chain at receipt 2", err)
	}
}
