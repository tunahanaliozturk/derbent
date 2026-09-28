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
	"slices"
	"strings"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/setup"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

var errDoctorFound = errors.New("doctor: found a problem")

// slowStart is the hook start above which doctor says what it costs (docs/backlog.md item 1).
const slowStart = 500 * time.Millisecond

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

// runDoctor reads each CLI's config and Derbent's own, and names what is wrong with the fix. It writes
// nothing, and returns errDoctorFound, which exits with status 1, when it found a problem.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cliList := flags.String("cli", "", "comma-separated CLIs to check: "+strings.Join(setup.CLIs, ", ")+" (default: each one found)")
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
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
	approvals, envNames := checkConfig(r, *configPath)
	checkDatabase(r)
	if len(clis) == 0 {
		r.note("no CLI found: claude, codex and copilot are not on PATH, and ~/.gemini/config does not exist; name the CLIs to check with --cli")
	}
	var bins []string
	for _, cli := range clis {
		if cli == "claude" {
			checkClaudeVersion(ctx, r)
		}
		if bin := checkCLI(r, cli, dir, approvals, envNames); bin != "" && !slices.Contains(bins, bin) {
			bins = append(bins, bin)
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

// checkConfig checks Derbent's own config, loaded as the hook loads it, and returns the approval timeout
// the hooks must stay above and the ${env:NAME} variables Codex has to pass on.
func checkConfig(r *report, named string) (time.Duration, []string) {
	cfg, path, missing, err := loadConfig(named, config.LoadForHook)
	switch {
	case missing:
		r.problem("derbent init --preset balanced, or watch or strict", "derbent: no config at %s, so every call is allowed", path)
		return config.DefaultApprovalTimeout, nil
	case err != nil:
		r.problem("edit the file; derbent config check shows the same error", "derbent: the config does not load: %v", err)
		return config.DefaultApprovalTimeout, nil
	}
	r.ok("derbent: config %s: %d rules, approvals time out after %s", path, cfg.Rules.Len(), cfg.ApprovalTimeout)
	names, err := config.EnvNames(path)
	if err != nil {
		r.problem("edit the file", "derbent: %v", err)
	}
	return cfg.ApprovalTimeout, names
}

// checkDatabase checks, without writing, that the database can be written, or created where it does not
// exist yet.
func checkDatabase(r *report) {
	path, err := databasePath("")
	if err != nil {
		r.problem("set LOCALAPPDATA on Windows, or HOME elsewhere", "derbent: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0) //nolint:gosec // the user's own database, opened and closed without a write
	switch {
	case err == nil:
		f.Close()
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
			r.ok("derbent: database %s does not exist yet; the first call creates it", path)
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

// checkCLI checks one CLI's hook and MCP entry against each other and against Derbent's config, and
// returns the binary the hook starts, for timing, or "" when there is none to time.
func checkCLI(r *report, cli, dir string, approvals time.Duration, envNames []string) string {
	p, err := setup.PathsOf(cli)
	if err != nil {
		r.problem("set HOME, or USERPROFILE on Windows", "%s: %v", cli, err)
		return ""
	}
	entries, err := setup.MCPEntries(cli, p, dir)
	if err != nil {
		r.problem("fix the file, or restore it from its .derbent-backup copy", "%s: %v", cli, err)
		return ""
	}
	hooks, err := setup.Hooks(cli, p)
	if err != nil {
		r.problem("fix the file, or restore it from its .derbent-backup copy", "%s: %v", cli, err)
		return ""
	}
	for _, file := range setup.Unchecked(cli, p) {
		r.note("%s: %s does not parse as JSON, so doctor did not check it for a derbent gate hook; if it holds one, the gate may run twice or be reported missing", cli, file)
	}
	fix := "derbent init --cli " + cli

	var hook setup.Entry
	server := "derbent"
	if at := slices.IndexFunc(hooks, setup.Entry.RunsGate); at >= 0 {
		hook = hooks[at]
		r.ok("%s: the hook in %s runs %s", cli, hook.File, strings.Join(hook.Words(), " "))
		if s, ok := hook.Flag("--server"); ok {
			server = s
		}
	} else {
		r.problem(fix, "%s: no pre-tool hook runs derbent gate, so its built-in tools are not gated", cli)
	}

	var entry setup.Entry
	at := slices.IndexFunc(entries, func(e setup.Entry) bool { return e.Name == server })
	other := slices.IndexFunc(entries, setup.Entry.RunsMCP)
	switch {
	case at >= 0 && entries[at].RunsMCP():
		entry = entries[at]
		r.ok("%s: the MCP entry %s runs %s", cli, entry.Name, strings.Join(entry.Words(), " "))
	case at >= 0:
		r.problem("point it at derbent mcp --agent "+cli+", or give it another name", "%s: the MCP entry %s runs %s, not derbent mcp", cli, server, strings.Join(entries[at].Words(), " "))
	case other >= 0 && hook.Command != "":
		r.problem("rename the entry to "+server+", or add --server "+entries[other].Name+" to the hook",
			"%s: the MCP entry %s runs derbent mcp, but the hook skips only the entry named %s, so each call to Derbent's own tools is decided and recorded twice", cli, entries[other].Name, server)
	case other >= 0:
		entry = entries[other]
		r.ok("%s: the MCP entry %s runs %s", cli, entry.Name, strings.Join(entry.Words(), " "))
	default:
		r.problem(fix, "%s: no MCP entry runs derbent mcp", cli)
	}

	for _, e := range []setup.Entry{entry, hook} {
		if e.Command != "" && !startable(e.Program()) {
			r.problem("run derbent init again after removing the entry, or correct the path in "+e.File, "%s: %s names %s, which does not exist", cli, e.File, e.Program())
		}
	}
	if hook.Command == "" {
		return ""
	}

	timeout := hook.Timeout
	if timeout == 0 {
		timeout = setup.DefaultTimeout[cli]
	}
	if limit := time.Duration(timeout) * time.Second; limit <= approvals {
		r.problem(fmt.Sprintf("set the hook's timeout above %s, such as %d seconds", approvals, max(setup.HookTimeout, int(approvals.Seconds())+60)),
			"%s: the hook's timeout, %s, is not above [approvals] timeout, %s: the CLI gives up on the hook while it waits for your approval, and the call goes on", cli, limit, approvals)
	} else {
		r.ok("%s: the hook's timeout, %s, is above [approvals] timeout, %s", cli, limit, approvals)
	}

	agent, _ := hook.Flag("--agent")
	if mcpAgent, _ := entry.Flag("--agent"); entry.Command != "" && mcpAgent != agent {
		r.problem("use the same --agent in both", "%s: the hook says --agent %s and the MCP entry --agent %s, so the rules and grants see two agents", cli, agent, mcpAgent)
	}
	switch protocol, hasCLI := hook.Flag("--cli"); {
	case agent == "":
		r.problem("add --agent "+cli+" to the hook", "%s: the hook passes no --agent, so it denies every call", cli)
	case agent != cli && (!hasCLI || protocol != cli):
		r.problem("add --cli "+cli+" to the hook", "%s: the hook's --agent %s is not the CLI's name, so it needs --cli %s to speak the CLI's hook protocol", cli, agent, cli)
	}

	if cli == "codex" && entry.Command != "" {
		var missing []string
		for _, name := range envNames {
			if !slices.Contains(entry.EnvVars, name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			r.problem(`add env_vars = ["`+strings.Join(missing, `", "`)+`"] to [mcp_servers.`+entry.Name+"] in "+entry.File,
				"codex: Derbent's config uses ${env:%s}, which Codex does not pass to the gate, so the gate does not start", strings.Join(missing, "}, ${env:"))
		}
	}
	if !startable(hook.Program()) {
		return ""
	}
	return hook.Program()
}

// startable reports whether program can be started: a path that exists, or a bare name on PATH.
func startable(program string) bool {
	if strings.ContainsAny(program, `/\`) {
		_, err := os.Stat(program)
		return err == nil
	}
	_, err := exec.LookPath(program)
	return err == nil
}

// timeHookStart starts bin with version three times and notes the middle time: every built-in tool
// call waits about that long for the hook before anything else happens. It is a note, never a problem,
// so a slow machine with a correct setup still passes.
func timeHookStart(ctx context.Context, r *report, bin string) {
	times := make([]time.Duration, 0, 3)
	for range 3 {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		start := time.Now()
		err := exec.CommandContext(runCtx, bin, "version").Run() //nolint:gosec // the binary the user's hook runs
		took := time.Since(start)
		cancel()
		if err != nil {
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
