// Command derbent is an agentic gate: the one MCP server that several coding agents connect to.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

var errUsage = errors.New("usage: derbent [--db path] | derbent mcp --agent <name> | derbent pending | " +
	"derbent approve [--session] <id> | derbent deny <id> | derbent grants | derbent revoke <id>|--all | " +
	"derbent suggest [--min N] | derbent pins [show|accept] | derbent handoffs [--all] | derbent receipts | derbent verify [--file path [--head hash]] | derbent explain --agent <name> --tool <name> [--args <json>] | derbent config check | derbent gate --agent <name> | derbent init [--cli <list>] [--preset <name>] [--yes] [--dry-run] | derbent doctor [--cli <list>] [--config path] | derbent version")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "derbent:", err)
		var block errBlock
		if errors.As(err, &block) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runUI(ctx, args, stdin, stdout, stderr)
	}
	switch args[0] {
	case "mcp":
		return runMCP(ctx, args[1:], stdin, stdout, stderr)
	case "gate":
		return runGate(ctx, args[1:], stdin, stdout, stderr)
	case "init":
		return runInit(ctx, args[1:], stdin, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "pending":
		return runPending(ctx, args[1:], stdout, stderr)
	case "approve", "deny":
		return runDecide(ctx, args[0], args[1:], stdout, stderr)
	case "grants":
		return runGrants(ctx, args[1:], stdout, stderr)
	case "revoke":
		return runRevoke(ctx, args[1:], stdout, stderr)
	case "suggest":
		return runSuggest(ctx, args[1:], stdout, stderr)
	case "pins":
		return runPins(ctx, args[1:], stdout, stderr)
	case "handoffs":
		return runHandoffs(ctx, args[1:], stdout, stderr)
	case "receipts":
		return runReceipts(ctx, args[1:], stdout, stderr)
	case "verify":
		return runVerify(ctx, args[1:], stdin, stdout, stderr)
	case "explain":
		return runExplain(ctx, args[1:], stdout, stderr)
	case "config":
		if len(args) < 2 || args[1] != "check" {
			return fmt.Errorf("unknown config command: %w", errUsage)
		}
		return runConfigCheck(ctx, args[2:], stdout, stderr)
	case "version":
		_, err := fmt.Fprintln(stdout, version)
		return err
	default:
		return fmt.Errorf("unknown command %q: %w", args[0], errUsage)
	}
}
