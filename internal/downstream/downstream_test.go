package downstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	switch {
	case os.Getenv("PORTCULLIS_TEST_HANG") != "":
		_, _ = io.Copy(io.Discard, os.Stdin) // reads, never answers, and exits when stdin closes
		os.Exit(0)
	case os.Getenv("PORTCULLIS_TEST_JUNK") != "":
		fmt.Println("starting up, not a JSON-RPC message")
	}
	s := newTestServer()
	if os.Getenv("PORTCULLIS_TEST_NO_TOOLS") != "" {
		s = mcp.NewServer(&mcp.Implementation{Name: "no-tools", Version: "0"}, nil)
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	// Run returns once stdin is closed: the client shut the session down properly.
	if marker := os.Getenv("PORTCULLIS_TEST_EXIT_MARKER"); marker != "" {
		_ = os.WriteFile(marker, nil, 0o600)
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

func newManager(t *testing.T, specs ...downstream.Spec) (*downstream.Manager, *toolLists) {
	t.Helper()
	lists := &toolLists{}
	m := downstream.New(specs, lists.record, downstream.Options{
		Version: "test", ConnectTimeout: 20 * time.Second, MinBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
	})
	t.Cleanup(m.Close)
	return m, lists
}

// start starts the servers and waits until each has had its first attempt.
func start(t *testing.T, specs ...downstream.Spec) (*downstream.Manager, *toolLists) {
	t.Helper()
	m, lists := newManager(t, specs...)
	m.Start(t.Context())
	select {
	case <-m.Started():
	case <-time.After(30 * time.Second):
		t.Fatal("the servers' first attempts did not finish")
	}
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

// A redirect would carry the configured headers to wherever it points, so it is refused.
func TestRedirectIsRefusedAndHeadersStayHome(t *testing.T) {
	var mu sync.Mutex
	var leaked []string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, "nothing here", http.StatusNotFound)
	}))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	m, _ := start(t, downstream.Spec{Name: "web", URL: redirecting.URL, Headers: map[string]string{"Authorization": "Bearer t0ken-value"}})
	if _, err := callText(t, m, "web", "echo", `{"text":"x"}`); !errors.Is(err, downstream.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable for a server that only redirects", err)
	}
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 {
		t.Fatalf("the redirect target received %d requests, Authorization %q", len(leaked), leaked)
	}
}

// Start must not hold the gate for a server that is slow to come up: the agent's session starts at
// once, other servers work, and Close still ends the attempt that is connecting.
func TestSlowServerHoldsNothingButItsOwnStart(t *testing.T) {
	m, _ := newManager(t, stdioSpec("slow", map[string]string{"PORTCULLIS_TEST_HANG": "1"}), stdioSpec("test", nil))
	began := time.Now()
	m.Start(t.Context())
	if took := time.Since(began); took > time.Second {
		t.Fatalf("Start took %s", took)
	}
	eventually(t, "the other server to work", func() bool {
		got, err := callText(t, m, "test", "echo", `{"text":"meanwhile"}`)
		return err == nil && got == "echo:meanwhile"
	})
	select {
	case <-m.Started():
		t.Fatal("Started closed while a server was still on its first attempt")
	default:
	}
	began = time.Now()
	m.Close()
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("Close took %s with a server still connecting", took)
	}
}

func TestCloseLetsServersExitOnTheirOwn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "exited")
	m, _ := start(t, stdioSpec("test", map[string]string{"PORTCULLIS_TEST_EXIT_MARKER": marker}))
	if _, err := callText(t, m, "test", "echo", `{"text":"up"}`); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the server was killed instead of being let to exit when its stdin closed")
	}
}

func TestServerWithoutToolsIsUpWithNone(t *testing.T) {
	_, lists := start(t, stdioSpec("bare", map[string]string{"PORTCULLIS_TEST_NO_TOOLS": "1"}))
	names, loads := lists.get("bare")
	if loads != 1 || len(names) != 0 {
		t.Fatalf("tools = %v after %d loads, want an empty list once", names, loads)
	}
	time.Sleep(300 * time.Millisecond) // many backoffs, if it were being restarted
	if _, again := lists.get("bare"); again != 1 {
		t.Fatalf("the server was loaded %d times; one without tools is not broken", again)
	}
}

func TestServerPrintingJunkToStdoutHarmsOnlyItself(t *testing.T) {
	m, _ := start(t, stdioSpec("junk", map[string]string{"PORTCULLIS_TEST_JUNK": "1"}), stdioSpec("test", nil))
	if got, err := callText(t, m, "test", "echo", `{"text":"fine"}`); err != nil || got != "echo:fine" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	// Whether the SDK skips the line or drops the session, the junk server must take nothing else down.
	_, _ = callText(t, m, "junk", "echo", `{"text":"x"}`)
	if got, err := callText(t, m, "test", "echo", `{"text":"still"}`); err != nil || got != "echo:still" {
		t.Fatalf("echo after the junk server = %q, %v", got, err)
	}
}
