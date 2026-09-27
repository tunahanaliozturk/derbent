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
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/gate"
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

// lockedBuffer collects a child process's stderr, which exec copies on a goroutine of its own.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gateCommand is a gate process for agent. Its stderr, and its downstream servers', is printed if the
// test fails, so a failure says what the gate saw.
func gateCommand(t testing.TB, dir, agent, configPath string) *exec.Cmd {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "mcp", "--agent", agent,
		"--db", filepath.Join(dir, "p.db"), "--config", configPath, "--project", dir)
	cmd.Env = append(os.Environ(), "DERBENT_TEST_MAIN=1")
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("stderr of the %s gate:\n%s", agent, stderr.String())
		}
	})
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

// writeAllowConfig writes a config that allows every call and has no servers, and returns its path.
func writeAllowConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[[rule]]\naction = \"allow\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTwoAgentProcessesShareMemory(t *testing.T) {
	dir := t.TempDir()
	cfg := writeAllowConfig(t, dir)

	claude := connectProcess(t, dir, "claude", cfg)
	res, err := claude.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{
		"title": "Retry policy", "body": "Payment calls retry three times with jitter.",
	}})
	if err != nil || res.IsError {
		t.Fatalf("write: err %v, result %q", err, resultText(res))
	}
	if err = claude.Close(); err != nil {
		t.Fatal(err)
	}

	codex := connectProcess(t, dir, "codex", cfg)
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

// askedEcho starts a gate whose rules ask before echo__echo, calls the tool with text in the background
// and waits until the call is pending. It returns the database path, the pending approval and the call's result.
func askedEcho(t *testing.T, text string) (string, approval.Pending, <-chan called) {
	t.Helper()
	dir := t.TempDir()
	codex := connectProcess(t, dir, "codex", writeAskConfig(t, dir, "30s"))
	t.Cleanup(func() { codex.Close() })
	done := make(chan called, 1)
	go func() {
		res, err := codex.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo__echo", Arguments: map[string]any{"text": text}})
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
	path, p, done := askedEcho(t, "hi")
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
	path, p, done := askedEcho(t, "hi")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"approve", "--session", "--db", path, fmt.Sprint(p.ID)}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("approved echo__echo calls that rule %d asks about, for the rest of codex's session: %d", p.Rule, p.ID); !strings.Contains(out.String(), want) {
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
	id, granted, err := approval.NewQueue(db).Granted(t.Context(), "codex", p.Session, "echo__echo", p.RuleKey)
	if err != nil || !granted || id != p.ID {
		t.Fatalf("Granted = %d, %v, %v; want %d, true", id, granted, err, p.ID)
	}
}

// An approval has no rule key when its rule matched on an argument it could not read, or when a gate
// from before grants followed the rule, still running after the upgrade, asked. Such an approval grants
// nothing. approve --session refuses it, says why and how to approve it once, and the call keeps waiting
// until it is.
func TestApproveForTheSessionRefusesAnApprovalWithNoRuleKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecContext(t.Context(), `INSERT INTO approvals
		(created_ms, deadline_ms, project, agent, session, tool, args, rule) VALUES (?, ?, 'p', 'codex', 's', 'echo__echo', '{}', 1)`,
		time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
	var id int64
	if err == nil {
		id, err = res.LastInsertId()
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"approve", "--session", "--db", path, fmt.Sprint(id)}, strings.NewReader(""), io.Discard, io.Discard)
	if want := fmt.Sprintf("its rule could not read one of its arguments, or the gate that asked predates rule-scoped grants; "+
		"approve it once with derbent approve %d", id); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want one saying %q", err, want)
	}
	var out bytes.Buffer
	if err = run(t.Context(), []string{"approve", "--db", path, fmt.Sprint(id)}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("approving once after the refusal: %v", err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("approved %d", id)) {
		t.Fatalf("approve output: %q", out.String())
	}
}

// The id can be written as the UI shows it, #12.
func TestDenyRefusesTheWaitingCall(t *testing.T) {
	path, p, done := askedEcho(t, "hi")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"deny", "--db", path, fmt.Sprintf("#%d", p.ID)}, strings.NewReader(""), &out, io.Discard); err != nil {
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
	for _, args := range [][]string{{"approve"}, {"deny", "twelve"}, {"deny", "#"}, {"deny", "##5"}} {
		if err := run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Errorf("run %v succeeded", args)
		}
	}
	err := run(t.Context(), []string{"approve", "5", "--session"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "before the id") {
		t.Fatalf("a flag after the id: err = %v, want one saying flags go before the id", err)
	}
}

// --session grants what A does: the tool's calls that the same rule asks about under the same rules
// above it, since a grant is keyed on the rule and every rule above it (ADR 0011).
func TestApproveHelpNamesWhatASessionApprovalCovers(t *testing.T) {
	var stderr bytes.Buffer
	if err := run(t.Context(), []string{"approve", "-h"}, strings.NewReader(""), io.Discard, &stderr); err == nil {
		t.Fatal("approve -h succeeded")
	}
	if want := "calls that the same rule asks about under the same rules above it, for the rest"; !strings.Contains(stderr.String(), want) {
		t.Fatalf("help lacks %q:\n%s", want, stderr.String())
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

// pending shows each waiting call with its whole arguments, read from what a real gate process wrote.
func TestPendingListsWaitingCallsWithTheirWholeArguments(t *testing.T) {
	text := strings.Repeat("x", 3000) + "END"
	path, p, done := askedEcho(t, text)
	var table bytes.Buffer
	if err := run(t.Context(), []string{"pending", "--db", path}, strings.NewReader(""), &table, io.Discard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], fmt.Sprintf("#%d  codex  echo__echo  ", p.ID)) ||
		!strings.HasSuffix(lines[0], " left") || lines[1] != `    {"text":"`+text+`"}` {
		t.Fatalf("pending table:\n%s", table.String())
	}
	var js bytes.Buffer
	if err := run(t.Context(), []string{"pending", "--db", path, "--json"}, strings.NewReader(""), &js, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got pendingLine
	if err := json.Unmarshal(js.Bytes(), &got); err != nil {
		t.Fatalf("not one JSON line: %v: %q", err, js.String())
	}
	if got.ID != p.ID || got.Agent != "codex" || got.Session != p.Session || got.Tool != "echo__echo" || got.Args != `{"text":"`+text+`"}` {
		t.Fatalf("pending line = %+v", got)
	}
	for _, at := range []string{got.Created, got.Deadline} {
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Errorf("%q is not RFC 3339: %v", at, err)
		}
	}
	if err := run(t.Context(), []string{"deny", "--db", path, fmt.Sprint(p.ID)}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	callResult(t, done)
}

// A waiting call's agent, tool and arguments come from an agent, so pending escapes them as receipts
// does, in rows and in JSON lines.
func TestPendingEscapesAgentText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	agent, tool, args := "co\x1b[2Jdex", "gh\u009b2Jissue\U0000202e"+sneaky, "{\"x\":\"\x1b]52;c;ZXZpbA==\x07\nnext"+sneaky+"\"}"
	_, err = db.ExecContext(t.Context(), `INSERT INTO approvals
		(created_ms, deadline_ms, project, agent, session, tool, args, rule) VALUES (?, ?, 'p', ?, 's', ?, ?, 1)`,
		time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli(), agent, tool, args)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	var table bytes.Buffer
	if err = run(t.Context(), []string{"pending", "--db", path}, strings.NewReader(""), &table, io.Discard); err != nil {
		t.Fatal(err)
	}
	out := table.String()
	if strings.ContainsAny(out, "\x1b\a\u009b\U0000202e"+sneaky) || strings.Count(out, "\n") != 2 {
		t.Fatalf("raw control characters reached the table:\n%q", out)
	}
	for _, want := range []string{`co\u001b[2Jdex`, `gh\u009b2Jissue` + "\\u202e" + sneakyEscaped, `\u001b]52;c;ZXZpbA==\u0007\nnext` + sneakyEscaped} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%q", want, out)
		}
	}
	var js bytes.Buffer
	if err = run(t.Context(), []string{"pending", "--db", path, "--json"}, strings.NewReader(""), &js, io.Discard); err != nil {
		t.Fatal(err)
	}
	line := js.String()
	if strings.ContainsAny(line, "\u009b\U0000202e"+sneaky) || strings.Count(line, "\n") != 1 {
		t.Fatalf("raw control characters reached the JSON line: %q", line)
	}
	var got pendingLine
	if err = json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the line is not JSON: %v: %q", err, line)
	}
	if got.Agent != agent || got.Tool != tool || got.Args != args {
		t.Fatalf("decoded %+v; want agent %q, tool %q, args %q", got, agent, tool, args)
	}
}

// sneaky holds runes a terminal draws as nothing or uses to change how text reads: a tag character, a
// zero-width space, a zero-width joiner, a line separator and a variation selector.
const (
	sneaky        = "\U000e0041\U0000200b\U0000200d\U00002028\U0000fe0f"
	sneakyEscaped = "\\U000e0041\\u200b\\u200d\\u2028\\ufe0f"
)

// Tool names are stored as the agent sent them, so the table must not hand an agent's control
// characters to the user's terminal: no fake rows, no escape sequences, no reordered or hidden text.
func TestReceiptsTableEscapesStoredText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = receipt.NewLog(db).Append(t.Context(), receipt.Receipt{
		Project: "p", Agent: "codex", Session: "s", Tool: "evil\n\x1b]52;c;ZXZpbA==\x07\U0000202e" + sneaky,
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
	if strings.ContainsAny(out, "\x1b\a\U0000202e"+sneaky) || strings.Count(out, "\n") != 2 {
		t.Fatalf("raw control characters reached the table:\n%q", out)
	}
	if want := `evil\n\u001b]52;c;ZXZpbA==\u0007` + "\\u202e" + sneakyEscaped; !strings.Contains(out, want) {
		t.Fatalf("table lacks the escaped tool name %s:\n%q", want, out)
	}
}

// encoding/json escapes only the C0 controls, so JSON lines need the same care as the table: an agent
// writes the args, and DEL, a C1 control, a bidirectional override or an invisible rune must not reach a
// terminal raw.
func TestReceiptsJSONEscapesWhatATerminalActsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	tool, args := "gh\u009b2Jissue\U0000202e"+sneaky, "{\"x\":\"\u009b2J\U0000202egnp.exe\x7f\U00002066"+sneaky+"\"}"
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
	if strings.ContainsAny(line, "\u009b\U0000202e\x7f\U00002066"+sneaky) {
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
	for _, args := range [][]string{
		{"receipts", "--db", path, "codex", "--json"}, {"verify", "--db", path, "extra"}, {"pending", "--db", path, "extra"},
	} {
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

// A mistyped --db must fail and name the path, not open an empty database that never shows a call.
func TestUINeedsAnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	path := filepath.Join(dir, "p.db")
	err := run(t.Context(), []string{"--db", path}, strings.NewReader("q"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want an error naming %s", err, path)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the UI created %s", dir)
	}
}

func TestUIOpensAndQuits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--db", path}, strings.NewReader("q"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "RECEIPTS") {
		t.Fatalf("the UI drew nothing recognisable: %q", out.String())
	}
}

// runHook starts this test binary as `derbent gate` with stdin and returns its stdout, stderr and exit
// code, as a CLI's hook runner would see them. It may run on a goroutine of its own, so a process that
// cannot start is reported with Errorf, and its code is -1.
func runHook(t testing.TB, dir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	base := []string{"gate", "--db", filepath.Join(dir, "p.db"), "--config", filepath.Join(dir, "config.toml")}
	cmd := exec.CommandContext(t.Context(), os.Args[0], append(base, args...)...)
	cmd.Env = append(os.Environ(), "DERBENT_TEST_MAIN=1")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Errorf("start the hook: %v", err)
		code = -1
	}
	return out.String(), errOut.String(), code
}

const hookConfig = `
[approvals]
timeout = "20s"

[[rule]]
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"

[[rule]]
tool   = "native__Bash"
args   = { command = "rm -rf*" }
action = "deny"

[[rule]]
action = "allow"
`

func writeHookConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(hookConfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

func claudeHookInput(dir, command string) string {
	in, _ := json.Marshal(map[string]any{
		"session_id": "sess-1", "cwd": dir, "hook_event_name": "PreToolUse",
		"tool_name": "Bash", "tool_input": map[string]any{"command": command},
	})
	return string(in)
}

func claudeToolInput(dir, tool string) string {
	in, _ := json.Marshal(map[string]any{"session_id": "sess-1", "cwd": dir, "tool_name": tool, "tool_input": map[string]any{}})
	return string(in)
}

type hookReceipt struct{ tool, project, decidedBy string }

// hookReceiptRows reads the receipts in dir's database, or none when no call ever created it.
func hookReceiptRows(t *testing.T, dir string) []hookReceipt {
	t.Helper()
	db, err := store.OpenExisting(t.Context(), filepath.Join(dir, "p.db"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), `SELECT tool, project, decided_by FROM receipts ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []hookReceipt
	for rows.Next() {
		var r hookReceipt
		if err = rows.Scan(&r.tool, &r.project, &r.decidedBy); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// awaitHookApproval waits until a hook call is pending in dir's database and returns its id.
func awaitHookApproval(t *testing.T, dir string) int64 {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := approval.NewQueue(db)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		p, err := q.Pending(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(p) > 0 {
			return p[0].ID
		}
	}
	t.Fatal("the hook never asked")
	return 0
}

func TestHookAllowedByARuleSaysNothing(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	out, errOut, code := runHook(t, dir, claudeHookInput(dir, "go test ./..."), "--agent", "claude")
	if code != 0 || out != "" {
		t.Fatalf("code %d, stdout %q, stderr %q; want exit 0 and no output", code, out, errOut)
	}
	if got := hookReceiptRows(t, dir); len(got) != 1 || got[0].tool != "native__Bash" || got[0].decidedBy != "rule:3" {
		t.Fatalf("receipts = %+v; the first hook call creates the database and records the call", got)
	}
}

// Claude Code reads the hook's stdout as one JSON object, so a deny must be exactly that: anything
// else there, even a log line, spoils the answer.
func TestHookDeniedByARule(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	out, errOut, code := runHook(t, dir, claudeHookInput(dir, "rm -rf /"), "--agent", "claude")
	if code != 0 {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got struct {
		HookSpecificOutput struct {
			HookEventName, PermissionDecision, PermissionDecisionReason string
		} `json:"hookSpecificOutput"`
	}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout %q is not the answer object: %v", out, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout %q holds more than one JSON object (err %v)", out, err)
	}
	if o := got.HookSpecificOutput; o.HookEventName != "PreToolUse" || o.PermissionDecision != "deny" ||
		!strings.Contains(o.PermissionDecisionReason, "native__Bash is not allowed") {
		t.Fatalf("answer = %+v", o)
	}
}

// The hook leaves Derbent's own tools, the memory tools and those of the servers in its config, to the
// MCP gate, which decides and records them. A name Derbent does not serve is decided as a native tool.
func TestHookSkipsDerbentsOwnTools(t *testing.T) {
	dir := t.TempDir()
	cfg := hookConfig + "\n[servers.echo]\ncommand = ['echo-server']\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var own []string
	for _, tool := range gate.MemoryTools {
		own = append(own, "mcp__derbent__"+tool)
	}
	for _, tool := range append(own, "mcp__derbent__echo__echo") {
		out, errOut, code := runHook(t, dir, claudeToolInput(dir, tool), "--agent", "claude")
		if code != 0 || out != "" {
			t.Fatalf("%s: code %d, stdout %q, stderr %q", tool, code, out, errOut)
		}
	}
	if got := hookReceiptRows(t, dir); len(got) != 0 {
		t.Fatalf("receipts = %+v; the gate's own tools get none from the hook", got)
	}
	out, errOut, code := runHook(t, dir, claudeToolInput(dir, "mcp__derbent__other__tool"), "--agent", "claude")
	if code != 0 || out != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if got := hookReceiptRows(t, dir); len(got) != 1 || got[0].tool != "native__mcp__derbent__other__tool" {
		t.Fatalf("receipts = %+v; a server not in the config is not Derbent's", got)
	}
}

func TestHookAskIsApprovedFromAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	type result struct {
		out, errOut string
		code        int
	}
	done := make(chan result, 1)
	go func() {
		out, errOut, code := runHook(t, dir, claudeHookInput(dir, "git push origin main"), "--agent", "claude")
		done <- result{out, errOut, code}
	}()
	id := awaitHookApproval(t, dir)
	if err := run(t.Context(), []string{"approve", "--db", filepath.Join(dir, "p.db"), fmt.Sprint(id)}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.code != 0 || !strings.Contains(r.out, `"permissionDecision":"allow"`) {
			t.Fatalf("code %d, stdout %q, stderr %q", r.code, r.out, r.errOut)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the hook did not return")
	}
}

// A hook stopped while its call waits for the user, as a CLI stops a hook that runs past its timeout,
// withdraws the approval and answers deny.
func TestHookStoppedWhileWaitingWithdrawsItsApproval(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"gate", "--agent", "claude", "--db", filepath.Join(dir, "p.db"), "--config", filepath.Join(dir, "config.toml")},
			strings.NewReader(claudeHookInput(dir, "git push")), &out, io.Discard)
	}()
	id := awaitHookApproval(t, dir)
	cancel()
	select {
	case err := <-done:
		if err != nil || !strings.Contains(out.String(), `"permissionDecision":"deny"`) || !strings.Contains(out.String(), "withdrawn") {
			t.Fatalf("err %v, stdout %q", err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stopped hook did not return")
	}
	if got := hookReceiptRows(t, dir); len(got) != 1 || got[0].decidedBy != fmt.Sprintf("withdrawn:%d", id) {
		t.Fatalf("receipts = %+v", got)
	}
}

func TestHookFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	out, errOut, code := runHook(t, dir, `{"tool_name":"Bash"}`, "--agent", "claude")
	if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "session") {
		t.Fatalf("missing session id: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[[rule]]\naction = \"maybe\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Under a config that does not load, the hook cannot tell which tools are Derbent's, so it denies
	// those too rather than skip them.
	for _, in := range []string{claudeHookInput(dir, "ls"), claudeToolInput(dir, "mcp__derbent__memory_write")} {
		out, errOut, code = runHook(t, dir, in, "--agent", "claude")
		if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "config") {
			t.Fatalf("broken config: code %d, stdout %q, stderr %q", code, out, errOut)
		}
	}
	// Once the CLI is known, a bad agent name is answered in that CLI's format.
	out, errOut, code = runHook(t, dir, claudeHookInput(dir, "ls"), "--agent", "Claude Code", "--cli", "claude")
	if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "--agent") {
		t.Fatalf("bad agent name: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	_, errOut, code = runHook(t, dir, claudeHookInput(dir, "ls"), "--agent", "someone")
	if code != 2 || !strings.Contains(errOut, "--cli") {
		t.Fatalf("unknown cli: code %d, stderr %q", code, errOut)
	}
}

// jsonText is s as it appears inside a JSON string, so a Windows path can be found in a hook's answer.
func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// A --config the user named that does not exist is a mistake, not a request to allow everything: the
// hook denies the call and names the path, and derbent mcp and config check refuse to start.
func TestAMissingConfigTheUserNamedIsAnError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "config.toml")
	out, errOut, code := runHook(t, dir, claudeHookInput(dir, "ls"), "--agent", "claude")
	if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, jsonText(missing)) {
		t.Fatalf("hook: code %d, stdout %q, stderr %q; want a deny naming %s", code, out, errOut, missing)
	}
	for _, args := range [][]string{
		{"mcp", "--agent", "claude", "--config", missing, "--db", filepath.Join(dir, "p.db"), "--project", dir},
		{"config", "check", "--config", missing},
	} {
		err := run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s: err = %v, want one naming %s", args[0], err, missing)
		}
	}
}

// useConfigDir points the user config directory at dir, on every platform, and returns the default
// config path under it.
func useConfigDir(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("APPDATA", dir)         // Windows
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // macOS
	path, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("default config path %s is not under %s", path, dir)
	}
	return path
}

// Without --config, a missing default file keeps the first-run behaviour: every call is allowed.
// derbent mcp says so once on stderr when it starts, config check says so, and the hook, which runs
// on every call, says nothing.
func TestWithoutAConfigFileEveryCallIsAllowed(t *testing.T) {
	dir := t.TempDir()
	path := useConfigDir(t, dir)
	db := filepath.Join(dir, "p.db")

	var errOut bytes.Buffer
	if err := run(t.Context(), []string{"mcp", "--agent", "claude", "--db", db, "--project", dir}, strings.NewReader(""), io.Discard, &errOut); err != nil {
		t.Fatalf("mcp: %v", err)
	}
	if want := "no config found at " + path + ", so every call is allowed"; !strings.Contains(errOut.String(), want) {
		t.Fatalf("mcp stderr = %q, want it to say %q", errOut.String(), want)
	}

	var out bytes.Buffer
	errOut.Reset()
	err := run(t.Context(), []string{"gate", "--agent", "claude", "--db", db}, strings.NewReader(claudeHookInput(dir, "rm -rf /")), &out, &errOut)
	if err != nil || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("hook: err %v, stdout %q, stderr %q; want the call allowed without a word", err, out.String(), errOut.String())
	}

	out.Reset()
	if err = run(t.Context(), []string{"config", "check"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("config check: %v", err)
	}
	if want := "config: " + path + " (not found: every call is allowed)"; !strings.Contains(out.String(), want) {
		t.Fatalf("config check output = %q, want it to say %q", out.String(), want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

// A hook whose answer cannot be written exits with status 2, which the CLIs take as "block this call".
// A call a rule allows has no answer to write, so it cannot fail that way.
func TestHookThatCannotWriteItsAnswerBlocks(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	for in, wantBlock := range map[string]bool{
		`{"tool_name":"Bash"}`:            true, // refused before any rule is read
		claudeHookInput(dir, "rm -rf /"):  true, // refused by a rule
		claudeHookInput(dir, "go test ."): false,
	} {
		err := run(t.Context(), []string{"gate", "--agent", "claude", "--db", filepath.Join(dir, "p.db"), "--config", filepath.Join(dir, "config.toml")},
			strings.NewReader(in), failingWriter{}, io.Discard)
		var block errBlock
		if errors.As(err, &block) != wantBlock || (err != nil) != wantBlock {
			t.Errorf("input %s: err = %v, want a block: %v", in, err, wantBlock)
		}
	}
}

func TestHookInputIsBounded(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	huge := `{"session_id":"s","cwd":"x","tool_name":"Bash","tool_input":{"command":"` + strings.Repeat("a", 17<<20) + `"}}`
	out, errOut, code := runHook(t, dir, huge, "--agent", "claude")
	if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "larger than") {
		t.Fatalf("code %d, stdout %.200q, stderr %q", code, out, errOut)
	}
}

// A CLI hands the secrets in an MCP server entry's env to the MCP server only, never to its hooks. A
// config whose servers need them must not lock the hook out, while derbent mcp and config check,
// which start those servers, still refuse to run without them.
func TestHookLoadsAConfigWhoseServerSecretsItLacks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := hookConfig + "\n[servers.github]\ncommand = ['github-mcp-server']\nenv = { TOKEN = \"${env:DERBENT_TEST_UNSET}\" }\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"Bash", "mcp__derbent__github__get_me"} {
		in := claudeHookInput(dir, "ls")
		if tool != "Bash" {
			in = claudeToolInput(dir, tool)
		}
		if out, errOut, code := runHook(t, dir, in, "--agent", "claude"); code != 0 || out != "" {
			t.Fatalf("%s: code %d, stdout %q, stderr %q; want no decision", tool, code, out, errOut)
		}
	}
	if got := hookReceiptRows(t, dir); len(got) != 1 || got[0].tool != "native__Bash" {
		t.Fatalf("receipts = %+v; want the Bash call only, the server's tool left to the MCP gate", got)
	}
	for _, args := range [][]string{
		{"mcp", "--agent", "claude", "--config", path, "--db", filepath.Join(dir, "p.db")},
		{"config", "check", "--config", path},
	} {
		err := run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "DERBENT_TEST_UNSET") {
			t.Errorf("%v: err = %v, want it to name DERBENT_TEST_UNSET", args[:2], err)
		}
	}
}

// The project is --project when given, else the directory the CLI reports, else the working directory,
// since Antigravity can report no workspace at all.
func TestHookProjectComesFromTheFlagThenTheCLIThenTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeHookConfig(t, dir)
	other := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	antigravity := `{"toolCall":{"name":"run_command","args":{}},"conversationId":"c1","workspacePaths":[]}`
	for _, c := range []struct {
		in   string
		args []string
	}{
		{claudeHookInput(dir, "ls"), []string{"--agent", "claude", "--project", other}},
		{claudeHookInput(dir, "ls"), []string{"--agent", "claude"}},
		{antigravity, []string{"--agent", "antigravity"}},
	} {
		if out, errOut, code := runHook(t, dir, c.in, c.args...); code != 0 || out != "" {
			t.Fatalf("%v: code %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
	got := hookReceiptRows(t, dir)
	for i, d := range []string{other, dir, wd} {
		want, err := config.ProjectKey(d)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[i].project != want {
			t.Fatalf("receipts = %+v; call %d should be in project %s", got, i, want)
		}
	}
}
