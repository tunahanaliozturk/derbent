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
	"derbent approve [--session] <id> | derbent deny <id> | derbent receipts | derbent verify | derbent config check | " +
	"derbent version")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "derbent:", err)
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
	case "pending":
		return runPending(ctx, args[1:], stdout, stderr)
	case "approve", "deny":
		return runDecide(ctx, args[0], args[1:], stdout, stderr)
	case "receipts":
		return runReceipts(ctx, args[1:], stdout, stderr)
	case "verify":
		return runVerify(ctx, args[1:], stdout, stderr)
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
