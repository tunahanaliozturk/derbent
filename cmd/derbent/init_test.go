package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/preset"
	"github.com/tunahanaliozturk/derbent/internal/setup"
)

// fakeCLIs are the CLIs whose own mcp add init runs. The tests put fakes of them on PATH.
var fakeCLIs = []string{"claude", "codex", "copilot"}

// fakeCLIName is claude, codex or copilot when this test binary runs as a fake of that CLI: a copy named
// after it, started while DERBENT_TEST_FAKE_CLI_LOG names the log it appends its arguments to.
func fakeCLIName() string {
	if os.Getenv("DERBENT_TEST_FAKE_CLI_LOG") == "" {
		return ""
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if slices.Contains(fakeCLIs, name) {
		return name
	}
	return ""
}

// runFakeCLI stands in for `<cli> mcp add [--scope user] <name> -- <command> <args...>`. It appends its
// arguments to the log, one line per run, writes the entry where the real CLI does, so a later init or
// doctor finds it, and says so with an escape sequence in the line, as a hostile CLI could. As
// `claude --version` it prints DERBENT_TEST_FAKE_CLAUDE_VERSION, or a recent version, and logs nothing.
func runFakeCLI(cli string, args []string) int {
	if cli == "claude" && slices.Equal(args, []string{"--version"}) {
		v := os.Getenv("DERBENT_TEST_FAKE_CLAUDE_VERSION")
		if v == "" {
			v = "2.1.283 (Claude Code)"
		}
		fmt.Println(v)
		return 0
	}
	logFile, err := os.OpenFile(os.Getenv("DERBENT_TEST_FAKE_CLI_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 3
	}
	fmt.Fprintln(logFile, cli+" "+strings.Join(args, " "))
	logFile.Close()
	sep := slices.Index(args, "--")
	if len(args) < 4 || args[0] != "mcp" || args[1] != "add" || sep < 3 || sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "fake", cli+": unexpected arguments", args)
		return 2
	}
	name, command, rest := args[sep-1], args[sep+1], args[sep+2:]
	home, err := os.UserHomeDir()
	if err != nil {
		return 3
	}
	switch cli {
	case "codex":
		quoted := make([]string, len(rest))
		for i, a := range rest {
			quoted[i] = strconv.Quote(a)
		}
		err = appendFile(filepath.Join(home, ".codex", "config.toml"),
			fmt.Sprintf("\n[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n", name, strconv.Quote(command), strings.Join(quoted, ", ")))
	case "copilot":
		err = setJSONServer(filepath.Join(home, ".copilot", "mcp-config.json"), name, command, rest)
	default:
		err = setJSONServer(filepath.Join(home, ".claude.json"), name, command, rest)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added MCP server %s\x1b[2J\n", name)
	return 0
}

func appendFile(file, text string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func setJSONServer(file, name, command string, args []string) error {
	root := map[string]any{}
	if data, readErr := os.ReadFile(file); readErr == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return err
		}
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	servers[name] = map[string]any{"type": "stdio", "command": command, "args": args}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// scratchHome points every directory init and doctor read at a new scratch home: HOME, USERPROFILE,
// Derbent's config and state directories, and no CLAUDE_CONFIG_DIR, CODEX_HOME or COPILOT_HOME. PATH
// holds only fake claude, codex and copilot commands, which log their arguments to the file returned.
// A test that calls it cannot reach the real CLIs or their configs.
func scratchHome(t *testing.T) (home, log string) {
	t.Helper()
	home = t.TempDir()
	useConfigDir(t, home) // APPDATA, XDG_CONFIG_HOME and HOME
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("XDG_STATE_HOME", home)
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME"} {
		t.Setenv(k, "")
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, cli := range fakeCLIs {
		dst := filepath.Join(bin, cli+exeSuffix())
		if os.Link(os.Args[0], dst) == nil {
			continue
		}
		data, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dst, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	log = filepath.Join(home, "cli.log")
	t.Setenv("DERBENT_TEST_FAKE_CLI_LOG", log)
	t.Setenv("DERBENT_TEST_MAIN", "1") // doctor starts the hook's binary, this test binary, with version
	return home, log
}

// initCmd runs derbent init with stdin and returns what it printed and its error.
func initCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(t.Context(), append([]string{"init"}, args...), strings.NewReader(stdin), &out, io.Discard)
	return out.String(), err
}

// testBinary is the path init writes for this test binary.
func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(exe)
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshot reads every file under home but the fake CLIs, keyed by path.
func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, walkErr error) error {
		switch {
		case walkErr != nil:
			return walkErr
		case d.IsDir() && d.Name() == "bin":
			return filepath.SkipDir
		case !d.IsDir():
			files[path] = readString(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// backupsOf lists the copies init made of file.
func backupsOf(t *testing.T, file string) []string {
	t.Helper()
	list, err := filepath.Glob(file + ".derbent-backup-*")
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestInitSetsUpEveryCLI(t *testing.T) {
	home, log := scratchHome(t)
	bin := testBinary(t)
	word := setup.ShellWord(bin) // the binary in a shell line
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	const settingsBefore = `{"model": "opus", "hooks": {"PostToolUse": [{"matcher": "Write", "hooks": [{"type": "command", "command": "fmt.sh"}]}]}}`
	writeFile(t, settings, settingsBefore)
	codexConfig := filepath.Join(home, ".codex", "config.toml")
	const codexBefore = "# my Codex settings\nmodel = \"gpt-5\" # the default\n"
	writeFile(t, codexConfig, codexBefore)

	out, err := initCmd(t, "", "--yes")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	// Each CLI's own command ran once, with the absolute path of the binary.
	wantLog := "claude mcp add --scope user derbent -- " + bin + " mcp --agent claude\n" +
		"codex mcp add derbent -- " + bin + " mcp --agent codex\n" +
		"copilot mcp add derbent -- " + bin + " mcp --agent copilot\n"
	if got := readString(t, log); got != wantLog {
		t.Errorf("CLI commands:\n%s\nwant:\n%s", got, wantLog)
	}

	// Claude Code: the hook in exec form, the user's own keys kept, and a copy of the file as it was.
	var s struct {
		Model string
		Hooks map[string][]struct {
			Matcher string
			Hooks   []struct {
				Type, Command string
				Args          []string
			}
		}
	}
	if err = json.Unmarshal([]byte(readString(t, settings)), &s); err != nil {
		t.Fatal(err)
	}
	if s.Model != "opus" || len(s.Hooks["PostToolUse"]) != 1 || len(s.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("settings.json = %+v", s)
	}
	if h := s.Hooks["PreToolUse"][0]; h.Matcher != "*" || h.Hooks[0].Command != bin || !slices.Equal(h.Hooks[0].Args, []string{"gate", "--agent", "claude"}) {
		t.Errorf("Claude Code hook = %+v", h)
	}
	if b := backupsOf(t, settings); len(b) != 1 || readString(t, b[0]) != settingsBefore {
		t.Errorf("settings.json copies = %v, want one holding the file as it was", b)
	}

	// Codex: the file's own bytes kept, comments included, and the hook appended after the entry the
	// CLI added; the copy holds the file from before both.
	got := readString(t, codexConfig)
	var c struct {
		MCPServers map[string]struct{ Command string } `toml:"mcp_servers"`
		Hooks      struct {
			PreToolUse []struct {
				Matcher string
				Hooks   []struct{ Command string }
			}
		}
	}
	if _, err = toml.Decode(got, &c); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, codexBefore) || c.MCPServers["derbent"].Command != bin ||
		len(c.Hooks.PreToolUse) != 1 || c.Hooks.PreToolUse[0].Hooks[0].Command != word+" gate --agent codex" {
		t.Errorf("config.toml:\n%s", got)
	}
	if b := backupsOf(t, codexConfig); len(b) != 1 || readString(t, b[0]) != codexBefore {
		t.Errorf("config.toml copies = %v, want one holding the file as it was", b)
	}

	// Copilot CLI: a new hooks file, readable by the user only, with a timeout above the approvals'.
	hooksFile := filepath.Join(home, ".copilot", "hooks", "derbent.json")
	var h struct {
		Version int
		Hooks   struct {
			PreToolUse []struct {
				Bash, Powershell string
				TimeoutSec       int
			}
		}
	}
	if err = json.Unmarshal([]byte(readString(t, hooksFile)), &h); err != nil {
		t.Fatal(err)
	}
	if h.Version != 1 || len(h.Hooks.PreToolUse) != 1 || h.Hooks.PreToolUse[0].TimeoutSec != 120 ||
		h.Hooks.PreToolUse[0].Powershell != `& "`+bin+`" gate --agent copilot` || h.Hooks.PreToolUse[0].Bash != word+" gate --agent copilot" {
		t.Errorf("derbent.json = %+v", h)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]fs.FileMode{hooksFile: 0o600, filepath.Dir(hooksFile): 0o700} {
			if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != want {
				t.Errorf("%s: mode %v, err %v; want %v", path, info.Mode().Perm(), statErr, want)
			}
		}
	}

	// Antigravity CLI: both files written by init itself.
	var m struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		}
	}
	if err = json.Unmarshal([]byte(readString(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if e := m.MCPServers["derbent"]; e.Command != bin || !slices.Equal(e.Args, []string{"mcp", "--agent", "antigravity"}) {
		t.Errorf("mcp_config.json = %+v", m)
	}
	var ag map[string]struct {
		PreToolUse []struct {
			Matcher string
			Hooks   []struct {
				Command string
				Timeout int
			}
		}
	}
	if err = json.Unmarshal([]byte(readString(t, filepath.Join(home, ".gemini", "config", "hooks.json"))), &ag); err != nil {
		t.Fatal(err)
	}
	if d := ag["derbent"]; len(d.PreToolUse) != 1 || d.PreToolUse[0].Hooks[0].Command != word+" gate --agent antigravity" || d.PreToolUse[0].Hooks[0].Timeout != 120 {
		t.Errorf("hooks.json = %+v", ag)
	}

	for _, want := range []string{
		"claude: set up\n", "codex: set up; Codex runs the new hook only after you trust it in /hooks\n", "copilot: set up\n", "antigravity: set up\n",
		"every call is allowed until one exists", "derbent init --preset watch", "run derbent doctor to check",
		"Added MCP server derbent\\u001b[2J\n", // the CLI's own output, escaped
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("init passed a CLI's escape sequence to the terminal:\n%q", out)
	}
}

// A second run finds every CLI set up and changes nothing: no command runs, no file or copy changes.
func TestInitTwiceChangesNothing(t *testing.T) {
	home, _ := scratchHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := initCmd(t, "", "--yes"); err != nil {
		t.Fatalf("first init: %v\n%s", err, out)
	}
	before := snapshot(t, home)
	out, err := initCmd(t, "", "--yes")
	if err != nil {
		t.Fatalf("second init: %v\n%s", err, out)
	}
	if after := snapshot(t, home); !maps.Equal(before, after) {
		t.Errorf("the second run changed files: before %v, after %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
	for _, cli := range []string{"claude", "codex", "copilot", "antigravity"} {
		if !strings.Contains(out, cli+": already set up") {
			t.Errorf("init printed:\n%s\nwant %s already set up", out, cli)
		}
	}
}

// Entries the user wrote, in other forms than init's, count as set up and stay as they are.
func TestInitLeavesAnExistingEntryAlone(t *testing.T) {
	home, log := scratchHome(t)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"derbent": {"command": "derbent", "args": ["mcp", "--agent", "claude"]}}}`)
	settings := filepath.Join(home, ".claude", "settings.json")
	const mine = `{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "\"C:/Program Files/Derbent/derbent.exe\" gate --agent claude"}]}]}}`
	writeFile(t, settings, mine)
	out, err := initCmd(t, "", "--yes", "--cli", "claude")
	if err != nil || !strings.Contains(out, "claude: already set up") {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if readString(t, settings) != mine || len(backupsOf(t, settings)) != 0 {
		t.Error("settings.json changed or was copied")
	}
	if _, statErr := os.Stat(log); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a CLI command ran: %s", readString(t, log))
	}
}

// A file that does not parse is left byte for byte, its CLI fails with the file named and runs no
// command, and the other CLIs are still set up.
func TestInitLeavesAFileThatDoesNotParse(t *testing.T) {
	home, log := scratchHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	const broken = `{"hooks": {"PreToolUse": [`
	writeFile(t, settings, broken)
	codexConfig := filepath.Join(home, ".codex", "config.toml")
	const codexOff = "hooks = \"off\"\n" // parses, but the hook's tables cannot be appended to it
	writeFile(t, codexConfig, codexOff)

	out, err := initCmd(t, "", "--yes", "--cli", "claude,codex,copilot")
	if !errors.Is(err, errInitFailed) {
		t.Fatalf("err = %v, want errInitFailed\n%s", err, out)
	}
	if readString(t, settings) != broken || readString(t, codexConfig) != codexOff ||
		len(backupsOf(t, settings)) != 0 || len(backupsOf(t, codexConfig)) != 0 {
		t.Error("a file that could not be set up was changed or copied")
	}
	if got := readString(t, log); !strings.HasPrefix(got, "copilot mcp add") || strings.Count(got, "\n") != 1 {
		t.Errorf("CLI commands:\n%s\nwant only Copilot CLI's", got)
	}
	for _, want := range []string{"claude: failed: " + settings, "codex: failed: " + codexConfig, "copilot: set up"} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed:\n%s\nwant %q", out, want)
		}
	}
}

// --dry-run prints every change, the preset included, and writes and runs nothing.
func TestInitDryRunWritesNothing(t *testing.T) {
	home, log := scratchHome(t)
	word := setup.ShellWord(testBinary(t))
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, home)
	out, err := initCmd(t, "", "--dry-run", "--yes", "--preset", "balanced")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if after := snapshot(t, home); !maps.Equal(before, after) {
		t.Errorf("--dry-run wrote files: %v", slices.Sorted(maps.Keys(after)))
	}
	if _, statErr := os.Stat(log); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a CLI command ran: %s", readString(t, log))
	}
	for _, want := range []string{
		"claude mcp add --scope user derbent -- " + word + " mcp --agent claude", `"--agent",`,
		word + " gate --agent codex", "the balanced preset", "claude: skipped: dry run", "antigravity: skipped: dry run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed:\n%s\nwant %q", out, want)
		}
	}
}

// Without --yes, init asks once: only y or yes writes.
func TestInitWritesOnlyAfterYes(t *testing.T) {
	home, log := scratchHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	for _, answer := range []string{"n\n", "", "yes please\n"} {
		out, err := initCmd(t, answer, "--cli", "claude")
		if err != nil || !strings.Contains(out, "claude: skipped: not confirmed") {
			t.Fatalf("answer %q: %v\n%s", answer, err, out)
		}
		if _, statErr := os.Stat(settings); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("answer %q wrote settings.json", answer)
		}
	}
	if _, statErr := os.Stat(log); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("a CLI command ran: %s", readString(t, log))
	}
	out, err := initCmd(t, "Y\n", "--cli", "claude")
	if err != nil || !strings.Contains(out, "claude: set up") || !strings.Contains(readString(t, settings), "gate") {
		t.Fatalf("answer Y: %v\n%s", err, out)
	}
}

// --preset writes Derbent's config only where there is none, --print prints it, and an unknown name
// lists the three.
func TestInitPreset(t *testing.T) {
	scratchHome(t)
	cfg, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	balanced, err := preset.Text("balanced")
	if err != nil {
		t.Fatal(err)
	}
	out, err := initCmd(t, "", "--yes", "--cli", "claude", "--preset", "balanced")
	if err != nil || readString(t, cfg) != balanced || !strings.Contains(out, "wrote the balanced preset") {
		t.Fatalf("init --preset balanced: %v\n%s", err, out)
	}
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(cfg); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("config mode %v, err %v; want 0600", info.Mode().Perm(), statErr)
		}
	}
	out, err = initCmd(t, "", "--yes", "--cli", "claude", "--preset", "strict")
	if err != nil || readString(t, cfg) != balanced || !strings.Contains(out, "never replaces a config") {
		t.Fatalf("init --preset strict over a config: %v\n%s", err, out)
	}
	watch, err := preset.Text("watch")
	if err != nil {
		t.Fatal(err)
	}
	if out, err = initCmd(t, "", "--preset", "watch", "--print"); err != nil || out != watch {
		t.Fatalf("--print: %v\n%q", err, out)
	}
	if _, err = initCmd(t, "", "--preset", "lenient"); err == nil || !strings.Contains(err.Error(), "use one of watch, balanced, strict") {
		t.Fatalf("unknown preset: err = %v", err)
	}
	if _, err = initCmd(t, "", "--print"); err == nil || !strings.Contains(err.Error(), "--print needs --preset") {
		t.Fatalf("--print alone: err = %v", err)
	}
}

// A CLI named with --cli that is not installed still gets its files, and its mcp add is printed for the
// user to run instead of run.
func TestInitPrintsTheCommandOfACLIThatIsNotInstalled(t *testing.T) {
	home, log := scratchHome(t)
	word := setup.ShellWord(testBinary(t))
	if err := os.Remove(filepath.Join(home, "bin", "codex"+exeSuffix())); err != nil {
		t.Fatal(err)
	}
	out, err := initCmd(t, "", "--yes", "--cli", "codex")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(log); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a CLI command ran: %s", readString(t, log))
	}
	if !strings.Contains(readString(t, filepath.Join(home, ".codex", "config.toml")), "gate --agent codex") {
		t.Error("the Codex hook was not written")
	}
	if want := "codex: set up, except the MCP entry: run codex mcp add derbent -- " + word + " mcp --agent codex"; !strings.Contains(out, want) {
		t.Errorf("init printed:\n%s\nwant %q", out, want)
	}
}

func TestInitNamesWhatItCannotSetUp(t *testing.T) {
	home, _ := scratchHome(t)
	if _, err := initCmd(t, "", "--cli", "claude,gemini"); err == nil || !strings.Contains(err.Error(), "use claude, codex, copilot, antigravity") {
		t.Fatalf("unknown CLI: err = %v", err)
	}
	for _, cli := range fakeCLIs {
		if err := os.Remove(filepath.Join(home, "bin", cli+exeSuffix())); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := initCmd(t, "", "--yes"); err != nil || !strings.Contains(out, "No CLI found") {
		t.Fatalf("no CLI: %v\n%s", err, out)
	}
}

// A derbent gate hook in another file the CLI reads user hooks from counts as set up: Codex's
// hooks.json, and Antigravity CLI's settings.json. init adds no second one.
func TestInitSeesAHookInTheCLIsOtherFiles(t *testing.T) {
	home, _ := scratchHome(t)
	writeFile(t, filepath.Join(home, ".codex", "hooks.json"),
		`{"hooks": {"PreToolUse": [{"matcher": ".*", "hooks": [{"type": "command", "command": "derbent gate --agent codex"}]}]}}`)
	writeFile(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"),
		`{"hooks": {"mine": {"PreToolUse": [{"matcher": ".*", "hooks": [{"command": "derbent gate --agent antigravity"}]}]}}}`)
	out, err := initCmd(t, "", "--yes", "--cli", "codex,antigravity")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{"codex: set up; already there: hook\n", "antigravity: set up; already there: hook\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(readString(t, filepath.Join(home, ".codex", "config.toml")), "gate") {
		t.Error("a second Codex hook was added to config.toml")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".gemini", "config", "hooks.json")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("a second Antigravity CLI hook was added to hooks.json")
	}
}

// Claude Code before 2.1.139 ignores a hook's args and would start derbent with none, so the hook would
// decide nothing: init sets nothing up for it and says to upgrade. A version it cannot read is a note.
func TestInitSkipsAClaudeCodeTooOldForExecForm(t *testing.T) {
	home, log := scratchHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	t.Setenv("DERBENT_TEST_FAKE_CLAUDE_VERSION", "2.1.100 (Claude Code)")
	out, err := initCmd(t, "", "--yes", "--cli", "claude")
	if err != nil || !strings.Contains(out, "claude: skipped: Claude Code 2.1.100 is older than 2.1.139") || !strings.Contains(out, "upgrade it") {
		t.Fatalf("old Claude Code: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(settings); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("settings.json was written for an old Claude Code")
	}
	if _, statErr := os.Stat(log); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a CLI command ran: %s", readString(t, log))
	}
	t.Setenv("DERBENT_TEST_FAKE_CLAUDE_VERSION", "2.1.139-beta.1 (Claude Code)") // a pre-release comes before its release
	out, err = initCmd(t, "", "--yes", "--cli", "claude")
	if err != nil || !strings.Contains(out, "claude: skipped: Claude Code 2.1.139-beta.1 is older than 2.1.139") {
		t.Fatalf("pre-release Claude Code: %v\n%s", err, out)
	}
	t.Setenv("DERBENT_TEST_FAKE_CLAUDE_VERSION", "claude, some build")
	out, err = initCmd(t, "", "--yes", "--cli", "claude")
	if err != nil || !strings.Contains(out, "claude: note: could not read Claude Code's version") || !strings.Contains(out, "claude: set up") {
		t.Fatalf("unreadable version: %v\n%s", err, out)
	}
}

// A derbent gate hook whose matcher lets only some tools through gates only those. init adds no second
// hook beside it, changes nothing for the CLI, and says to widen the matcher.
func TestInitSaysToWidenAHookThatGatesSomeTools(t *testing.T) {
	home, log := scratchHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	const mine = `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "derbent", "args": ["gate", "--agent", "claude"]}]}]}}`
	writeFile(t, settings, mine)
	out, err := initCmd(t, "", "--yes", "--cli", "claude")
	if err != nil || !strings.Contains(out, `claude: skipped: the derbent gate hook matches only some tools: the one in `+settings+` matches "Bash"`) ||
		!strings.Contains(out, `set its matcher to "*"`) {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if readString(t, settings) != mine || len(backupsOf(t, settings)) != 0 {
		t.Error("settings.json was changed or copied")
	}
	if _, statErr := os.Stat(log); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a CLI command ran: %s", readString(t, log))
	}
}

// When a change fails after others for the same CLI were made, init names what was made and where the
// copy of each file is, so the user can finish or undo it.
func TestInitSaysWhatWasMadeWhenAChangeFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	home, _ := scratchHome(t)
	registry := filepath.Join(home, ".claude.json")
	writeFile(t, registry, `{"numStartups": 3}`)
	settings := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, settings, `{}`)
	if err := os.Chmod(settings, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(settings, 0o600) })
	out, err := initCmd(t, "", "--yes", "--cli", "claude")
	if !errors.Is(err, errInitFailed) {
		t.Fatalf("err = %v, want errInitFailed\n%s", err, out)
	}
	copies, settingsCopies := backupsOf(t, registry), backupsOf(t, settings)
	if len(copies) != 1 || len(settingsCopies) != 1 {
		t.Fatalf(".claude.json copies %v, settings.json copies %v; want one each", copies, settingsCopies)
	}
	for _, want := range []string{
		"claude: failed: write " + settings,
		"the copy of " + settings + " made before this change: " + settingsCopies[0],
		"already made: ran claude mcp add, which writes " + registry + " (copied first to " + copies[0] + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed:\n%s\nwant %q", out, want)
		}
	}
}

// With claude not on PATH its version cannot be checked, so init says what the hook it writes needs.
func TestInitNotesTheVersionTheHookNeedsWhenClaudeIsNotOnPATH(t *testing.T) {
	home, _ := scratchHome(t)
	if err := os.Remove(filepath.Join(home, "bin", "claude"+exeSuffix())); err != nil {
		t.Fatal(err)
	}
	out, err := initCmd(t, "", "--yes", "--cli", "claude")
	if err != nil || !strings.Contains(out, "claude: note: claude is not on PATH, so its version was not checked; the hook init writes needs Claude Code 2.1.139 or later") {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(readString(t, filepath.Join(home, ".claude", "settings.json")), "gate") {
		t.Error("the hook was not written")
	}
}

// A file init only looks in for an existing hook, and never writes, may not parse: Antigravity CLI takes
// comments in its settings.json. init goes on, leaves the file alone and says it could not check it.
func TestInitGoesOnPastAHookFileItOnlyReads(t *testing.T) {
	home, _ := scratchHome(t)
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	const mine = "{\n  // my settings\n  \"model\": \"x\"\n}\n"
	writeFile(t, settings, mine)
	out, err := initCmd(t, "", "--yes", "--cli", "antigravity")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{
		"antigravity: note: could not read " + settings + " as JSON, so it was not checked for a derbent gate hook: if it holds one, remove one of the two",
		"antigravity: set up\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed:\n%s\nwant %q", out, want)
		}
	}
	if !strings.Contains(readString(t, filepath.Join(home, ".gemini", "config", "hooks.json")), "gate --agent antigravity") {
		t.Error("the hook was not written")
	}
	if readString(t, settings) != mine || len(backupsOf(t, settings)) != 0 {
		t.Error("settings.json was changed or copied")
	}
}
