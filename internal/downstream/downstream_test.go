package downstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/portcullis/internal/downstream"
)

// TestMain doubles as a stdio MCP server when PORTCULLIS_TEST_SERVER is set.
func TestMain(m *testing.M) {
	if os.Getenv("PORTCULLIS_TEST_SERVER") != "" {
		serveTestServer()
		return
	}
	goleak.VerifyTestMain(m)
}

type textIn struct {
	Text string `json:"text"`
}

func newTestServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Text}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "env"}, func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv(in.Text)}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crash"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		os.Exit(3)
		return nil, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "grow"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		mcp.AddTool(s, &mcp.Tool{Name: "late"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, nil
		})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "grown"}}}, nil, nil
	})
	return s
}

func serveTestServer() {
	if marker := os.Getenv("PORTCULLIS_TEST_FAIL_ONCE"); marker != "" {
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, nil, 0o600)
			os.Exit(1)
		}
	}
	if err := newTestServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func stdioSpec(name string, env map[string]string) downstream.Spec {
	e := map[string]string{"PORTCULLIS_TEST_SERVER": "1"}
	for k, v := range env {
		e[k] = v
	}
	return downstream.Spec{Name: name, Command: []string{os.Args[0], "-test.run=^$"}, Env: e}
}

// toolLists records the latest tool list per server.
type toolLists struct {
	mu    sync.Mutex
	lists map[string][]string
	calls int
}

func (l *toolLists) record(server string, tools []*mcp.Tool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lists == nil {
		l.lists = map[string][]string{}
	}
	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	l.lists[server] = names
	l.calls++
}

func (l *toolLists) get(server string) ([]string, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lists[server], l.calls
}

func start(t *testing.T, specs ...downstream.Spec) (*downstream.Manager, *toolLists) {
	t.Helper()
	lists := &toolLists{}
	m := downstream.New(specs, lists.record, downstream.Options{
		Version: "test", StartTimeout: 20 * time.Second, MinBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
	})
	m.Start(t.Context())
	t.Cleanup(m.Close)
	return m, lists
}

func callText(t *testing.T, m *downstream.Manager, server, tool, args string) (string, error) {
	t.Helper()
	res, err := m.Call(t.Context(), server, tool, json.RawMessage(args))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), nil
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStdioServerIsListedAndCalled(t *testing.T) {
	m, lists := start(t, stdioSpec("test", map[string]string{"PORTCULLIS_TEST_VALUE": "passed-through"}))
	if names, _ := lists.get("test"); !slices.Equal(names, []string{"crash", "echo", "env", "grow"}) {
		t.Fatalf("tools = %v", names)
	}
	if got, err := callText(t, m, "test", "echo", `{"text":"hi"}`); err != nil || got != "echo:hi" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	if got, err := callText(t, m, "test", "env", `{"text":"PORTCULLIS_TEST_VALUE"}`); err != nil || got != "passed-through" {
		t.Fatalf("env = %q, %v", got, err)
	}
}

func TestCrashedServerIsRestarted(t *testing.T) {
	m, lists := start(t, stdioSpec("test", nil))
	_, before := lists.get("test")
	if _, err := callText(t, m, "test", "crash", `{}`); err == nil {
		t.Fatal("a call that killed the server succeeded")
	}
	eventually(t, "the server to come back", func() bool {
		got, err := callText(t, m, "test", "echo", `{"text":"again"}`)
		return err == nil && got == "echo:again"
	})
	if _, after := lists.get("test"); after <= before {
		t.Fatal("the tool list was not loaded again after the restart")
	}
}

func TestListChangedReloadsTools(t *testing.T) {
	m, lists := start(t, stdioSpec("test", nil))
	if _, err := callText(t, m, "test", "grow", `{}`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new tool to be listed", func() bool {
		names, _ := lists.get("test")
		return slices.Contains(names, "late")
	})
}

func TestServerThatFailsToStartIsRetried(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "failed-once")
	m, _ := start(t, stdioSpec("test", map[string]string{"PORTCULLIS_TEST_FAIL_ONCE": marker}))
	eventually(t, "the second start to succeed", func() bool {
		got, err := callText(t, m, "test", "echo", `{"text":"up"}`)
		return err == nil && got == "echo:up"
	})
}

func TestMissingServerIsUnavailableButOthersWork(t *testing.T) {
	m, _ := start(t,
		downstream.Spec{Name: "ghost", Command: []string{"portcullis-test-no-such-command"}},
		stdioSpec("test", nil),
	)
	if _, err := callText(t, m, "ghost", "anything", `{}`); !errors.Is(err, downstream.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got, err := callText(t, m, "test", "echo", `{"text":"fine"}`); err != nil || got != "echo:fine" {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

func TestHTTPServerGetsItsHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return newTestServer() }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	m, _ := start(t, downstream.Spec{Name: "web", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer t0ken-value"}})
	if got, err := callText(t, m, "web", "echo", `{"text":"over http"}`); err != nil || got != "echo:over http" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || slices.ContainsFunc(seen, func(h string) bool { return h != "Bearer t0ken-value" }) {
		t.Fatalf("Authorization headers seen = %q", seen)
	}
}

func TestCloseStopsServers(t *testing.T) {
	m, _ := start(t, stdioSpec("test", nil))
	m.Close()
	if _, err := callText(t, m, "test", "echo", `{"text":"x"}`); !errors.Is(err, downstream.ErrUnavailable) {
		t.Fatalf("err = %v after Close, want ErrUnavailable", err)
	}
}
