package gate_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/pin"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

func described(name, desc string) *mcp.Tool {
	t := objectTool(name)
	t.Description = desc
	return t
}

// pinGate is a gate for agent that pins downstream tools in e's database, connected to a client that
// reports each list_changed on the returned channel.
func pinGate(t *testing.T, e *env, agent string, specs ...rule.Spec) (*gate.Gate, *mcp.ClientSession, <-chan struct{}) {
	t.Helper()
	g := e.gate(t, agent, specs...)
	g.Forward = (&fakeRemote{}).forward
	g.Pins = pin.NewStore(e.db)
	changed := make(chan struct{}, 16)
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
	return g, cs, changed
}

// watch runs g.WatchPins until the test ends.
func watch(t *testing.T, g *gate.Gate) {
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		g.WatchPins(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
}

func pinState(t *testing.T, e *env, server, tool string) pin.State {
	t.Helper()
	p, err := pin.NewStore(e.db).Get(t.Context(), server, tool)
	if errors.Is(err, pin.ErrNoPin) {
		return pin.New
	}
	if err != nil {
		t.Fatal(err)
	}
	return p.State()
}

func TestAToolIsPinnedOnFirstUseAndServed(t *testing.T) {
	e := newEnv(t)
	g, cs, _ := pinGate(t, e, "claude")
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
		t.Fatalf("tools = %v", got)
	}
	if s := pinState(t, e, "github", "get_me"); s != pin.Pinned {
		t.Fatalf("pin state = %q", s)
	}
}

func TestAChangedToolIsWithheldAndItsCallsRefused(t *testing.T) {
	e := newEnv(t)
	first, _, _ := pinGate(t, e, "claude")
	first.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1"), described("create_issue", "v1")})
	g, cs, _ := pinGate(t, e, "claude")
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v2"), described("create_issue", "v1")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__create_issue"}) {
		t.Fatalf("tools = %v, want get_me withheld", got)
	}
	res := call(t, cs, "github__get_me", nil)
	want := "derbent: github__get_me changed since it was pinned; the user can review it with derbent pins"
	if !res.IsError || text(res) != want {
		t.Fatalf("call = %q, want %q", text(res), want)
	}
	if by := decidedBy(t, e.db); !slices.Equal(by, []string{"pin"}) {
		t.Fatalf("decided_by = %v", by)
	}
	if s := pinState(t, e, "github", "get_me"); s != pin.Changed {
		t.Fatalf("pin state = %q", s)
	}
}

// A gate that is running serves an accepted tool again, and the agent hears of it through list_changed.
func TestAnAcceptedToolIsServedAgainWhileTheGateRuns(t *testing.T) {
	gate.SetPinRecheck(t, 20*time.Millisecond)
	e := newEnv(t)
	first, _, _ := pinGate(t, e, "claude")
	first.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1")})
	g, cs, changed := pinGate(t, e, "claude")
	watch(t, g)
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v2")})
	if got := toolNames(t, cs); len(got) != 0 {
		t.Fatalf("tools = %v, want get_me withheld", got)
	}
	if _, err := pin.NewStore(e.db).Accept(t.Context(), "github", "get_me"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent was not told the tool list changed")
	}
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
		t.Fatalf("tools after accept = %v", got)
	}
	if res := call(t, cs, "github__get_me", nil); res.IsError {
		t.Fatalf("call after accept: %s", text(res))
	}
}

func TestAServerSendingThePinnedDefinitionAgainIsServed(t *testing.T) {
	e := newEnv(t)
	first, _, _ := pinGate(t, e, "claude")
	first.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1")})
	g, cs, _ := pinGate(t, e, "claude")
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v2")})
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
		t.Fatalf("tools = %v", got)
	}
	if s := pinState(t, e, "github", "get_me"); s != pin.Pinned {
		t.Fatalf("pin state = %q, want the change dropped", s)
	}
}

func TestAServerWithPinningOffIsServedAsItComes(t *testing.T) {
	e := newEnv(t)
	for _, desc := range []string{"v1", "v2"} {
		g, cs, _ := pinGate(t, e, "claude")
		g.Unpinned = map[string]bool{"github": true}
		g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", desc)})
		if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
			t.Fatalf("%s: tools = %v", desc, got)
		}
	}
	if s := pinState(t, e, "github", "get_me"); s != pin.New {
		t.Fatalf("pin state = %q, want no pin", s)
	}
}

func TestAToolThatDisappearsKeepsItsPin(t *testing.T) {
	e := newEnv(t)
	g, _, _ := pinGate(t, e, "claude")
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("a", "v1"), described("b", "v1")})
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("a", "v1")})
	next, cs, _ := pinGate(t, e, "claude")
	next.SyncTools(t.Context(), "github", []*mcp.Tool{described("a", "v1"), described("b", "v2")})
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__a"}) {
		t.Fatalf("tools = %v, want b withheld on its return", got)
	}
}

// Pins are global: a tool the rules hide from this agent is pinned for the next agent that can see it.
func TestAToolHiddenFromThisAgentIsStillPinned(t *testing.T) {
	e := newEnv(t)
	g, cs, _ := pinGate(t, e, "codex", rule.Spec{Tool: "github__secret", Action: rule.Deny}, allowRest)
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("secret", "v1")})
	if got := toolNames(t, cs); len(got) != 0 {
		t.Fatalf("tools = %v, want secret hidden", got)
	}
	if s := pinState(t, e, "github", "secret"); s != pin.Pinned {
		t.Fatalf("pin state = %q", s)
	}
}

// A pin check that fails serves nothing unchecked: the server's tools are withheld.
func TestToolsWhosePinsCannotBeCheckedAreWithheld(t *testing.T) {
	e := newEnv(t)
	g, cs, _ := pinGate(t, e, "claude")
	if _, err := e.db.ExecContext(t.Context(), `DROP TABLE pins`); err != nil {
		t.Fatal(err)
	}
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1")})
	if got := toolNames(t, cs); len(got) != 0 {
		t.Fatalf("tools = %v, want them withheld", got)
	}
	if res := call(t, cs, "github__get_me", nil); !res.IsError || !strings.Contains(text(res), "could not be checked") {
		t.Fatalf("call = %q", text(res))
	}
}

// A running gate serves the withheld tools once their pins can be checked again.
func TestToolsWithheldUncheckedAreServedOnceTheirPinsCanBeChecked(t *testing.T) {
	gate.SetPinRecheck(t, 20*time.Millisecond)
	e := newEnv(t)
	g, cs, changed := pinGate(t, e, "claude")
	if _, err := e.db.ExecContext(t.Context(), `ALTER TABLE pins RENAME TO pins_away`); err != nil {
		t.Fatal(err)
	}
	g.SyncTools(t.Context(), "github", []*mcp.Tool{described("get_me", "v1")})
	if got := toolNames(t, cs); len(got) != 0 {
		t.Fatalf("tools = %v, want them withheld", got)
	}
	watch(t, g)
	if _, err := e.db.ExecContext(t.Context(), `ALTER TABLE pins_away RENAME TO pins`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent was not told the tool list changed")
	}
	if got := toolNames(t, cs); !slices.Equal(got, []string{"github__get_me"}) {
		t.Fatalf("tools = %v", got)
	}
	if s := pinState(t, e, "github", "get_me"); s != pin.Pinned {
		t.Fatalf("pin state = %q", s)
	}
}
