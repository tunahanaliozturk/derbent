package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// TestMain lets the end-to-end tests start this test binary as the derbent command, or as a small
// MCP server standing in for a user's downstream server.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv("DERBENT_TEST_ECHO") == "1":
		serveEcho()
	case os.Getenv("DERBENT_TEST_HANG") == "1":
		_, _ = io.Copy(io.Discard, os.Stdin) // a server that never answers, and exits when stdin closes
		os.Exit(0)
	case os.Getenv("DERBENT_TEST_MAIN") == "1":
		main()
		os.Exit(0)
	default:
		goleak.VerifyTestMain(m)
	}
}

type echoIn struct {
	Text string `json:"text"`
}

func serveEcho() {
	s := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Text}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "secret_tool"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crash"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		os.Exit(3)
		return nil, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// writeConfig writes a config with the echo stand-in as a downstream server and returns its path.
func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := `
[servers.echo]
command = ['` + os.Args[0] + `', '-test.run=^$']
env     = { DERBENT_TEST_ECHO = "1" }

[receipts]
redact = ['ghp_[A-Za-z0-9]{36}']

[[rule]]
agent  = "codex"
tool   = "echo__secret_tool"
action = "deny"

[[rule]]
action = "allow"
`
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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

func TestMCPNeedsAValidAgent(t *testing.T) {
	for _, args := range [][]string{{"mcp"}, {"mcp", "--agent", "Claude Code"}} {
		err := run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "--agent") {
			t.Errorf("run %v: err = %v, want an --agent error", args, err)
		}
	}
}

func gateCommand(t *testing.T, dir, agent, configPath string) *exec.Cmd {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "mcp", "--agent", agent,
		"--db", filepath.Join(dir, "p.db"), "--config", configPath, "--project", dir)
	cmd.Env = append(os.Environ(), "DERBENT_TEST_MAIN=1")
	return cmd
}

func connectProcess(t *testing.T, dir, agent, configPath string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).
		Connect(t.Context(), &mcp.CommandTransport{Command: gateCommand(t, dir, agent, configPath)}, nil)
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

	claude := connectProcess(t, dir, "claude", filepath.Join(dir, "absent.toml"))
	res, err := claude.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{
		"title": "Retry policy", "body": "Payment calls retry three times with jitter.",
	}})
	if err != nil || res.IsError {
		t.Fatalf("write: err %v, result %q", err, resultText(res))
	}
	if err = claude.Close(); err != nil {
		t.Fatal(err)
	}

	codex := connectProcess(t, dir, "codex", filepath.Join(dir, "absent.toml"))
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

func TestDownstreamServerThroughARealGate(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir)

	codex := connectProcess(t, dir, "codex", cfg)
	tools, err := codex.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "echo__echo") || slices.Contains(names, "echo__secret_tool") {
		t.Fatalf("tools = %v, want echo__echo listed and echo__secret_tool hidden from codex", names)
	}
	token := "ghp_" + strings.Repeat("Q", 36)
	res, err := codex.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo__echo", Arguments: map[string]any{"text": token}})
	if err != nil || res.IsError || resultText(res) != "echo:"+token {
		t.Fatalf("echo: err %v, result %q", err, resultText(res))
	}
	if err = codex.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenExisting(t.Context(), filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var args string
	if err = db.QueryRowContext(t.Context(), `SELECT args FROM receipts WHERE tool = 'echo__echo'`).Scan(&args); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(args, token) || !strings.Contains(args, "[redacted]") {
		t.Fatalf("stored args = %s, want the token masked", args)
	}
}

func TestConfigCheckListsServerTools(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run(t.Context(), []string{"config", "check", "--config", writeConfig(t, dir)}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("config check: %v\n%s", err, out.String())
	}
	for _, want := range []string{"rules: 2", "server echo: 3 tools", "echo__crash", "echo__echo", "echo__secret_tool"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestConfigCheckFailsForAServerThatDoesNotStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := "[servers.ghost]\ncommand = ['derbent-test-no-such-command']\n\n[[rule]]\naction = \"allow\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run(t.Context(), []string{"config", "check", "--config", path}, strings.NewReader(""), &out, io.Discard)
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out.String(), "server ghost: not running") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
}

// Codex gives an MCP server ten seconds to initialise and list its tools. A downstream server that
// never answers must not use them up: the agent's session starts at once and the tool list comes
// after the gate's own short wait, with every server that did come up.
func TestSlowServerDoesNotHoldTheAgentsSession(t *testing.T) {
	dir := t.TempDir()
	cfg := `
[servers.slow]
command = ['` + os.Args[0] + `', '-test.run=^$']
env     = { DERBENT_TEST_HANG = "1" }

[servers.echo]
command = ['` + os.Args[0] + `', '-test.run=^$']
env     = { DERBENT_TEST_ECHO = "1" }

[[rule]]
action = "allow"
`
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	codex := connectProcess(t, dir, "codex", path)
	defer codex.Close()
	tools, err := codex.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 8*time.Second {
		t.Fatalf("initialize and the first tool list took %s", took)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "echo__echo") || !slices.Contains(names, "memory_write") {
		t.Fatalf("tools = %v, want the memory tools and the server that came up", names)
	}
}

func TestServerCrashingMidCallGivesAToolErrorAndAReceipt(t *testing.T) {
	dir := t.TempDir()
	codex := connectProcess(t, dir, "codex", writeConfig(t, dir))
	res, err := codex.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo__crash", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("err %v; want a tool error the agent can read", err)
	}
	if !res.IsError {
		t.Fatalf("result %q; want a tool error", resultText(res))
	}
	if err = codex.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenExisting(t.Context(), filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var outcome string
	if err = db.QueryRowContext(t.Context(), `SELECT outcome FROM receipts WHERE tool = 'echo__crash'`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "error" {
		t.Fatalf("outcome = %q, want error", outcome)
	}
}

// writeAskConfig writes a config where calls to the echo stand-in need the user's approval.
func writeAskConfig(t *testing.T, dir, timeout string) string {
	t.Helper()
	cfg := `
[servers.echo]
command = ['` + os.Args[0] + `', '-test.run=^$']
env     = { DERBENT_TEST_ECHO = "1" }

[approvals]
timeout = "` + timeout + `"

[[rule]]
tool   = "echo__echo"
action = "ask"

[[rule]]
action = "allow"
`
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type called struct {
	res *mcp.CallToolResult
	err error
}

// askedEcho starts a gate whose rules ask before echo__echo, calls the tool in the background and waits
// until the call is pending. It returns the database path, the pending approval and the call's result.
func askedEcho(t *testing.T) (string, approval.Pending, <-chan called) {
	t.Helper()
	dir := t.TempDir()
	codex := connectProcess(t, dir, "codex", writeAskConfig(t, dir, "30s"))
	t.Cleanup(func() { codex.Close() })
	done := make(chan called, 1)
	go func() {
		res, err := codex.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo__echo", Arguments: map[string]any{"text": "hi"}})
		done <- called{res, err}
	}()

	path := filepath.Join(dir, "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := approval.NewQueue(db)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var pending []approval.Pending
		if pending, err = q.Pending(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(pending) > 0 {
			return path, pending[0], done
		}
	}
	t.Fatal("the gate never asked")
	return "", approval.Pending{}, nil
}

// callResult waits for the background call to come back and fails the test if it does not.
func callResult(t *testing.T, done <-chan called) *mcp.CallToolResult {
	t.Helper()
	select {
	case c := <-done:
		if c.err != nil {
			t.Fatalf("call: %v", c.err)
		}
		return c.res
	case <-time.After(15 * time.Second):
		t.Fatal("the decided call did not return")
		return nil
	}
}

// The design's evidence for approvals: a gate process waits on ask, a second process approves, and the
// call goes through.
func TestApprovalFromAnotherProcess(t *testing.T) {
	path, p, done := askedEcho(t)
	var out bytes.Buffer
	if err := run(t.Context(), []string{"approve", "--db", path, fmt.Sprint(p.ID)}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("approved %d", p.ID)) {
		t.Fatalf("approve output: %q", out.String())
	}
	if res := callResult(t, done); res.IsError || resultText(res) != "echo:hi" {
		t.Fatalf("call result %q", resultText(res))
	}
	err := run(t.Context(), []string{"deny", "--db", path, fmt.Sprint(p.ID)}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Fatalf("deciding twice: err = %v", err)
	}
}

func TestApproveForTheSessionLeavesAGrant(t *testing.T) {
	path, p, done := askedEcho(t)
	var out bytes.Buffer
	if err := run(t.Context(), []string{"approve", "--session", "--db", path, fmt.Sprint(p.ID)}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("approved for the rest of the session: %d", p.ID); !strings.Contains(out.String(), want) {
		t.Fatalf("approve output %q lacks %q", out.String(), want)
	}
	if res := callResult(t, done); res.IsError || resultText(res) != "echo:hi" {
		t.Fatalf("call result %q", resultText(res))
	}
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, granted, err := approval.NewQueue(db).Granted(t.Context(), "codex", p.Session, "echo__echo")
	if err != nil || !granted || id != p.ID {
		t.Fatalf("Granted = %d, %v, %v; want %d, true", id, granted, err, p.ID)
	}
}

func TestDenyRefusesTheWaitingCall(t *testing.T) {
	path, p, done := askedEcho(t)
	var out bytes.Buffer
	if err := run(t.Context(), []string{"deny", "--db", path, fmt.Sprint(p.ID)}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("denied %d", p.ID)) {
		t.Fatalf("deny output: %q", out.String())
	}
	if res := callResult(t, done); !res.IsError || !strings.Contains(resultText(res), "the user denied echo__echo") {
		t.Fatalf("call result %q, IsError %v", resultText(res), res.IsError)
	}
}

func TestApprovalTimeoutDeniesTheCall(t *testing.T) {
	dir := t.TempDir()
	codex := connectProcess(t, dir, "codex", writeAskConfig(t, dir, "1s"))
	defer codex.Close()
	res, err := codex.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo__echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "none came within 1s") {
		t.Fatalf("err %v, result %q", err, resultText(res))
	}
}

func TestApproveNeedsANumber(t *testing.T) {
	for _, args := range [][]string{{"approve"}, {"deny", "twelve"}} {
		if err := run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Errorf("run %v succeeded", args)
		}
	}
	err := run(t.Context(), []string{"approve", "5", "--session"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "before the id") {
		t.Fatalf("a flag after the id: err = %v, want one saying flags go before the id", err)
	}
}

func TestDecidingNeedsAnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	path := filepath.Join(dir, "p.db")
	for _, command := range []string{"approve", "deny"} {
		err := run(t.Context(), []string{command, "--db", path, "5"}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: err = %v, want an error naming %s", command, err, path)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deciding created %s", dir)
	}
}

// Tool names are stored as the agent sent them, so the table must not hand an agent's control
// characters to the user's terminal: no fake rows, no escape sequences, no reordered text.
func TestReceiptsTableEscapesStoredText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = receipt.NewLog(db).Append(t.Context(), receipt.Receipt{
		Project: "p", Agent: "codex", Session: "s", Tool: "evil\n\x1b]52;c;ZXZpbA==\x07\u202e",
		Args: "{}", Decision: "deny", DecidedBy: "rule:2", Outcome: "refused",
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	var table bytes.Buffer
	if err = run(t.Context(), []string{"receipts", "--db", path}, strings.NewReader(""), &table, io.Discard); err != nil {
		t.Fatal(err)
	}
	out := table.String()
	if strings.ContainsAny(out, "\x1b\a\u202e") || strings.Count(out, "\n") != 2 {
		t.Fatalf("raw control characters reached the table:\n%q", out)
	}
	if want := `"evil\n\x1b]52;c;ZXZpbA==\a\u202e"`; !strings.Contains(out, want) {
		t.Fatalf("table lacks the quoted tool name %s:\n%q", want, out)
	}
}

// encoding/json escapes only the C0 controls, so JSON lines need the same care as the table: an agent
// writes the args, and DEL, a C1 control or a bidirectional override must not reach a terminal raw.
func TestReceiptsJSONEscapesWhatATerminalActsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	tool, args := "gh\u009b2Jissue\u202e", "{\"x\":\"\u009b2J\u202egnp.exe\x7f\u2066\"}"
	_, err = receipt.NewLog(db).Append(t.Context(), receipt.Receipt{
		Project: "p", Agent: "codex", Session: "s", Tool: tool, Args: args, Decision: "deny", DecidedBy: "rule:2", Outcome: "refused",
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = run(t.Context(), []string{"receipts", "--db", path, "--json"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	line := out.String()
	if strings.ContainsAny(line, "\u009b\u202e\x7f\u2066") {
		t.Fatalf("raw control characters reached the JSON line: %q", line)
	}
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("want exactly one line: %q", line)
	}
	var got receiptLine
	if err = json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the line is not JSON: %v: %q", err, line)
	}
	if got.Tool != tool || got.Args != args {
		t.Fatalf("decoded tool %q, args %q; want %q, %q", got.Tool, got.Args, tool, args)
	}
}

func TestReceiptsCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	log := receipt.NewLog(db)
	for _, agent := range []string{"claude", "codex"} {
		if _, err = log.Append(t.Context(), receipt.Receipt{
			Project: "p", Agent: agent, Session: "s", Tool: "memory_write",
			Args: "{}", Decision: "allow", DecidedBy: "rule:1", Outcome: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	var table bytes.Buffer
	if err = run(t.Context(), []string{"receipts", "--db", path}, strings.NewReader(""), &table, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SEQ", "AGENT", "claude", "codex", "memory_write", "rule:1"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}

	var lines bytes.Buffer
	if err = run(t.Context(), []string{"receipts", "--db", path, "--json", "--agent", "codex"}, strings.NewReader(""), &lines, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(lines.String()), "\n")
	if len(got) != 1 || !strings.Contains(got[0], `"agent":"codex"`) || !strings.Contains(got[0], `"seq":2`) {
		t.Fatalf("json lines:\n%s", lines.String())
	}

	if err = run(t.Context(), []string{"receipts", "--db", path, "--since", "yesterday-ish"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("a bad --since was accepted")
	}

	// The flag package stops at the first word that is not a flag, so a stray word would silently drop
	// every flag after it.
	for _, args := range [][]string{{"receipts", "--db", path, "codex", "--json"}, {"verify", "--db", path, "extra"}} {
		err = run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), `"`+args[3]+`"`) {
			t.Errorf("run %v: err = %v, want one naming %q", args, err, args[3])
		}
	}
}

func TestUINamesAStrayArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	err := run(t.Context(), []string{"--db", path, "extra"}, strings.NewReader("q"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `"extra"`) {
		t.Fatalf("err = %v, want one naming %q", err, "extra")
	}
}

func TestUIOpensAndQuits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--db", path}, strings.NewReader("q"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "RECEIPTS") {
		t.Fatalf("the UI drew nothing recognisable: %q", out.String())
	}
}
