package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/portcullis/internal/config"
	"github.com/tunahanaliozturk/portcullis/internal/downstream"
	"github.com/tunahanaliozturk/portcullis/internal/gate"
)

var errCheckFailed = errors.New("config check failed")

// runConfigCheck loads the config, starts every downstream server once, and prints the tools each one
// would give the agents, so a mistake shows up here rather than in the middle of an agent session.
func runConfigCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("config check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path := *configPath
	if path == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			return err
		}
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	note := ""
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		note = " (not found, using defaults)"
	}
	if _, err = fmt.Fprintf(stdout, "config: %s%s\nrules: %d\n", path, note, cfg.Rules.Len()); err != nil {
		return err
	}

	var mu sync.Mutex
	listed := map[string][]string{}
	collect := func(server string, tools []*mcp.Tool) {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, server+"__"+t.Name)
		}
		slices.Sort(names)
		mu.Lock()
		listed[server] = names
		mu.Unlock()
	}
	mgr := downstream.New(specsOf(cfg.Servers), collect, downstream.Options{
		Version: version, Stderr: stderr, StartTimeout: 30 * time.Second,
	})
	mgr.Start(ctx)
	mgr.Close()

	failed := false
	for _, s := range cfg.Servers {
		mu.Lock()
		names, ok := listed[s.Name]
		mu.Unlock()
		if !ok {
			failed = true
			if _, err = fmt.Fprintf(stdout, "server %s: not running\n", s.Name); err != nil {
				return err
			}
			continue
		}
		if _, err = fmt.Fprintf(stdout, "server %s: %d tools\n", s.Name, len(names)); err != nil {
			return err
		}
		for _, name := range names {
			mark := ""
			if !gate.ServableName(name) {
				mark = "  (left out: not a valid tool name of at most 64 characters)"
			}
			if _, err = fmt.Fprintf(stdout, "  %s%s\n", name, mark); err != nil {
				return err
			}
		}
	}
	if failed {
		return errCheckFailed
	}
	return nil
}
