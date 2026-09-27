package gate_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/redact"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

type forwarded struct{ server, tool, args string }

type fakeRemote struct {
	mu    sync.Mutex
	calls []forwarded
	err   error
}

func (f *fakeRemote) forward(_ context.Context, server, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, forwarded{server, tool, string(args)})
	if f.err != nil {
		return nil, f.err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "from " + server + "/" + tool}}}, nil
}

func objectTool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: name, InputSchema: map[string]any{"type": "object"}}
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		if !strings.HasPrefix(tool.Name, "memory_") {
			names = append(names, tool.Name)
		}
	}
	slices.Sort(names)
	return names
}

func remoteGate(t *testing.T, e *env, agent string, remote *fakeRemote, specs ...rule.Spec) (*gate.Gate, *mcp.ClientSession) {
	t.Helper()
	g := e.gate(t, agent, specs...)
	g.Forward = remote.forward
	cs := connect(t, g)
	return g, cs
}

func TestDownstreamToolsAreNamespacedAndForwarded(t *testing.T) {
	e := newEnv(t)
	remote := &fakeRemote{}
	g, cs := remoteGate(t, e, "claude", remote)
	g.SyncTools(t.Context(), "github", []*mcp.Tool{objectTool("get_me"), objectTool("create_issue")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__create_issue", "github__get_me"}) {
		t.Fatalf("tools = %v", got)
	}
	res := call(t, cs, "github__get_me", map[string]any{"verbose": true})
	if text(res) != "from github/get_me" {
		t.Fatalf("result = %q", text(res))
	}
	if len(remote.calls) != 1 || remote.calls[0] != (forwarded{"github", "get_me", `{"verbose":true}`}) {
		t.Fatalf("forwarded = %+v", remote.calls)
	}
	if got := receipts(t, e.db); len(got) != 1 || got[0].tool != "github__get_me" || got[0].outcome != "ok" {
		t.Fatalf("receipts = %+v", got)
	}
}

func TestSyncRemovesToolsThatAreGoneAndTellsTheAgent(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude")
	g.Forward = (&fakeRemote{}).forward
	changed := make(chan struct{}, 8)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := g.Server().Connect(t.Context(), serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	}).Connect(t.Context(), clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		_ = ss.Wait()
	})
	g.SyncTools(t.Context(), "github", []*mcp.Tool{objectTool("get_me"), objectTool("create_issue")})
	g.SyncTools(t.Context(), "github", []*mcp.Tool{objectTool("get_me")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
		t.Fatalf("tools = %v", got)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent was not told the tool list changed")
	}
}

func TestUnservableToolsAreLeftOut(t *testing.T) {
	e := newEnv(t)
	g, cs := remoteGate(t, e, "claude", &fakeRemote{})
	g.SyncTools(t.Context(), "x", []*mcp.Tool{
		objectTool("fine"),
		objectTool("has space"),
		objectTool(strings.Repeat("a", 62)),
		{Name: "string_schema", InputSchema: map[string]any{"type": "string"}},
		{Name: "no_schema"},
	})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"x__fine"}) {
		t.Fatalf("tools = %v", got)
	}
}

func TestHiddenDownstreamToolIsRefusedByName(t *testing.T) {
	e := newEnv(t)
	remote := &fakeRemote{}
	g, cs := remoteGate(t, e, "copilot", remote,
		rule.Spec{Agent: "copilot", Tool: "github__create_*", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	g.SyncTools(t.Context(), "github", []*mcp.Tool{objectTool("get_me"), objectTool("create_issue")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
		t.Fatalf("tools = %v", got)
	}
	if res := call(t, cs, "github__create_issue", map[string]any{"title": "x"}); !res.IsError {
		t.Fatal("a hidden tool called by name was not refused")
	}
	if len(remote.calls) != 0 {
		t.Fatalf("a refused call reached the server: %+v", remote.calls)
	}
}

func TestForwardErrorBecomesAToolError(t *testing.T) {
	e := newEnv(t)
	remote := &fakeRemote{err: errors.New("github: server is not running")}
	g, cs := remoteGate(t, e, "claude", remote)
	g.SyncTools(t.Context(), "github", []*mcp.Tool{objectTool("get_me")})
	res := call(t, cs, "github__get_me", nil)
	if !res.IsError || !strings.Contains(text(res), "not running") {
		t.Fatalf("result = %q, want a tool error", text(res))
	}
	if got := receipts(t, e.db); len(got) != 1 || got[0].outcome != "error" {
		t.Fatalf("receipts = %+v", got)
	}
}

// checkArgumentsAreRedacted reports a secret that reached a stored receipt, or returns nil.
func checkArgumentsAreRedacted(t *testing.T) error {
	e := newEnv(t)
	red, err := redact.New([]string{`ghp_[A-Za-z0-9]{36}`}, nil)
	if err != nil {
		return err
	}
	g := e.gate(t, "claude")
	g.Redact = red.JSON
	cs := connect(t, g)
	token := "ghp_" + strings.Repeat("Z", 36)
	call(t, cs, "memory_write", map[string]any{"title": "token", "body": "the token is " + token})
	got := receipts(t, e.db)
	if len(got) != 1 {
		return errors.New("no receipt")
	}
	if strings.Contains(got[0].args, token) {
		return errors.New("the token is in the stored arguments: " + got[0].args)
	}
	if !strings.Contains(got[0].args, redact.Mask) {
		return errors.New("nothing was masked: " + got[0].args)
	}
	return nil
}

func TestArgumentsAreRedacted(t *testing.T) {
	if err := checkArgumentsAreRedacted(t); err != nil {
		t.Fatal(err)
	}
}

func TestRedactionCheckFailsWithoutRedaction(t *testing.T) {
	gate.SwitchOffRedaction(t)
	if checkArgumentsAreRedacted(t) == nil {
		t.Fatal("the check still passes with redaction switched off")
	}
}

func TestServableName(t *testing.T) {
	for name, want := range map[string]bool{
		"github__get_me": true, "a-b_c": true, strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false, "has space": false, "dot.ted": false, "": false,
	} {
		if got := gate.ServableName(name); got != want {
			t.Errorf("ServableName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLeftOutSaysWhy(t *testing.T) {
	tests := map[string]struct {
		name string
		tool *mcp.Tool
		want string
	}{
		"servable":         {"x__fine", objectTool("fine"), ""},
		"bad name":         {"x__has space", objectTool("has space"), "not a tool name"},
		"string schema":    {"x__s", &mcp.Tool{Name: "s", InputSchema: map[string]any{"type": "string"}}, "not an object"},
		"no schema at all": {"x__n", &mcp.Tool{Name: "n"}, "not an object"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := gate.LeftOut(tc.name, tc.tool)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("LeftOut = %q, want %q", got, tc.want)
			}
		})
	}
}

// Rules read named string arguments. Arguments that are not an object would pass a deny whose args
// condition cannot see them, so they are refused before any rule is consulted.
func TestArgumentsThatAreNotAnObjectAreRefused(t *testing.T) {
	e := newEnv(t)
	remote := &fakeRemote{}
	g, cs := remoteGate(t, e, "codex", remote,
		rule.Spec{Tool: "x__run", Args: map[string]string{"command": "git push*"}, Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	g.SyncTools(t.Context(), "x", []*mcp.Tool{objectTool("run")})
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "x__run", Arguments: []any{"git push --force"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "not a JSON object") {
		t.Fatalf("result = %s", text(res))
	}
	if len(remote.calls) != 0 {
		t.Fatalf("forwarded %v", remote.calls)
	}
	if got := receipts(t, e.db); len(got) != 1 || got[0].decision != "deny" || got[0].outcome != "refused" {
		t.Fatalf("receipts = %+v", got)
	}
}

// The server comes up only once the listing is known to be waiting for it, so the listing can include
// it only by having waited.
func TestToolListWaitsForServersStartingUp(t *testing.T) {
	e := newEnv(t)
	waiting := make(chan struct{}, 1)
	gate.WhenWaitingForTools(t, func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	})
	g := e.gate(t, "claude")
	g.Forward = (&fakeRemote{}).forward
	ready := make(chan struct{})
	g.ToolsReady, g.ToolsWait = ready, 10*time.Second
	cs := connect(t, g) // initialize is answered at once
	type listing struct {
		res *mcp.ListToolsResult
		err error
	}
	listed := make(chan listing, 1)
	go func() {
		res, err := cs.ListTools(t.Context(), nil)
		listed <- listing{res, err}
	}()
	select {
	case <-waiting:
	case l := <-listed:
		t.Fatalf("the listing came back without waiting for the servers: %+v, %v", l.res, l.err)
	case <-time.After(10 * time.Second):
		t.Fatal("the listing never started waiting for the servers")
	}
	g.SyncTools(t.Context(), "x", []*mcp.Tool{objectTool("late")})
	close(ready)
	l := <-listed
	if l.err != nil {
		t.Fatal(l.err)
	}
	var got []string
	for _, tool := range l.res.Tools {
		if !strings.HasPrefix(tool.Name, "memory_") {
			got = append(got, tool.Name)
		}
	}
	if !slices.Equal(got, []string{"x__late"}) {
		t.Fatalf("tools = %v, want the server that came up during the wait", got)
	}
}

// The SDK builds a *mcp.CallToolRequest for every tools/call. Should that change, the gate refuses a
// call it cannot read instead of passing it on with no rule applied.
func TestACallTheGateCannotReadIsRefused(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude")
	passed := false
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		passed = true
		return &mcp.CallToolResult{}, nil
	}
	res, err := g.GateCalls(next)(t.Context(), "tools/call", &mcp.ListToolsRequest{})
	if passed || err == nil || res != nil {
		t.Fatalf("result %v, err %v, passed on %v; want it refused before the handler", res, err, passed)
	}
}

func TestToolListWaitsForASlowServerOnlySoLong(t *testing.T) {
	e := newEnv(t)
	g := e.gate(t, "claude")
	g.ToolsReady, g.ToolsWait = make(chan struct{}), 300*time.Millisecond
	cs := connect(t, g)
	began := time.Now()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 5*time.Second || len(res.Tools) != 3 {
		t.Fatalf("listed %d tools after %s; want the memory tools after at most the wait", len(res.Tools), took)
	}
}
