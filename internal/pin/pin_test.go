package pin_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/pin"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

func open(t *testing.T) (*pin.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return pin.NewStore(db), path
}

func tool(name, desc string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: desc, InputSchema: map[string]any{"type": "object"}}
}

// The canonical form is what every pin is taken over, so it is fixed here byte for byte: changing it
// would make every stored pin look changed.
func TestDefinitionIsCanonical(t *testing.T) {
	got, err := pin.Definition(&mcp.Tool{
		Name: "get_me", Title: "Me", Description: "Who am I <b> & you",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"b": map[string]any{"type": "number", "maximum": 3.0}, "a": map[string]any{"type": "string"},
		}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"annotations":{"idempotentHint":false,"readOnlyHint":true},"description":"Who am I <b> & you",` +
		`"inputSchema":{"properties":{"a":{"type":"string"},"b":{"maximum":3,"type":"number"}},"type":"object"},` +
		`"name":"get_me","outputSchema":null,"title":"Me"}`
	if got != want {
		t.Fatalf("Definition =\n%s\nwant\n%s", got, want)
	}
}

// A server that sends the same tool with its keys in another order, or other spacing, sends the same
// definition.
func TestDefinitionDoesNotDependOnKeyOrderOrSpacing(t *testing.T) {
	var a, b mcp.Tool
	if err := json.Unmarshal([]byte(`{"name":"run","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}}}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{ "inputSchema" : { "properties" : { "y" : {"type":"integer"}, "x":{"type":"string"} }, "type":"object" },
		"description":"d", "name":"run" }`), &b); err != nil {
		t.Fatal(err)
	}
	da, errA := pin.Definition(&a)
	db, errB := pin.Definition(&b)
	if errA != nil || errB != nil || da != db {
		t.Fatalf("definitions differ:\n%s\n%s\n(%v, %v)", da, db, errA, errB)
	}
}

func TestCheckPinsOnFirstUseAndRecordsAChange(t *testing.T) {
	s, _ := open(t)
	changed, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", "v1"), tool("list", "v1")})
	if err != nil || len(changed) != 0 {
		t.Fatalf("first check = %v, %v; want both pinned", changed, err)
	}
	changed, err = s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", "v2")})
	if err != nil || !changed["get_me"] || len(changed) != 1 {
		t.Fatalf("second check = %v, %v; want get_me changed", changed, err)
	}
	p, err := s.Get(t.Context(), "github", "get_me")
	if err != nil || p.State() != pin.Changed || !strings.Contains(p.Definition, `"v1"`) ||
		!strings.Contains(p.NewDefinition, `"v2"`) || p.ChangedAt.IsZero() {
		t.Fatalf("pin = %+v, %v", p, err)
	}
	// A tool the server no longer lists keeps its pin.
	if p, err = s.Get(t.Context(), "github", "list"); err != nil || p.State() != pin.Pinned {
		t.Fatalf("the missing tool's pin = %+v, %v", p, err)
	}
	if n, err := s.CountChanged(t.Context()); err != nil || n != 1 {
		t.Fatalf("CountChanged = %d, %v", n, err)
	}
}

func TestTheServerGoingBackDropsTheChange(t *testing.T) {
	s, _ := open(t)
	for _, desc := range []string{"v1", "v2"} {
		if _, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", desc)}); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", "v1")})
	if err != nil || len(changed) != 0 {
		t.Fatalf("check with the pinned definition again = %v, %v", changed, err)
	}
	if p, err := s.Get(t.Context(), "github", "get_me"); err != nil || p.State() != pin.Pinned || p.NewSHA256 != "" {
		t.Fatalf("pin = %+v, %v; want the change dropped", p, err)
	}
}

func TestAcceptMakesTheNewDefinitionThePin(t *testing.T) {
	s, _ := open(t)
	for _, desc := range []string{"v1", "v2"} {
		if _, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", desc)}); err != nil {
			t.Fatal(err)
		}
	}
	def, err := pin.Definition(tool("get_me", "v2"))
	if err != nil {
		t.Fatal(err)
	}
	sum := pin.Sum(def)
	p, err := s.Accept(t.Context(), "github", "get_me", sum)
	if err != nil || p.SHA256 != sum {
		t.Fatalf("Accept = %+v, %v; want the v2 definition's hash", p, err)
	}
	changed, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", "v2")})
	if err != nil || len(changed) != 0 {
		t.Fatalf("check after accept = %v, %v", changed, err)
	}
	if _, err = s.Accept(t.Context(), "github", "get_me", sum); !errors.Is(err, pin.ErrNotChanged) {
		t.Fatalf("accepting twice: err = %v, want ErrNotChanged", err)
	}
	if _, err = s.Accept(t.Context(), "github", "nope", sum); !errors.Is(err, pin.ErrNoPin) {
		t.Fatalf("accepting an unknown tool: err = %v, want ErrNoPin", err)
	}
}

// The user accepts the change they reviewed: a hash that is not the recorded change's is refused, and
// the pin stays as it was. So is a prefix of the recorded change's own hash: a hostile server controls
// every definition of its tool and can find two whose hashes share a short prefix, show one for review
// and send the other.
func TestAcceptRefusesAChangeThatIsNotTheOneGiven(t *testing.T) {
	s, _ := open(t)
	for _, desc := range []string{"v1", "v2"} {
		if _, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", desc)}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Get(t.Context(), "github", "get_me")
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := pin.Definition(tool("get_me", "v3")) // what the user saw before the server changed again
	if err != nil {
		t.Fatal(err)
	}
	for _, given := range []string{pin.Sum(reviewed), before.SHA256, before.NewSHA256[:8], before.NewSHA256[:63], ""} {
		if _, err = s.Accept(t.Context(), "github", "get_me", given); !errors.Is(err, pin.ErrOtherChange) {
			t.Errorf("Accept(%q): err = %v, want ErrOtherChange", given, err)
		}
	}
	if after, err := s.Get(t.Context(), "github", "get_me"); err != nil || after != before {
		t.Fatalf("pin after refused accepts = %+v, %v; want %+v", after, err, before)
	}
}

// Recheck, which a running gate calls for a tool it withholds, writes only when no change is recorded,
// so two gates holding different definitions never overwrite each other's record.
func TestRecheckRecordsAChangeOnlyWhenNoneIsRecorded(t *testing.T) {
	s, _ := open(t)
	for _, desc := range []string{"v1", "v2"} {
		if _, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("get_me", desc)}); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err := s.Get(t.Context(), "github", "get_me")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.Recheck(t.Context(), "github", tool("get_me", "v3"))
	if err != nil || !changed {
		t.Fatalf("Recheck(v3) = %v, %v; want changed", changed, err)
	}
	p, err := s.Get(t.Context(), "github", "get_me")
	if err != nil || p != recorded {
		t.Fatalf("pin = %+v, %v; want the recorded v2 change left alone", p, err)
	}
	if _, err = s.Accept(t.Context(), "github", "get_me", recorded.NewSHA256); err != nil {
		t.Fatal(err)
	}
	if changed, err = s.Recheck(t.Context(), "github", tool("get_me", "v2")); err != nil || changed {
		t.Fatalf("Recheck(v2) after accepting v2 = %v, %v; want not changed", changed, err)
	}
	if changed, err = s.Recheck(t.Context(), "github", tool("get_me", "v3")); err != nil || !changed {
		t.Fatalf("Recheck(v3) after accepting v2 = %v, %v; want changed", changed, err)
	}
	if p, err = s.Get(t.Context(), "github", "get_me"); err != nil || !strings.Contains(p.NewDefinition, `"v3"`) {
		t.Fatalf("pin = %+v, %v; want v3 recorded now that no change was", p, err)
	}
}

// config check reads pins from a database it opens read-only, so State must write nothing.
func TestStateWritesNothing(t *testing.T) {
	s, path := open(t)
	for _, desc := range []string{"v1", "v2"} {
		if _, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("changed", desc)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Check(t.Context(), "github", []*mcp.Tool{tool("same", "v1")}); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	read := pin.NewStore(ro)
	for _, tc := range []struct {
		tool *mcp.Tool
		want pin.State
	}{{tool("new", "v1"), pin.New}, {tool("same", "v1"), pin.Pinned}, {tool("changed", "v2"), pin.Changed}, {tool("same", "v9"), pin.Changed}} {
		if got, err := read.State(t.Context(), "github", tc.tool); err != nil || got != tc.want {
			t.Errorf("State(%s %s) = %q, %v; want %q", tc.tool.Name, tc.tool.Description, got, err, tc.want)
		}
	}
}

// Two gates that start at the same moment on a fresh database pin each tool once and both serve it.
func TestConcurrentChecksPinEachToolOnce(t *testing.T) {
	s, _ := open(t)
	tools := []*mcp.Tool{tool("a", "v1"), tool("b", "v1"), tool("c", "v1")}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	changes := make([]int, 4)
	for i := range 4 {
		wg.Go(func() {
			changed, err := s.Check(t.Context(), "github", tools)
			errs[i], changes[i] = err, len(changed)
		})
	}
	wg.Wait()
	for i := range 4 {
		if errs[i] != nil || changes[i] != 0 {
			t.Fatalf("check %d = %d changed, %v", i, changes[i], errs[i])
		}
	}
	if list, err := s.List(t.Context()); err != nil || len(list) != 3 {
		t.Fatalf("pins = %d, %v; want 3", len(list), err)
	}
}
