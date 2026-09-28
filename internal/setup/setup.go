// Package setup knows where the four agent CLIs keep their MCP entries and pre-tool hooks, reads
// Derbent's entries back out of those files, and builds the changes derbent init makes (ADR 0015).
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// CLIs lists the CLIs setup knows, in the order init and doctor report them.
var CLIs = []string{"claude", "codex", "copilot", "antigravity"}

// DefaultTimeout is the hook timeout, in seconds, each CLI uses when the hook sets none.
var DefaultTimeout = map[string]int{"claude": 600, "codex": 600, "copilot": 30, "antigravity": 30}

// HookTimeout is the timeout, in seconds, init writes where a CLI's default of 30 is not above the
// default approval timeout of 50.
const HookTimeout = 120

// Paths are the files one CLI keeps Derbent's MCP entry and hook in.
type Paths struct {
	MCP     string // the MCP registry the CLI's own mcp add writes, or init edits
	Hook    string // the file init adds the hook to
	HookDir string // Copilot CLI only: every *.json file in it holds hooks
}

// PathsOf returns cli's files. They are under the user's home directory, or under the directory
// CLAUDE_CONFIG_DIR, CODEX_HOME or COPILOT_HOME names, as the CLI itself finds them.
func PathsOf(cli string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("find home directory: %w", err)
	}
	under := func(env, dir string) string {
		if d := os.Getenv(env); d != "" {
			return d
		}
		return filepath.Join(home, dir)
	}
	switch cli {
	case "claude":
		dir := under("CLAUDE_CONFIG_DIR", ".claude")
		registry := filepath.Join(home, ".claude.json")
		if os.Getenv("CLAUDE_CONFIG_DIR") != "" {
			registry = filepath.Join(dir, ".claude.json")
		}
		return Paths{MCP: registry, Hook: filepath.Join(dir, "settings.json")}, nil
	case "codex":
		file := filepath.Join(under("CODEX_HOME", ".codex"), "config.toml")
		return Paths{MCP: file, Hook: file}, nil
	case "copilot":
		dir := under("COPILOT_HOME", ".copilot")
		hooks := filepath.Join(dir, "hooks")
		return Paths{MCP: filepath.Join(dir, "mcp-config.json"), Hook: filepath.Join(hooks, "derbent.json"), HookDir: hooks}, nil
	case "antigravity":
		dir := filepath.Join(home, ".gemini", "config")
		return Paths{MCP: filepath.Join(dir, "mcp_config.json"), Hook: filepath.Join(dir, "hooks.json")}, nil
	}
	return Paths{}, fmt.Errorf("unknown CLI %q", cli)
}

// Found reports whether cli is installed: its command is on PATH, or for Antigravity CLI, which init
// runs no command of, its config directory exists.
func Found(cli string) bool {
	if cli != "antigravity" {
		_, err := exec.LookPath(cli)
		return err == nil
	}
	p, err := PathsOf(cli)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Dir(p.MCP))
	return err == nil && info.IsDir()
}

// Select returns the CLIs a comma-separated --cli list names, in the order of CLIs, or when the list is
// empty the CLIs Found reports. An unknown name is an error that lists the four.
func Select(list string) ([]string, error) {
	if list == "" {
		return slices.DeleteFunc(slices.Clone(CLIs), func(c string) bool { return !Found(c) }), nil
	}
	named := strings.Split(list, ",")
	for i, c := range named {
		named[i] = strings.TrimSpace(c)
		if !slices.Contains(CLIs, named[i]) {
			return nil, fmt.Errorf("unknown CLI %q in --cli: use %s", named[i], strings.Join(CLIs, ", "))
		}
	}
	return slices.DeleteFunc(slices.Clone(CLIs), func(c string) bool { return !slices.Contains(named, c) }), nil
}

// MinClaudeVersion is the first Claude Code release that reads a hook's args (exec form). An older one
// starts the command with no arguments, so Derbent's hook would decide nothing.
const MinClaudeVersion = "2.1.139"

// versionPattern is a version as Claude Code prints it, with a pre-release suffix such as -beta.1 if
// there is one.
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?`)

// ClaudeVersion runs claude --version, for ten seconds at most, and returns the version it prints.
func ClaudeVersion(ctx context.Context) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "claude", "--version")
	cmd.WaitDelay = time.Second // a .cmd shim's child may hold the pipe after the shim is killed
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude --version: %w", err)
	}
	v := versionPattern.FindString(string(out))
	if v == "" {
		return "", errors.New("claude --version printed no version number")
	}
	return v, nil
}

// VersionBefore reports whether the dotted version v is older than version than. A pre-release, such
// as 2.1.139-beta, is older than its release, 2.1.139.
func VersionBefore(v, than string) bool {
	parse := func(s string) ([]int, bool) {
		core, pre, _ := strings.Cut(s, "-")
		var n []int
		for _, part := range strings.Split(core, ".") {
			i, _ := strconv.Atoi(part)
			n = append(n, i)
		}
		return n, pre != ""
	}
	a, aPre := parse(v)
	b, bPre := parse(than)
	if c := slices.Compare(a, b); c != 0 {
		return c < 0
	}
	return aPre && !bPre // ponytail: two pre-releases of one release count as equal
}

// Entry is one MCP server entry, or one pre-tool command hook, as a CLI's config holds it.
type Entry struct {
	Name    string   // the MCP entry's name; empty for a hook
	Command string   // the program, or with Shell the whole command line
	Args    []string // the arguments to Command
	Shell   bool     // Command is a line a shell reads
	Timeout int      // a hook's timeout in seconds; 0 when it sets none
	EnvVars []string // Codex only: the variables the MCP entry passes on to the server
	File    string   // the file the entry came from
}

// Words is the entry's command line as words: a shell line split the way sh, cmd.exe and PowerShell
// agree on, without PowerShell's call operator in front, or else the program and its arguments.
func (e Entry) Words() []string {
	if !e.Shell {
		return append([]string{e.Command}, e.Args...)
	}
	w := split(e.Command)
	if len(w) > 0 && w[0] == "&" {
		w = w[1:]
	}
	return w
}

// Program is the first word: the binary the entry starts.
func (e Entry) Program() string {
	w := e.Words()
	if len(w) == 0 {
		return ""
	}
	return w[0]
}

// RunsGate reports whether the entry runs derbent gate, whatever path it gives the binary.
func (e Entry) RunsGate() bool { return e.runs("gate") }

// RunsMCP reports whether the entry runs derbent mcp, whatever path it gives the binary.
func (e Entry) RunsMCP() bool { return e.runs("mcp") }

// runs reports whether the entry's program is a derbent binary, one whose file name starts with
// "derbent" (derbent.exe, a release binary such as derbent-v1.0.0-linux-amd64), and its first argument
// is sub.
func (e Entry) runs(sub string) bool {
	w := e.Words()
	base := strings.ToLower(path.Base(filepath.ToSlash(e.Program())))
	return len(w) >= 2 && strings.HasPrefix(base, "derbent") && w[1] == sub
}

// Flag returns the value of --name value or --name=value among the entry's words, and whether the
// flag is there.
func (e Entry) Flag(name string) (string, bool) {
	w := e.Words()
	for i, s := range w {
		if s == name && i+1 < len(w) {
			return w[i+1], true
		}
		if v, ok := strings.CutPrefix(s, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// split cuts a command line into words at spaces and tabs outside quotes. Double or single quotes
// around part of a word keep what is inside them as it is.
func split(line string) []string {
	var words []string
	var b strings.Builder
	inWord, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			b.WriteRune(r)
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, b.String())
				b.Reset()
				inWord = false
			}
		default:
			b.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, b.String())
	}
	return words
}

// MCPEntries reads every MCP server entry cli has at user scope and, for Claude Code, at the local
// scope of dir. A missing file holds none; a file that does not parse is an error that names it.
func MCPEntries(cli string, p Paths, dir string) ([]Entry, error) {
	if cli == "codex" {
		c, err := readCodex(p.MCP)
		if err != nil {
			return nil, err
		}
		var out []Entry
		for _, name := range slices.Sorted(maps.Keys(c.MCPServers)) {
			s := c.MCPServers[name]
			out = append(out, Entry{Name: name, Command: s.Command, Args: s.Args, EnvVars: envVarNames(s.EnvVars), File: p.MCP})
		}
		return out, nil
	}
	root, err := readJSON(p.MCP)
	if err != nil {
		return nil, err
	}
	out := servers(p.MCP, root["mcpServers"])
	if cli == "claude" {
		projects, _ := root["projects"].(map[string]any)
		for _, key := range slices.Sorted(maps.Keys(projects)) {
			if samePath(key, dir) {
				project, _ := projects[key].(map[string]any)
				out = append(out, servers(p.MCP, project["mcpServers"])...)
			}
		}
	}
	return out, nil
}

// Hooks reads every pre-tool command hook cli has at user scope: in the file init writes, and in the
// other user-level file the CLI reads hooks from, Codex's hooks.json beside config.toml and Antigravity
// CLI's ~/.gemini/antigravity-cli/settings.json. A missing file holds none. A file init edits that does
// not parse is an error that names it; the other file, which init only looks in, is skipped, and
// Unchecked names it.
func Hooks(cli string, p Paths) ([]Entry, error) {
	var out []Entry
	switch cli {
	case "codex":
		c, err := readCodex(p.Hook)
		if err != nil {
			return nil, err
		}
		for _, group := range c.Hooks.PreToolUse {
			for _, h := range group.Hooks {
				if h.Type == "" || h.Type == "command" {
					out = append(out, Entry{Command: h.Command, Shell: true, Timeout: h.Timeout, File: p.Hook})
				}
			}
		}
	case "claude":
		return jsonHooks(p.Hook, eventGroups)
	case "copilot":
		files, err := filepath.Glob(filepath.Join(p.HookDir, "*.json"))
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", p.HookDir, err)
		}
		for _, file := range files {
			root, readErr := readJSON(file)
			if readErr != nil {
				return nil, readErr
			}
			hooks, _ := root["hooks"].(map[string]any)
			for _, h := range objects(hooks["preToolUse"]) {
				if t, _ := h["type"].(string); t == "" || t == "command" {
					out = append(out, copilotHook(file, h))
				}
			}
		}
		return out, nil
	default: // Antigravity CLI
		hooks, err := jsonHooks(p.Hook, namedGroups)
		if err != nil {
			return nil, err
		}
		out = hooks
	}
	if file, groups := lookupFile(cli, p); file != "" {
		if more, err := jsonHooks(file, groups); err == nil { // else Unchecked names it
			out = append(out, more...)
		}
	}
	return out, nil
}

// lookupFile is the other user-level file cli reads hooks from, which init only looks in for an
// existing gate hook and never writes, and the function that finds its PreToolUse groups: Codex's
// hooks.json beside config.toml, and Antigravity CLI's ~/.gemini/antigravity-cli/settings.json, which
// keeps the same map of hook names under "hooks". Antigravity's docs show no example of settings.json,
// so a PreToolUse list right under "hooks" is read as well. It is "" for the other CLIs.
func lookupFile(cli string, p Paths) (string, func(root map[string]any) []map[string]any) {
	switch cli {
	case "codex":
		return filepath.Join(filepath.Dir(p.Hook), "hooks.json"), eventGroups
	case "antigravity":
		return filepath.Join(filepath.Dir(filepath.Dir(p.Hook)), "antigravity-cli", "settings.json"), func(root map[string]any) []map[string]any {
			hooks, _ := root["hooks"].(map[string]any)
			return append(namedGroups(hooks), objects(hooks["PreToolUse"])...)
		}
	}
	return "", nil
}

// Unchecked lists the files Hooks skipped because they cannot be read as JSON: cli's file that init
// only looks in, which may hold comments the CLI accepts. A gate hook in it is not seen.
func Unchecked(cli string, p Paths) []string {
	if file, _ := lookupFile(cli, p); file != "" {
		if _, err := readJSON(file); err != nil {
			return []string{file}
		}
	}
	return nil
}

// jsonHooks reads the command hooks in a JSON file's PreToolUse groups, which groups finds in the file.
func jsonHooks(file string, groups func(root map[string]any) []map[string]any) ([]Entry, error) {
	root, err := readJSON(file)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, g := range groups(root) {
		for _, h := range objects(g["hooks"]) {
			if t, _ := h["type"].(string); t != "" && t != "command" {
				continue
			}
			command, _ := h["command"].(string)
			if w, ok := h["commandWindows"].(string); ok && runtime.GOOS == "windows" {
				command = w // Codex's hooks.json: the line Codex runs instead of command on Windows
			}
			args, execForm := h["args"]
			out = append(out, Entry{Command: command, Args: stringList(args), Shell: !execForm, Timeout: seconds(h["timeout"]), File: file})
		}
	}
	return out, nil
}

// eventGroups finds the PreToolUse groups of Claude Code's settings.json and Codex's hooks.json: hooks,
// then the event.
func eventGroups(root map[string]any) []map[string]any {
	hooks, _ := root["hooks"].(map[string]any)
	return objects(hooks["PreToolUse"])
}

// namedGroups finds the PreToolUse groups in Antigravity CLI's map of hook names to their events,
// leaving out a hook that is turned off.
func namedGroups(m map[string]any) []map[string]any {
	var groups []map[string]any
	for _, name := range slices.Sorted(maps.Keys(m)) {
		def, _ := m[name].(map[string]any)
		if enabled, ok := def["enabled"].(bool); ok && !enabled {
			continue
		}
		groups = append(groups, objects(def["PreToolUse"])...)
	}
	return groups
}

// copilotHook reads one Copilot CLI hook: exec and args, or the shell line this system runs
// (powershell on Windows, bash elsewhere), else the cross-platform command.
func copilotHook(file string, h map[string]any) Entry {
	timeout := seconds(h["timeoutSec"])
	if timeout == 0 {
		timeout = seconds(h["timeout"])
	}
	if exe, ok := h["exec"].(string); ok {
		return Entry{Command: exe, Args: stringList(h["args"]), Timeout: timeout, File: file}
	}
	field := "bash"
	if runtime.GOOS == "windows" {
		field = "powershell"
	}
	line, ok := h[field].(string)
	if !ok {
		line, _ = h["command"].(string)
	}
	return Entry{Command: line, Shell: true, Timeout: timeout, File: file}
}

// codexConfig is the part of Codex's config.toml setup reads.
type codexConfig struct {
	MCPServers map[string]struct {
		Command string   `toml:"command"`
		Args    []string `toml:"args"`
		EnvVars []any    `toml:"env_vars"`
	} `toml:"mcp_servers"`
	Hooks struct {
		PreToolUse []struct {
			Hooks []struct {
				Type    string `toml:"type"`
				Command string `toml:"command"`
				Timeout int    `toml:"timeout"`
			} `toml:"hooks"`
		} `toml:"PreToolUse"`
	} `toml:"hooks"`
}

// readCodex reads Codex's config.toml. A missing file is an empty config.
func readCodex(file string) (codexConfig, error) {
	var c codexConfig
	data, err := readFile(file)
	if err != nil || data == nil {
		return c, err
	}
	if _, err = toml.Decode(strings.TrimPrefix(string(data), "\uFEFF"), &c); err != nil {
		return c, fmt.Errorf("%s does not parse as Codex's config: %w", file, err)
	}
	return c, nil
}

// envVarNames reads Codex's env_vars: plain names, or tables with a name and a source.
func envVarNames(list []any) []string {
	var out []string
	for _, v := range list {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			if name, ok := x["name"].(string); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// readJSON reads a JSON object file. A missing file is nil, with no error.
func readJSON(file string) (map[string]any, error) {
	data, err := readFile(file)
	if err != nil || data == nil {
		return nil, err
	}
	return decodeJSON(file, data)
}

// decodeJSON decodes a JSON object. A leading byte order mark is dropped, a file of only white space is
// an empty object, and numbers stay as written.
func decodeJSON(file string, data []byte) (map[string]any, error) {
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("%s does not parse as JSON: %w", file, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s does not parse as JSON: more follows the object", file)
	}
	if root == nil {
		return nil, fmt.Errorf("%s does not parse as JSON: it holds no object", file)
	}
	return root, nil
}

// servers reads a JSON mcpServers object.
func servers(file string, v any) []Entry {
	m, _ := v.(map[string]any)
	var out []Entry
	for _, name := range slices.Sorted(maps.Keys(m)) {
		s, _ := m[name].(map[string]any)
		command, _ := s["command"].(string)
		out = append(out, Entry{Name: name, Command: command, Args: stringList(s["args"]), File: file})
	}
	return out
}

func objects(v any) []map[string]any {
	list, _ := v.([]any)
	var out []map[string]any
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// seconds reads a whole JSON number of seconds; anything else is 0.
func seconds(v any) int {
	n, _ := v.(json.Number)
	i, err := n.Int64()
	if err != nil {
		return 0
	}
	return int(i)
}

// samePath compares two paths as the file system does: without case on Windows, where Claude Code
// writes a drive letter in either case.
func samePath(a, b string) bool {
	a, b = filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
