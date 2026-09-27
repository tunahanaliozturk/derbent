package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/downstream"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/pin"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

var errCheckFailed = errors.New("config check failed")

// runConfigCheck loads the config, starts every downstream server once, and prints the tools each one
// would give the agents with their pin states, so a mistake shows up here rather than in the middle of
// an agent session.
func runConfigCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("config check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
	dbFlag := flags.String("db", "", "database to read tool pins from, read-only (default: derbent.db in the user state directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, path, missing, err := loadConfig(*configPath, config.Load)
	if err != nil {
		return err
	}
	note := ""
	if missing {
		note = " (not found: every call is allowed)"
	}
	if _, err = fmt.Fprintf(stdout, "config: %s%s\nrules: %d\nbudgets: %d\n", path, note, cfg.Rules.Len(), cfg.Budgets.Len()); err != nil {
		return err
	}
	dbPath, err := databasePath(*dbFlag)
	if err != nil {
		return err
	}
	// Pins are read, never written: config check shows each tool's state and pins nothing.
	var pins *pin.Store
	pinNote := ""
	db, err := store.OpenExisting(ctx, dbPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		pinNote = " (not found: every tool is new)"
	case err != nil:
		return err
	default:
		defer db.Close()
		pins = pin.NewStore(db)
	}
	if _, err = fmt.Fprintf(stdout, "pins: %s%s\n", dbPath, pinNote); err != nil {
		return err
	}

	var mu sync.Mutex
	listed := map[string][]*mcp.Tool{}
	collect := func(_ context.Context, server string, tools []*mcp.Tool) {
		mu.Lock()
		listed[server] = tools
		mu.Unlock()
	}
	mgr := downstream.New(specsOf(cfg.Servers), collect, downstream.Options{Version: version, Stderr: stderr})
	mgr.Start(ctx)
	select {
	case <-mgr.Started():
	case <-ctx.Done():
	}
	mgr.Close()

	failed := false
	for _, s := range cfg.Servers {
		mu.Lock()
		tools, ok := listed[s.Name]
		mu.Unlock()
		if !ok {
			failed = true
			if _, err = fmt.Fprintf(stdout, "server %s: not running\n", s.Name); err != nil {
				return err
			}
			continue
		}
		if _, err = fmt.Fprintf(stdout, "server %s: %d tools\n", s.Name, len(tools)); err != nil {
			return err
		}
		slices.SortFunc(tools, func(a, b *mcp.Tool) int { return strings.Compare(a.Name, b.Name) })
		for _, t := range tools {
			name, mark := s.Name+"__"+t.Name, ""
			switch why := gate.LeftOut(name, t); {
			case why != "":
				mark = "  (left out: " + why + ")"
			case !s.Pin:
				mark = "  (not pinned: pin = false)"
			default:
				state := pin.New
				if pins != nil {
					if state, err = pins.State(ctx, s.Name, t); err != nil {
						return err
					}
				}
				mark = "  (pin: " + string(state) + ")"
			}
			if _, err = fmt.Fprintf(stdout, "  %s%s\n", visible.Escape(name), mark); err != nil {
				return err
			}
		}
	}
	if failed {
		return errCheckFailed
	}
	return nil
}
