package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/downstream"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// firstListWait is how long the agent's first tool listing waits for downstream servers to come up. It
// stays well below Codex's default MCP startup timeout of ten seconds, which covers initialize and the
// first listing.
const firstListWait = 5 * time.Second

// runMCP serves one agent session over stdin and stdout until the agent disconnects. Nothing but MCP
// frames may be written to stdout.
func runMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	agent := flags.String("agent", "", "name of the agent this gate serves, such as claude or codex")
	projectDir := flags.String("project", "", "project directory (default: the git root of the working directory)")
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !agentName.MatchString(*agent) {
		return fmt.Errorf("mcp: --agent must be 1 to 32 lower-case letters, digits, dashes or underscores, got %q", *agent)
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *projectDir == "" {
		if *projectDir, err = os.Getwd(); err != nil {
			return fmt.Errorf("find working directory: %w", err)
		}
	}
	project, err := config.ProjectKey(*projectDir)
	if err != nil {
		return err
	}
	db, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	session, err := newSession()
	if err != nil {
		return err
	}
	g := &gate.Gate{
		Agent: *agent, Project: project, Session: session, Version: version,
		Rules: cfg.Rules, Memory: memory.NewStore(db), Receipts: receipt.NewLog(db),
		Redact: cfg.Redact.JSON, Approvals: approval.NewQueue(db), ApprovalTimeout: cfg.ApprovalTimeout,
		Stop: ctx, // SIGINT or SIGTERM withdraws the calls still waiting for the user
	}
	srv := g.Server()
	if len(cfg.Servers) > 0 {
		mgr := downstream.New(specsOf(cfg.Servers), g.SyncTools, downstream.Options{Version: version, Stderr: stderr})
		g.Forward = mgr.Call
		g.ToolsReady, g.ToolsWait = mgr.Started(), firstListWait
		mgr.Start(ctx)
		defer mgr.Close()
	}
	transport := &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}}
	if err := srv.Run(ctx, transport); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func specsOf(servers []config.Server) []downstream.Spec {
	specs := make([]downstream.Spec, 0, len(servers))
	for _, s := range servers {
		specs = append(specs, downstream.Spec{Name: s.Name, Command: s.Command, Env: s.Env, URL: s.URL, Headers: s.Headers})
	}
	return specs
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func loadConfig(path string) (config.Config, error) {
	if path == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			return config.Config{}, err
		}
		path = p
	}
	return config.Load(path)
}

// databasePath is the --db a command was given, or the default database when it was given none.
func databasePath(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return config.DefaultDBPath()
}

// openDB opens the database at path, or the default one, creating it if needed.
func openDB(ctx context.Context, path string) (*sql.DB, error) {
	path, err := databasePath(path)
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, path)
}

func newSession() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("new session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
