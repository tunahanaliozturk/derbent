package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/preset"
	"github.com/tunahanaliozturk/derbent/internal/setup"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

var errInitFailed = errors.New("init: at least one change failed")

// initPlan is what init does for one CLI.
type initPlan struct {
	cli     string
	changes []setup.Change
	done    []string // the parts already set up
	err     error
}

// runInit sets the agent CLIs up to use Derbent: each one's MCP entry and pre-tool hook, and with
// --preset Derbent's own config. It shows every change and asks once before it makes any (ADR 0015).
func runInit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cliList := flags.String("cli", "", "comma-separated CLIs to set up: "+strings.Join(setup.CLIs, ", ")+" (default: each one found)")
	presetName := flags.String("preset", "", "also write Derbent's config from a preset: "+strings.Join(preset.Names(), ", "))
	printOnly := flags.Bool("print", false, "with --preset, print the preset and change nothing")
	yes := flags.Bool("yes", false, "make the changes without asking")
	dryRun := flags.Bool("dry-run", false, "print the changes and make none")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("init: unexpected argument %q", flags.Arg(0))
	}
	presetText := ""
	if *presetName != "" {
		text, textErr := preset.Text(*presetName)
		if textErr != nil {
			return fmt.Errorf("init: %w", textErr)
		}
		presetText = text
	}
	if *printOnly {
		if presetText == "" {
			return errors.New("init: --print needs --preset")
		}
		_, writeErr := io.WriteString(stdout, presetText)
		return writeErr
	}
	clis, err := setup.Select(*cliList)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	bin, err := derbentBinary()
	if err != nil {
		return err
	}
	cfgPath, err := config.DefaultConfigPath()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(cfgPath)
	cfgExists := statErr == nil

	plans := make([]initPlan, 0, len(clis))
	for _, cli := range clis {
		pl := initPlan{cli: cli}
		var p setup.Paths
		if p, pl.err = setup.PathsOf(cli); pl.err == nil {
			pl.changes, pl.done, pl.err = setup.Plan(cli, p, bin)
		}
		plans = append(plans, pl)
	}
	var own []setup.Change // Derbent's own config, from the preset
	if presetText != "" && !cfgExists {
		own = append(own, setup.NewFile(cfgPath, presetText))
	}

	now := time.Now()
	pending := len(own)
	for _, pl := range plans {
		for _, c := range pl.changes {
			printChange(stdout, c, now, "")
			pending++
		}
	}
	for _, c := range own {
		printChange(stdout, c, now, *presetName)
	}
	if len(clis) == 0 {
		fmt.Fprintln(stdout, "No CLI found: claude, codex and copilot are not on PATH, and ~/.gemini/config does not exist. Name the CLIs to set up with --cli.")
	}
	if presetText != "" && cfgExists {
		fmt.Fprintf(stdout, "derbent: %s exists, so the %s preset is not written: derbent init never replaces a config.\n", visible.Escape(cfgPath), *presetName)
	}

	confirmed := pending > 0 && !*dryRun
	if confirmed && !*yes {
		fmt.Fprint(stdout, "Make these changes? Type y to go ahead; anything else changes nothing: ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		answer := strings.TrimSpace(line)
		confirmed = strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
		fmt.Fprintln(stdout)
	}

	backedUp := map[string]bool{}
	failed := false
	for i := range plans {
		pl := &plans[i]
		if pl.err == nil && confirmed {
			pl.err = applyChanges(ctx, pl.changes, now, backedUp, stdout, stderr)
		}
		failed = failed || pl.err != nil
		fmt.Fprintf(stdout, "%s: %s\n", pl.cli, visible.Escape(initStatus(*pl, confirmed, *dryRun)))
	}
	switch {
	case len(own) > 0 && confirmed:
		if err = applyChanges(ctx, own, now, backedUp, stdout, stderr); err != nil {
			failed = true
			fmt.Fprintf(stdout, "derbent: failed: %s\n", visible.Escape(err.Error()))
		} else {
			fmt.Fprintf(stdout, "derbent: wrote the %s preset to %s\n", *presetName, visible.Escape(cfgPath))
		}
	case len(own) > 0:
		fmt.Fprintf(stdout, "derbent: skipped: the %s preset was not written\n", *presetName)
	case !cfgExists:
		fmt.Fprintf(stdout, "derbent: no config at %s, so every call is allowed until one exists. Start from a preset: derbent init --preset watch (record everything), balanced or strict.\n", visible.Escape(cfgPath))
	}
	fmt.Fprintln(stdout, "run derbent doctor to check")
	if failed {
		return errInitFailed
	}
	return nil
}

// initStatus is one CLI's line in init's report: set up, already set up, skipped and why, or failed
// and why.
func initStatus(pl initPlan, confirmed, dryRun bool) string {
	switch {
	case pl.err != nil:
		return "failed: " + pl.err.Error()
	case len(pl.changes) == 0:
		return "already set up"
	case dryRun:
		return "skipped: dry run, nothing changed"
	case !confirmed:
		return "skipped: not confirmed, nothing changed"
	}
	s := "set up"
	for _, c := range pl.changes {
		if c.Manual {
			s += ", except the MCP entry: run " + c.Text
		}
	}
	if len(pl.done) > 0 {
		s += "; already there: " + strings.Join(pl.done, ", ")
	}
	return s
}

// printChange shows one change before it is made: the file, the copy made of it first, and the command
// or the exact text added, every value escaped. A preset is named rather than printed.
func printChange(w io.Writer, c setup.Change, now time.Time, presetName string) {
	who := c.CLI
	if who == "" {
		who = "derbent"
	}
	fmt.Fprintf(w, "%s: %s\n", who, visible.Escape(c.File))
	switch _, err := os.Stat(c.File); {
	case c.Manual:
	case err == nil:
		fmt.Fprintf(w, "  copied first to %s\n", visible.Escape(setup.BackupName(c.File, now)))
	case c.Run == nil:
		fmt.Fprintln(w, "  a new file")
	}
	switch {
	case c.Manual:
		fmt.Fprintf(w, "  %s is not on PATH; run this yourself:\n    %s\n", c.Run[0], visible.Escape(c.Text))
	case c.Run != nil:
		fmt.Fprintf(w, "  runs:\n    %s\n", visible.Escape(c.Text))
	case presetName != "":
		fmt.Fprintf(w, "  adds the %s preset (derbent init --preset %s --print shows it)\n", presetName, presetName)
	default:
		fmt.Fprintln(w, "  adds:")
		for _, line := range strings.Split(strings.TrimSuffix(c.Text, "\n"), "\n") {
			fmt.Fprintf(w, "    %s\n", visible.Escape(line))
		}
	}
}

// applyChanges makes changes in order, copying each file that exists before its first change in this
// run. A command the user has to run is skipped.
func applyChanges(ctx context.Context, changes []setup.Change, now time.Time, backedUp map[string]bool, stdout, stderr io.Writer) error {
	for _, c := range changes {
		if c.Manual {
			continue
		}
		if !backedUp[c.File] {
			if _, err := setup.Backup(c.File, now); err != nil {
				return err
			}
			backedUp[c.File] = true
		}
		if err := c.Apply(ctx, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// derbentBinary is the running binary's absolute path, symbolic links resolved, with forward slashes, as
// init writes it into the CLIs' configs. A path some shell would read inside quotes is refused.
func derbentBinary() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", fmt.Errorf("find the derbent binary: %w", err)
	}
	bin := filepath.ToSlash(exe)
	if err = setup.CheckBinary(bin); err != nil {
		return "", err
	}
	return bin, nil
}
