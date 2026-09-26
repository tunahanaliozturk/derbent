// Package downstream runs the user's MCP servers behind the gate: it keeps each one connected, keeps its
// tool list current, forwards calls to it, and starts it again when it stops.
package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrUnavailable is returned by Call for a server that is not connected at the moment.
var ErrUnavailable = errors.New("server is not running")

// Spec describes one server. Exactly one of Command and URL is set.
type Spec struct {
	Name    string
	Command []string
	Env     map[string]string // added to the gate's own environment for a command server
	URL     string
	Headers map[string]string // sent with every request to a url server
}

// ToolsFunc receives a server's whole tool list every time it is loaded: after each connection and
// after each list_changed notification from the server. It may be called from several goroutines.
type ToolsFunc func(server string, tools []*mcp.Tool)

// Options tune a Manager. Zero values get the defaults noted on each field.
type Options struct {
	Version      string
	Stderr       io.Writer     // a command server's stderr; nil discards it
	StartTimeout time.Duration // how long Start waits for every server's first attempt; default 10s
	MinBackoff   time.Duration // wait before the first retry; default 1s
	MaxBackoff   time.Duration // longest wait between retries; default 60s
	Logger       *slog.Logger  // default slog.Default()
}

// Manager supervises the servers. Its zero value is not usable; call New.
type Manager struct {
	specs   []Spec
	onTools ToolsFunc
	opts    Options
	// http is shared by the url servers and owned by the Manager, so Close can drop its idle
	// connections instead of leaving them to the process-wide default transport.
	http *http.Transport

	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
	closed   bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// New returns a Manager for specs. Nothing starts until Start.
func New(specs []Spec, onTools ToolsFunc, opts Options) *Manager {
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 10 * time.Second
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	return &Manager{
		specs: specs, onTools: onTools, opts: opts, http: transport.Clone(),
		sessions: map[string]*mcp.ClientSession{},
	}
}

// Start begins supervising every server and returns once each has finished its first connection
// attempt, or StartTimeout has passed, so that the agent's first tool list already holds the servers
// that came up. Supervision goes on until Close.
func (m *Manager) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	ready := make(chan struct{}, len(m.specs))
	for _, s := range m.specs {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.supervise(ctx, s, ready)
		}()
	}
	timer := time.NewTimer(m.opts.StartTimeout)
	defer timer.Stop()
	for range m.specs {
		select {
		case <-ready:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Close stops supervision, disconnects every server, ends command servers' processes, and waits for
// every goroutine the Manager started. Calling it more than once is harmless.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	sessions := m.sessions
	m.sessions = map[string]*mcp.ClientSession{}
	m.mu.Unlock()
	for _, cs := range sessions {
		cs.Close()
	}
	m.wg.Wait()
	m.http.CloseIdleConnections()
}

// Call forwards one tool call to server. A server that is not connected gives ErrUnavailable; its
// supervisor is already working on bringing it back.
func (m *Manager) Call(ctx context.Context, server, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	m.mu.Lock()
	cs := m.sessions[server]
	m.mu.Unlock()
	if cs == nil {
		return nil, fmt.Errorf("%s: %w", server, ErrUnavailable)
	}
	params := &mcp.CallToolParams{Name: tool}
	if len(args) > 0 {
		params.Arguments = args
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("call %s on %s: %w", tool, server, err)
	}
	return res, nil
}

// supervise keeps one server connected until ctx ends: connect, load tools, wait for the session to
// end, back off, and go again. The backoff doubles on every failure up to MaxBackoff, and starts over
// after a session that lasted at least 30 seconds.
func (m *Manager) supervise(ctx context.Context, s Spec, ready chan<- struct{}) {
	backoff := m.opts.MinBackoff
	first := true
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		began := time.Now()
		cs, err := m.connect(ctx, s)
		if err == nil {
			if err = m.loadTools(ctx, s.Name, cs); err != nil {
				cs.Close()
			}
		}
		if first {
			ready <- struct{}{}
			first = false
		}
		switch {
		case err == nil && m.setSession(s.Name, cs):
			waitErr := cs.Wait()
			m.clearSession(s.Name, cs)
			// A session whose server has gone still holds a goroutine for its notification
			// subscription until it is closed.
			cs.Close()
			if ctx.Err() != nil {
				return
			}
			m.opts.Logger.Warn("portcullis: downstream server stopped", "server", s.Name, "err", waitErr)
			if time.Since(began) >= 30*time.Second {
				backoff = m.opts.MinBackoff
			}
		case err != nil && ctx.Err() == nil:
			m.opts.Logger.Warn("portcullis: downstream server could not start", "server", s.Name, "err", err)
		}
		if ctx.Err() != nil {
			return
		}
		timer.Reset(backoff)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, m.opts.MaxBackoff)
	}
}

func (m *Manager) connect(ctx context.Context, s Spec) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "portcullis", Version: m.opts.Version}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { m.reload(ctx, s.Name) },
	})
	var transport mcp.Transport
	if s.URL != "" {
		transport = &mcp.StreamableClientTransport{
			Endpoint:   s.URL,
			HTTPClient: &http.Client{Transport: headerTransport{headers: s.Headers, base: m.http}},
		}
	} else {
		cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...) //nolint:gosec // the command is the user's own configured server
		cmd.Env = os.Environ()
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stderr = m.opts.Stderr
		transport = &mcp.CommandTransport{Command: cmd}
	}
	connectCtx, cancel := context.WithTimeout(ctx, m.opts.StartTimeout)
	defer cancel()
	cs, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", s.Name, err)
	}
	return cs, nil
}

// reload lists a connected server's tools again after it said they changed. It runs on its own
// goroutine because it makes a request from inside a notification handler.
func (m *Manager) reload(ctx context.Context, server string) {
	m.mu.Lock()
	cs := m.sessions[server]
	if m.closed || cs == nil {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		if err := m.loadTools(ctx, server, cs); err != nil && ctx.Err() == nil {
			m.opts.Logger.Warn("portcullis: could not reload tools", "server", server, "err", err)
		}
	}()
}

func (m *Manager) loadTools(ctx context.Context, server string, cs *mcp.ClientSession) error {
	var tools []*mcp.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools of %s: %w", server, err)
		}
		tools = append(tools, t)
	}
	m.onTools(server, tools)
	return nil
}

// setSession records cs as server's session, or closes it and reports false when the Manager has
// already been closed, so a connection that finishes during Close is not leaked.
func (m *Manager) setSession(server string, cs *mcp.ClientSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		cs.Close()
		return false
	}
	m.sessions[server] = cs
	return true
}

func (m *Manager) clearSession(server string, cs *mcp.ClientSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[server] == cs {
		delete(m.sessions, server)
	}
}

// headerTransport adds the configured headers to every request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}
