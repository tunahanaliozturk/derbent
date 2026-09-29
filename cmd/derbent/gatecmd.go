package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/hook"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
)

// maxHookInput bounds what a hook call reads from standard input. A tool call larger than this is
// refused rather than read without limit.
const maxHookInput = 16 << 20

// errBlock makes main exit with status 2, which Claude Code and Codex treat as "block this call"
// even when nothing could be written in their answer format: the CLI is unknown, or writing the answer
// failed.
type errBlock struct{ err error }

func (e errBlock) Error() string { return e.err.Error() }
func (e errBlock) Unwrap() error { return e.err }

// runGate answers one call from an agent CLI's pre-tool hook. Once the CLI's protocol is known, every
// failure answers deny with the reason: the gate fails closed.
func runGate(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	agent := flags.String("agent", "", "name of the agent this hook serves, as in its rules")
	cli := flags.String("cli", "", "hook protocol: "+strings.Join(hook.Names(), ", ")+" (default: the agent name)")
	server := flags.String("server", "derbent", "the name of Derbent's MCP server entry in the CLI, whose tools the hook skips")
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	projectDir := flags.String("project", "", "project directory (default: the git root of the directory the CLI reports)")
	if err := flags.Parse(args); err != nil {
		return errBlock{err}
	}
	if *cli == "" {
		*cli = *agent
	}
	p, ok := hook.Lookup(*cli)
	if !ok {
		return errBlock{fmt.Errorf("gate: --cli must be one of %s (got %q; it defaults to --agent)", strings.Join(hook.Names(), ", "), *cli)}
	}
	answer := func(a gate.HookAnswer) error {
		out := p.Answer(a)
		if len(out) == 0 {
			return nil // no decision: nothing to write
		}
		if _, err := stdout.Write(out); err != nil {
			return errBlock{fmt.Errorf("write the hook answer: %w", err)}
		}
		return nil
	}
	deny := func(err error) error {
		return answer(gate.HookAnswer{Verdict: gate.Denied, Reason: "derbent: " + err.Error()})
	}
	if !agentName.MatchString(*agent) {
		return deny(fmt.Errorf("--agent must be 1 to 32 lower-case letters, digits, dashes or underscores, got %q", *agent))
	}
	in, err := io.ReadAll(io.LimitReader(stdin, maxHookInput+1))
	if err != nil {
		return deny(fmt.Errorf("read the hook input: %w", err))
	}
	if len(in) > maxHookInput {
		return deny(fmt.Errorf("the hook input is larger than %d bytes", maxHookInput))
	}
	call, err := p.Parse(in)
	if err != nil {
		return deny(err)
	}
	// The config says which tools are Derbent's, so it loads first: under a config that does not load,
	// Derbent's own tools are denied with the rest rather than skipped. A missing default file allows
	// every call without a warning, since the hook runs on every call; a missing --config is an error.
	cfg, _, _, err := loadConfig(*configPath, config.LoadForHook)
	if err != nil {
		return deny(err)
	}
	if p.Own(*server, call, servedBy(cfg)) {
		return nil // the MCP gate decides and records its own tools
	}
	dir := *projectDir
	if dir == "" {
		dir = call.Dir
	}
	if dir == "" { // Antigravity can report no workspace
		if dir, err = os.Getwd(); err != nil {
			return deny(fmt.Errorf("find working directory: %w", err))
		}
	}
	project, err := config.ProjectKey(dir)
	if err != nil {
		return deny(err)
	}
	root, err := config.CheckoutRoot(dir)
	if err != nil {
		return deny(err)
	}
	db, err := openDB(ctx, *dbPath)
	if err != nil {
		return deny(err)
	}
	defer db.Close()
	g := &gate.Gate{
		Agent: *agent, Project: project, Session: call.Session, Version: version,
		Rules: cfg.Rules, Budgets: cfg.Budgets, Memory: memory.NewStore(db), Receipts: receipt.NewLog(db),
		Redact: cfg.Redact.JSON, Approvals: approval.NewQueue(db), ApprovalTimeout: cfg.ApprovalTimeout,
		ProjectRules: config.NewProjectRules(root),
		Stop:         ctx, // SIGINT or SIGTERM withdraws the call if it waits for the user
	}
	ans, err := g.Hook(ctx, "native__"+call.Tool, call.Args)
	if werr := answer(ans); werr != nil {
		return errors.Join(werr, err)
	}
	if err != nil {
		fmt.Fprintln(stderr, "derbent:", err)
	}
	return nil
}

// servedBy reports whether the MCP gate under cfg may serve a tool: one of Derbent's own (gate.OwnTool), or
// <server>__<tool> for a server in cfg. The hook starts no servers, so it cannot know their exact tools.
func servedBy(cfg config.Config) func(tool string) bool {
	return func(tool string) bool {
		_, _, ok := downstreamTool(cfg, tool)
		return gate.OwnTool(tool) || ok
	}
}

// downstreamTool splits a gate tool name <server>__<name> and reports whether server is one of cfg's servers
// and name is not empty. The hook and derbent explain both ask it which names belong to a downstream
// server.
func downstreamTool(cfg config.Config, tool string) (server, name string, ok bool) {
	server, name, _ = strings.Cut(tool, "__")
	return server, name, name != "" && slices.ContainsFunc(cfg.Servers, func(s config.Server) bool { return s.Name == server })
}
