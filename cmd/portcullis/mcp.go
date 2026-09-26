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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/portcullis/internal/config"
	"github.com/tunahanaliozturk/portcullis/internal/gate"
	"github.com/tunahanaliozturk/portcullis/internal/memory"
	"github.com/tunahanaliozturk/portcullis/internal/receipt"
	"github.com/tunahanaliozturk/portcullis/internal/store"
)

var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// runMCP serves one agent session over stdin and stdout until the agent disconnects. Nothing but MCP
// frames may be written to stdout.
func runMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	agent := flags.String("agent", "", "name of the agent this gate serves, such as claude or codex")
	projectDir := flags.String("project", "", "project directory (default: the git root of the working directory)")
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
	dbPath := flags.String("db", "", "database file (default: portcullis.db in the user state directory)")
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
	}
	transport := &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}}
	if err := g.Server().Run(ctx, transport); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
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

func openDB(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		p, err := config.DefaultDBPath()
		if err != nil {
			return nil, err
		}
		path = p
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
