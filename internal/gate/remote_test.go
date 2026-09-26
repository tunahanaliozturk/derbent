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

	"github.com/tunahanaliozturk/portcullis/internal/gate"
	"github.com/tunahanaliozturk/portcullis/internal/redact"
	"github.com/tunahanaliozturk/portcullis/internal/rule"
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
	g.SyncTools("github", []*mcp.Tool{objectTool("get_me"), objectTool("create_issue")})
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
	g.SyncTools("github", []*mcp.Tool{objectTool("get_me"), objectTool("create_issue")})
	g.SyncTools("github", []*mcp.Tool{objectTool("get_me")})
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
	g.SyncTools("x", []*mcp.Tool{
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
	g.SyncTools("github", []*mcp.Tool{objectTool("get_me"), objectTool("create_issue")})
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
	g.SyncTools("github", []*mcp.Tool{objectTool("get_me")})
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
