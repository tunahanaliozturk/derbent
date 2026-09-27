package gate_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/rule"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type env struct {
	db        *sql.DB
	receipts  *receipt.Log
	memory    *memory.Store
	approvals *approval.Queue
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &env{db: db, receipts: receipt.NewLog(db), memory: memory.NewStore(db), approvals: approval.NewQueue(db)}
}

func (e *env) gate(t *testing.T, agent string, specs ...rule.Spec) *gate.Gate {
	t.Helper()
	if len(specs) == 0 {
		specs = []rule.Spec{{Action: rule.Allow}}
	}
	set, err := rule.Compile(specs)
	if err != nil {
		t.Fatal(err)
	}
	return &gate.Gate{
		Agent: agent, Project: "/work/shop", Session: agent + "-session", Version: "test",
		Rules: set, Memory: e.memory, Receipts: e.receipts,
		Approvals: e.approvals, ApprovalTimeout: 10 * time.Second,
	}
}

func connect(t *testing.T, g *gate.Gate) *mcp.ClientSession {
	t.Helper()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := g.Server().Connect(t.Context(), serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(t.Context(), clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		_ = ss.Wait() // the session ends because the client closed it; only the wait matters here
	})
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func decode(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", text(res))
	}
	if err := json.Unmarshal([]byte(text(res)), v); err != nil {
		t.Fatalf("decode %q: %v", text(res), err)
	}
}

type receiptRow struct{ agent, tool, args, decision, outcome string }

func receipts(t *testing.T, db *sql.DB) []receiptRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT agent, tool, args, decision, outcome FROM receipts ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []receiptRow
	for rows.Next() {
		var r receiptRow
		if err := rows.Scan(&r.agent, &r.tool, &r.args, &r.decision, &r.outcome); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListsMemoryTools(t *testing.T) {
	cs := connect(t, newEnv(t).gate(t, "claude"))
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := []string{"memory_read", "memory_search", "memory_write"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestNotesPassBetweenAgents(t *testing.T) {
	e := newEnv(t)
	claude := connect(t, e.gate(t, "claude"))
	codex := connect(t, e.gate(t, "codex"))

	var written struct {
		ID int64 `json:"id"`
	}
	decode(t, call(t, claude, "memory_write", map[string]any{
		"title": "Use PostgreSQL 17", "body": "The ledger moves to PostgreSQL 17; SQLite stays for tests.",
		"tags": []string{"database"},
	}), &written)

	var found struct {
		Notice string `json:"notice"`
		Hits   []struct {
			ID     int64  `json:"id"`
			Author string `json:"author"`
		} `json:"hits"`
	}
	decode(t, call(t, codex, "memory_search", map[string]any{"query": "postgresql"}), &found)
	if len(found.Hits) != 1 || found.Hits[0].ID != written.ID || found.Hits[0].Author != "claude" || found.Notice == "" {
		t.Fatalf("search = %+v", found)
	}

	var read struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	}
	decode(t, call(t, codex, "memory_read", map[string]any{"id": written.ID}), &read)
	if read.Author != "claude" || !strings.Contains(read.Body, "PostgreSQL 17") {
		t.Fatalf("read = %+v", read)
	}
}

func TestEveryCallGetsAReceipt(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "claude"))
	call(t, cs, "memory_write", map[string]any{"title": "t", "body": "b"})
	if res := call(t, cs, "memory_write", map[string]any{"title": "no body"}); !res.IsError {
		t.Fatal("a write without a body succeeded")
	}
	if res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "nope"}); err != nil || !res.IsError {
		t.Fatalf("an unknown tool: result %+v, err %v; want a tool error", res, err)
	}
	got := receipts(t, e.db)
	want := []receiptRow{
		{"claude", "memory_write", `{"body":"b","title":"t"}`, "allow", "ok"},
		{"claude", "memory_write", `{"title":"no body"}`, "allow", "error"},
		{"claude", "nope", `{}`, "deny", "refused"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("receipts =\n%+v\nwant\n%+v", got, want)
	}
	res, err := e.receipts.Verify(t.Context())
	if err != nil || res.FirstBad != 0 || res.Count != 3 {
		t.Fatalf("Verify = %+v, err %v", res, err)
	}
}

// canonicalResult is how anyone holding a result the agent received recomputes the bytes its receipt
// hashed (ADR 0004): the content, structuredContent and isError fields, keys sorted, numbers as
// written, no HTML escaping.
func canonicalResult(t *testing.T, res *mcp.CallToolResult) []byte {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&full); err != nil {
		t.Fatal(err)
	}
	picked := map[string]any{}
	for _, k := range []string{"content", "structuredContent", "isError"} {
		if v, ok := full[k]; ok {
			picked[k] = v
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(picked); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func TestResultHashCanBeRecomputedFromWhatTheAgentReceived(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "claude"))
	results := []*mcp.CallToolResult{
		call(t, cs, "memory_write", map[string]any{"title": "Escaping <b> & co", "body": "a <tag> & an ampersand"}),
		call(t, cs, "memory_search", map[string]any{"query": "ampersand"}),
		call(t, cs, "memory_write", map[string]any{"title": "no body"}),
	}
	for i, res := range results {
		var size int64
		var sum string
		if err := e.db.QueryRowContext(t.Context(), `SELECT result_size, result_sha256 FROM receipts WHERE seq = ?`, i+1).
			Scan(&size, &sum); err != nil {
			t.Fatal(err)
		}
		b := canonicalResult(t, res)
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != sum || int64(len(b)) != size {
			t.Errorf("receipt %d: hash %s size %d, recomputed %x size %d from %s", i+1, sum, size, got, len(b), b)
		}
	}
}

func breakReceipts(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER no_receipts BEFORE INSERT ON receipts
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
}

// A tool that ran but whose receipt failed has left its effect behind. The agent must hear that, or
// it retries and does the thing twice.
func TestCallThatRanButWasNotRecordedSaysSo(t *testing.T) {
	e := newEnv(t)
	breakReceipts(t, e.db)
	cs := connect(t, e.gate(t, "claude"))
	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"title": "t", "body": "b"}})
	if err == nil || !strings.Contains(err.Error(), "memory_write ran") || !strings.Contains(err.Error(), "do not repeat") {
		t.Fatalf("err = %v, want it to say the call ran and must not be repeated", err)
	}
	var n int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memories`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("notes = %d, err %v, want the write to have happened", n, err)
	}
}

func TestRefusedCallThatWasNotRecordedSaysItDidNotRun(t *testing.T) {
	e := newEnv(t)
	breakReceipts(t, e.db)
	cs := connect(t, e.gate(t, "claude", rule.Spec{Tool: "memory_write", Action: rule.Deny}, rule.Spec{Action: rule.Allow}))
	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"title": "t", "body": "b"}})
	if err == nil || !strings.Contains(err.Error(), "did not run") {
		t.Fatalf("err = %v, want it to say the call did not run", err)
	}
}

// checkDeniedToolRefused reports how a plain deny rule failed to protect memory_write, or returns nil
// when the tool was hidden, refused, recorded as refused, and nothing was written.
func checkDeniedToolRefused(t *testing.T) error {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "copilot",
		rule.Spec{Agent: "copilot", Tool: "memory_write", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	))
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "memory_write" {
			return errors.New("memory_write is listed for copilot")
		}
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"title": "t", "body": "b"}})
	if err != nil {
		return fmt.Errorf("call was not refused by the gate: %w", err)
	}
	if !res.IsError || !strings.Contains(text(res), "not allowed") {
		return fmt.Errorf("call was not refused: %q", text(res))
	}
	var n int
	if err := e.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memories`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%d notes were written", n)
	}
	if got := receipts(t, e.db); len(got) != 1 || got[0].decision != "deny" || got[0].outcome != "refused" {
		return fmt.Errorf("receipts = %+v", got)
	}
	return nil
}

func TestDeniedToolIsHiddenAndRefused(t *testing.T) {
	if err := checkDeniedToolRefused(t); err != nil {
		t.Fatal(err)
	}
}

func TestDeniedToolCheckFailsWithoutRules(t *testing.T) {
	gate.SwitchOffRules(t)
	if checkDeniedToolRefused(t) == nil {
		t.Fatal("the check still passes with the rules switched off")
	}
}

func TestDeniedToolCheckFailsWithoutHiding(t *testing.T) {
	gate.SwitchOffHiding(t)
	if checkDeniedToolRefused(t) == nil {
		t.Fatal("the check still passes with hiding switched off")
	}
}

func TestArgsRuleRefusesOnlyMatchingCalls(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "claude",
		rule.Spec{Tool: "memory_search", Args: map[string]string{"query": "secret*"}, Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	))
	if res := call(t, cs, "memory_search", map[string]any{"query": "secret plans"}); !res.IsError {
		t.Fatal("a matching search was not refused")
	}
	if res := call(t, cs, "memory_search", map[string]any{"query": "public plans"}); res.IsError {
		t.Fatalf("a non-matching search was refused: %s", text(res))
	}
	got := receipts(t, e.db)
	if len(got) != 2 || got[0].outcome != "refused" || got[1].outcome != "ok" {
		t.Fatalf("receipts = %+v", got)
	}
}
