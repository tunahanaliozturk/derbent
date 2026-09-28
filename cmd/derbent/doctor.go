package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/setup"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

var errDoctorFound = errors.New("doctor: found a problem")

// slowStart is the hook start above which doctor says what it costs (docs/backlog.md item 1).
const slowStart = 500 * time.Millisecond

// hookStartLimit is how long one start of the hook's binary may take before doctor stops it.
var hookStartLimit = 30 * time.Second

// report writes doctor's lines: ok, problem with its fix, or note, each escaped, since most values come
// from files other programs and agents can write.
type report struct {
	w        io.Writer
	problems int
}

func (r *report) line(status, format string, args ...any) {
	fmt.Fprintf(r.w, "%-8s%s\n", status, visible.Escape(fmt.Sprintf(format, args...)))
}

func (r *report) ok(format string, args ...any) { r.line("ok", format, args...) }

func (r *report) note(format string, args ...any) { r.line("note", format, args...) }

// problem reports what is wrong, then after "; fix: " what to change.
func (r *report) problem(fix, format string, args ...any) {
	r.problems++
	r.line("problem", "%s; fix: %s", fmt.Sprintf(format, args...), fix)
}

// derbentConfig is a config doctor checked: its path, the approval timeout the hooks must stay above,
// and the ${env:NAME} variables Codex has to pass on.
type derbentConfig struct {
	path      string
	approvals time.Duration
	envNames  []string
}

// runDoctor reads each CLI's config and Derbent's own, and names what is wrong with the fix, and returns
// errDoctorFound, which exits with status 1, when it found a problem. It changes no file. Its one write:
// where the database does not exist yet, it creates a file in the nearest directory on the database's
// path and removes it at once, to check that the database can be created there (checkCanCreate).
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cliList := flags.String("cli", "", "comma-separated CLIs to check: "+strings.Join(setup.CLIs, ", ")+" (default: each one found)")
	configPath := flags.String("config", "", "the config to check for Derbent itself (default: config.toml in the user config directory); a hook or MCP entry without its own --config is checked against the default")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("doctor: unexpected argument %q", flags.Arg(0))
	}
	clis, err := setup.Select(*cliList)
	if err != nil {
		return fmt.Errorf("doctor: %w", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}
	r := &report{w: stdout}
	own := checkConfig(r, "derbent", "the config", *configPath)
	// defaults is the default config, which a hook or MCP entry without --config of its own loads,
	// whatever --config names here; when --config names another file, it is checked once, when needed.
	defaults := func() derbentConfig { return own }
	if *configPath != "" {
		var loaded *derbentConfig
		defaults = func() derbentConfig {
			if loaded == nil {
				c := checkConfig(r, "derbent", "the default config", "")
				loaded = &c
			}
			return *loaded
		}
	}
	checkDatabase(ctx, r)
	if len(clis) == 0 {
		r.note("no CLI found: claude, codex and copilot are not on PATH, and ~/.gemini/config does not exist; name the CLIs to check with --cli")
	}
	var bins []string
	for _, cli := range clis {
		for _, bin := range checkCLI(ctx, r, cli, dir, defaults) {
			if !slices.Contains(bins, bin) {
				bins = append(bins, bin)
			}
		}
	}
	for _, bin := range bins {
		timeHookStart(ctx, r, bin)
	}
	if r.problems > 0 {
		return errDoctorFound
	}
	return nil
}

// checkConfig checks the config at named, or with named "" the default file, loaded as the hook loads
// it, and returns what the hooks and Codex's MCP entry are checked against. who and what name it in each
// line: "derbent" and "the config", or a CLI and the flag that names the file.
func checkConfig(r *report, who, what, named string) derbentConfig {
	cfg, path, missing, err := loadConfig(named, config.LoadForHook)
	checked := derbentConfig{path: path, approvals: config.DefaultApprovalTimeout}
	switch {
	case missing:
		r.problem("derbent init --preset balanced, or watch or strict", "%s: no config at %s, so every call is allowed", who, path)
		return checked
	case errors.Is(err, fs.ErrNotExist):
		r.problem("create it, or name your config", "%s: %s names %s, which does not exist", who, what, path)
		return checked
	case err != nil:
		r.problem("edit the file; derbent config check --config <file> shows the same error", "%s: %s does not load: %v", who, what, err)
		return checked
	}
	r.ok("%s: %s %s: %d rules, approvals time out after %s", who, what, path, cfg.Rules.Len(), cfg.ApprovalTimeout)
	checked.approvals = cfg.ApprovalTimeout
	if checked.envNames, err = config.EnvNames(path); err != nil {
		r.problem("edit the file", "%s: %v", who, err)
	}
	return checked
}

// checkDatabase checks that the database can be written and is one Derbent can use, leaving it and its
// directory as they were, or where it does not exist yet, that it can be created (checkCanCreate).
func checkDatabase(ctx context.Context, r *report) {
	path, err := databasePath("")
	if err != nil {
		r.problem("set LOCALAPPDATA on Windows, or HOME elsewhere", "derbent: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0) //nolint:gosec // the user's own database, opened and closed without a write
	switch {
	case err == nil:
		f.Close()
		if err = store.Check(ctx, path); err != nil {
			r.problem("move the file aside if it is not Derbent's, or upgrade derbent if its schema is newer", "derbent: database %s cannot be used: %v", path, err)
			return
		}
		r.ok("derbent: database %s can be written", path)
		return
	case !errors.Is(err, fs.ErrNotExist):
		r.problem("give your user write access to it, or remove what is in its place", "derbent: database %s cannot be written: %v", path, err)
		return
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, statErr := os.Stat(dir)
		switch {
		case statErr == nil && info.IsDir():
			checkCanCreate(r, dir, path)
			return
		case statErr == nil || !errors.Is(statErr, fs.ErrNotExist):
			r.problem("remove or rename it", "derbent: %s is in the way of the database %s", dir, path)
			return
		case filepath.Dir(dir) == dir:
			r.problem("create the directory", "derbent: no directory on the path of the database %s exists", path)
			return
		}
	}
}

// checkCanCreate checks that the user can create the database path, or the directories on its way, in
// dir, the nearest directory on its path that exists: it creates a file there and removes it at once.
func checkCanCreate(r *report, dir, path string) {
	f, err := os.CreateTemp(dir, ".derbent-doctor-*")
	if err != nil {
		r.problem("give your user write access to "+dir, "derbent: cannot create the database %s: %v", path, err)
		return
	}
	f.Close()
	if err = os.Remove(f.Name()); err != nil {
		r.problem("remove it", "derbent: could not remove %s, which doctor made to check that %s can be written: %v", f.Name(), dir, err)
		return
	}
	r.ok("derbent: database %s does not exist yet; the first call creates it", path)
}

// checkClaudeVersion checks that the claude on PATH passes a hook its args, as the exec-form hook init
// writes needs. A version it cannot read, or no claude on PATH, is a note.
func checkClaudeVersion(ctx context.Context, r *report) {
	const needs = "the hook init writes needs Claude Code " + setup.MinClaudeVersion + " or later"
	if _, err := exec.LookPath("claude"); err != nil {
		r.note("claude: claude is not on PATH, so Claude Code's version was not checked; %s", needs)
		return
	}
	v, err := setup.ClaudeVersion(ctx)
	switch {
	case err != nil:
		r.note("claude: could not read Claude Code's version (%v); %s", err, needs)
	case setup.VersionBefore(v, setup.MinClaudeVersion):
		r.problem("upgrade Claude Code: claude update",
			"claude: Claude Code %s is older than %s, the first that passes a hook its args, so it starts the hook's derbent with no arguments and the hook decides nothing", v, setup.MinClaudeVersion)
	default:
		r.ok("claude: Claude Code %s passes a hook its args", v)
	}
}

// checkCLI checks one CLI's hooks and MCP entries against each other and against the config each loads,
// and returns the binaries its gate hooks start, for timing.
func checkCLI(ctx context.Context, r *report, cli, dir string, defaults func() derbentConfig) []string {
	p, err := setup.PathsOf(cli)
	if err != nil {
		r.problem("set HOME, or USERPROFILE on Windows", "%s: %v", cli, err)
		return nil
	}
	entries, err := setup.MCPEntries(cli, p, dir)
	if err != nil {
		r.problem("fix the file, or restore it from its .derbent-backup copy", "%s: %v", cli, err)
		return nil
	}
	hooks, err := setup.Hooks(cli, p)
	if err != nil {
		r.problem("fix the file, or restore it from its .derbent-backup copy", "%s: %v", cli, err)
		return nil
	}
	for _, file := range setup.Unchecked(cli, p) {
		r.note("%s: %s does not parse as JSON, so doctor did not check it for a derbent gate hook; if it holds one, the gate may run twice or be reported missing", cli, file)
	}

	gates := slices.DeleteFunc(hooks, func(h setup.Entry) bool { return !h.RunsGate() })
	server := "derbent"
	switch len(gates) {
	case 0:
		r.problem("derbent init --cli "+cli, "%s: no pre-tool hook runs derbent gate, so its built-in tools are not gated", cli)
	case 1:
	default:
		files := make([]string, len(gates))
		for i, h := range gates {
			files[i] = h.File
		}
		r.problem("keep one of them", "%s: %d hooks run derbent gate, in %s, so every call is decided twice", cli, len(gates), strings.Join(files, ", "))
	}
	if len(gates) > 0 {
		if s, ok := gates[0].Flag("--server"); ok {
			server = s
		}
	}
	entry := checkEntries(r, cli, entries, server, len(gates) > 0)

	for _, e := range append([]setup.Entry{entry}, gates...) {
		if e.Command == "" {
			continue
		}
		if why := cannotStart(e.Program()); why != "" {
			r.problem("run derbent init again after removing the entry, or correct the path in "+e.File, "%s: %s names %s, %s", cli, e.File, e.Program(), why)
		}
	}
	if cli == "claude" && slices.ContainsFunc(gates, func(h setup.Entry) bool { return !h.Shell }) {
		checkClaudeVersion(ctx, r) // an older Claude Code starts an exec-form hook without its args
	}
	if cli == "codex" && len(gates) > 0 {
		r.note("codex: Codex runs a new hook only after you trust it in /hooks")
	}

	var bins []string
	for _, h := range gates {
		checkHook(r, cli, h, entry, defaults)
		if cannotStart(h.Program()) == "" {
			bins = append(bins, h.Program())
		}
	}
	if cli == "codex" && entry.Command != "" {
		cfg := configOf(r, cli, "the MCP entry's --config", entry, defaults)
		var missing []string
		for _, name := range cfg.envNames {
			if !slices.Contains(entry.EnvVars, name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			r.problem(`add env_vars = ["`+strings.Join(missing, `", "`)+`"] to [mcp_servers.`+entry.Name+"] in "+entry.File,
				"codex: Derbent's config %s uses ${env:%s}, which Codex does not pass to the gate, so the gate does not start", cfg.path, strings.Join(missing, "}, ${env:"))
		}
	}
	return bins
}

// checkEntries finds the MCP entry of cli that runs derbent mcp and returns it, or an empty entry when
// there is none to check the hooks against. It is the entry named server, the later of two where Claude
// Code has one at user and one at local scope, since Claude Code uses the local one. With a gate hook,
// any other entry that runs derbent mcp is a problem: the hook skips only the tools of server.
func checkEntries(r *report, cli string, entries []setup.Entry, server string, hooked bool) setup.Entry {
	var entry setup.Entry
	at := -1
	for i, e := range entries {
		if e.Name == server {
			at = i
		}
	}
	other := slices.IndexFunc(entries, setup.Entry.RunsMCP)
	switch {
	case at >= 0 && entries[at].RunsMCP():
		entry = entries[at]
		r.ok("%s: the MCP entry %s runs %s", cli, entry.Name, strings.Join(entry.Words(), " "))
	case at >= 0:
		r.problem("point it at derbent mcp --agent "+cli+", or give it another name", "%s: the MCP entry %s runs %s, not derbent mcp", cli, server, strings.Join(entries[at].Words(), " "))
	case other >= 0 && !hooked:
		entry = entries[other]
		r.ok("%s: the MCP entry %s runs %s", cli, entry.Name, strings.Join(entry.Words(), " "))
	case other < 0:
		r.problem("derbent init --cli "+cli, "%s: no MCP entry runs derbent mcp", cli)
	}
	if !hooked {
		return entry
	}
	for _, e := range entries {
		if e.Name == server || !e.RunsMCP() {
			continue
		}
		fix := "remove the entry " + e.Name
		if entry.Command == "" {
			fix = "rename the entry to " + server + ", or add --server " + e.Name + " to the hook"
		}
		r.problem(fix, "%s: the MCP entry %s runs derbent mcp, but the hook skips only the entry named %s, so each call to Derbent's own tools is decided and recorded twice", cli, e.Name, server)
	}
	return entry
}

// configOf is the config e loads: the file its --config names, checked and reported as what, or else
// the default config.
func configOf(r *report, cli, what string, e setup.Entry, defaults func() derbentConfig) derbentConfig {
	if named, ok := e.Flag("--config"); ok {
		return checkConfig(r, cli, what, named)
	}
	return defaults()
}

// checkHook checks one gate hook of cli: that it runs for every tool, that its timeout is above the
// approval timeout of the config it loads, its --config or the default one, and that its agent matches
// the MCP entry's and the CLI's hook protocol.
func checkHook(r *report, cli string, h, entry setup.Entry, defaults func() derbentConfig) {
	r.ok("%s: the hook in %s runs %s", cli, h.File, strings.Join(h.Words(), " "))
	if !h.GatesEveryTool() {
		r.problem(fmt.Sprintf("set its matcher to %q", setup.EveryToolMatcher(cli)), "%s: the hook in %s matches only %q, so the other tools are not gated", cli, h.File, h.Matcher)
	}
	cfg := configOf(r, cli, "the hook's --config", h, defaults)
	timeout := h.Timeout
	if timeout == 0 {
		timeout = setup.DefaultTimeout[cli]
	}
	if limit := time.Duration(timeout) * time.Second; limit <= cfg.approvals {
		r.problem(fmt.Sprintf("set the hook's timeout above %s, such as %d seconds", cfg.approvals, max(setup.HookTimeout, int(cfg.approvals.Seconds())+60)),
			"%s: the hook's timeout, %s, is not above [approvals] timeout, %s, in %s: the CLI gives up on the hook while it waits for your approval, and the call goes on", cli, limit, cfg.approvals, cfg.path)
	} else {
		r.ok("%s: the hook's timeout, %s, is above [approvals] timeout, %s, in %s", cli, limit, cfg.approvals, cfg.path)
	}

	agent, _ := h.Flag("--agent")
	if mcpAgent, _ := entry.Flag("--agent"); entry.Command != "" && mcpAgent != agent {
		r.problem("use the same --agent in both", "%s: the hook says --agent %s and the MCP entry --agent %s, so the rules and grants see two agents", cli, agent, mcpAgent)
	}
	switch protocol, hasCLI := h.Flag("--cli"); {
	case agent == "":
		r.problem("add --agent "+cli+" to the hook", "%s: the hook passes no --agent, so it denies every call", cli)
	case agent != cli && (!hasCLI || protocol != cli):
		r.problem("add --cli "+cli+" to the hook", "%s: the hook's --agent %s is not the CLI's name, so it needs --cli %s to speak the CLI's hook protocol", cli, agent, cli)
	}
}

// cannotStart says why program cannot be started, or "" when it can: a path must name a regular file,
// executable outside Windows, and a bare name must be on PATH.
func cannotStart(program string) string {
	if !strings.ContainsAny(program, `/\`) {
		if _, err := exec.LookPath(program); err != nil {
			return "which is not on PATH"
		}
		return ""
	}
	info, err := os.Stat(program)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "which does not exist"
	case err != nil:
		return "which cannot be read: " + err.Error()
	case !info.Mode().IsRegular():
		return "which is not a file"
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
		return "which is not executable"
	}
	return ""
}

// timeHookStart starts bin with version three times and notes the middle time: every built-in tool
// call waits about that long for the hook before anything else happens. It is a note, never a problem,
// so a slow machine with a correct setup still passes.
func timeHookStart(ctx context.Context, r *report, bin string) {
	times := make([]time.Duration, 0, 3)
	for range 3 {
		runCtx, cancel := context.WithTimeout(ctx, hookStartLimit)
		start := time.Now()
		err := exec.CommandContext(runCtx, bin, "version").Run() //nolint:gosec // the binary the user's hook runs
		took := time.Since(start)
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case err != nil && timedOut:
			r.problem("check that "+bin+" starts, or run derbent init again", "derbent: %s version did not finish within %s", bin, hookStartLimit)
			return
		case err != nil:
			r.problem("check that "+bin+" starts, or run derbent init again", "derbent: %s version did not run: %v", bin, err)
			return
		}
		times = append(times, took)
	}
	slices.Sort(times)
	middle := times[1].Round(time.Millisecond)
	r.note("derbent: starting the hook's binary took %s, the middle of three runs of %s version", middle, bin)
	if middle > slowStart {
		r.note("derbent: every built-in tool call waits about %s for the hook to start; on a managed Windows machine an endpoint scanner checking the unsigned binary is the likely cause (design.md, Known limits)", middle)
	}
}
