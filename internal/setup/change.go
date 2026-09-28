package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// Change is one change init makes: a file it edits, or the CLI's own command it runs.
type Change struct {
	CLI    string   // the CLI it sets up; empty for Derbent's own config
	File   string   // the file edited, or the file the command writes
	Run    []string // the command, for a change that runs one
	Manual bool     // the command is not on PATH, so the user runs it
	Text   string   // what is added, exactly as written, or the command as a line to paste
	edit   func(old []byte) ([]byte, error)
}

// Plan returns the changes that set cli up with bin, the absolute path of the derbent binary with
// forward slashes, and names the parts already set up: the MCP entry, when cli has one named derbent,
// and the hook, when a hook already runs derbent gate. It changes nothing. Each file edit is tried
// against the file as it is, so a file that does not parse, or would not with the change, fails here.
func Plan(cli string, p Paths, bin string) (changes []Change, done []string, err error) {
	entries, err := MCPEntries(cli, p, "")
	if err != nil {
		return nil, nil, err
	}
	if slices.ContainsFunc(entries, func(e Entry) bool { return e.Name == "derbent" }) {
		done = append(done, "MCP entry derbent")
	} else {
		changes = append(changes, mcpChange(cli, p, bin))
	}
	hooks, err := Hooks(cli, p)
	if err != nil {
		return nil, nil, err
	}
	if slices.ContainsFunc(hooks, Entry.RunsGate) {
		done = append(done, "hook")
	} else {
		changes = append(changes, hookChange(cli, p, bin))
	}
	for _, c := range changes {
		if c.edit == nil {
			continue
		}
		old, readErr := readFile(c.File)
		if readErr != nil {
			return nil, nil, readErr
		}
		if _, editErr := c.edit(old); editErr != nil {
			return nil, nil, editErr
		}
	}
	return changes, done, nil
}

// mcpChange adds Derbent's MCP entry: through the CLI's own mcp add where it has one, which owns that
// registry, and for Antigravity CLI, which has none, by editing mcp_config.json.
func mcpChange(cli string, p Paths, bin string) Change {
	serve := []string{bin, "mcp", "--agent", cli}
	switch cli {
	case "claude":
		return command(cli, p.MCP, append([]string{"claude", "mcp", "add", "--scope", "user", "derbent", "--"}, serve...))
	case "codex", "copilot":
		return command(cli, p.MCP, append([]string{cli, "mcp", "add", "derbent", "--"}, serve...))
	}
	entry := map[string]any{"command": bin, "args": []any{"mcp", "--agent", cli}}
	return Change{CLI: cli, File: p.MCP, Text: jsonText(map[string]any{"derbent": entry}), edit: editJSON(p.MCP, func(root map[string]any) error {
		entries, err := object(p.MCP, root, "mcpServers")
		if err != nil {
			return err
		}
		entries["derbent"] = entry
		return nil
	})}
}

// command is a change that runs argv, or has the user run it when argv[0] is not on PATH.
func command(cli, file string, argv []string) Change {
	_, err := exec.LookPath(argv[0])
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = ShellWord(a)
	}
	return Change{CLI: cli, File: file, Run: argv, Manual: err != nil, Text: strings.Join(words, " ")}
}

// hookChange adds Derbent's pre-tool hook, in the form each CLI's field is read: exec form for Claude
// Code, whose hook shell is Git Bash or PowerShell depending on what is installed, and a shell line
// elsewhere, with a timeout of HookTimeout where the CLI's default is 30 seconds.
func hookChange(cli string, p Paths, bin string) Change {
	gate := ShellWord(bin) + " gate --agent " + cli
	switch cli {
	case "claude":
		group := map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": bin, "args": []any{"gate", "--agent", "claude"}}}}
		return Change{CLI: cli, File: p.Hook, Text: jsonText(group), edit: editJSON(p.Hook, func(root map[string]any) error {
			hooks, err := object(p.Hook, root, "hooks")
			if err != nil {
				return err
			}
			return appendTo(p.Hook, hooks, "PreToolUse", group)
		})}
	case "codex":
		text := "# derbent init added Derbent's pre-tool hook; derbent doctor checks it.\n" +
			"[[hooks.PreToolUse]]\nmatcher = \".*\"\n\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = " + tomlString(gate) + "\n"
		return Change{CLI: cli, File: p.Hook, Text: text, edit: func(old []byte) ([]byte, error) { return AppendTOML(p.Hook, old, text) }}
	case "copilot":
		hook := map[string]any{"type": "command", "bash": gate, "powershell": `& "` + bin + `" gate --agent copilot`, "timeoutSec": HookTimeout}
		return Change{CLI: cli, File: p.Hook, Text: jsonText(hook), edit: editJSON(p.Hook, func(root map[string]any) error {
			if _, ok := root["version"]; !ok {
				root["version"] = 1
			}
			hooks, err := object(p.Hook, root, "hooks")
			if err != nil {
				return err
			}
			return appendTo(p.Hook, hooks, "preToolUse", hook)
		})}
	}
	def := map[string]any{"PreToolUse": []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": gate, "timeout": HookTimeout}}}}}
	return Change{CLI: cli, File: p.Hook, Text: jsonText(map[string]any{"derbent": def}), edit: editJSON(p.Hook, func(root map[string]any) error {
		if _, taken := root["derbent"]; taken {
			return fmt.Errorf("%s already has a hook named derbent that does not run derbent gate", p.Hook)
		}
		root["derbent"] = def
		return nil
	})}
}

// NewFile is a change that writes text to file, which must not exist when the change is made.
func NewFile(file, text string) Change {
	return Change{File: file, Text: text, edit: func(old []byte) ([]byte, error) {
		if old != nil {
			return nil, fmt.Errorf("%s exists, and derbent init never replaces it", file)
		}
		return []byte(text), nil
	}}
}

// Apply makes the change. A command runs with the CLI's output passed on to stdout and stderr once it
// ends, each line escaped. A file is read again, edited and written back; a missing file is created,
// with its directory, readable by the user only, and an existing one keeps its permissions.
func (c Change) Apply(ctx context.Context, stdout, stderr io.Writer) error {
	if c.Run != nil {
		cmd := exec.CommandContext(ctx, c.Run[0], c.Run[1:]...) //nolint:gosec // the CLI's own mcp add, shown to the user before it runs
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		writeEscaped(stdout, out.String())
		writeEscaped(stderr, errOut.String())
		if err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c.Run[:3], " "), err)
		}
		return nil
	}
	old, err := readFile(c.File)
	if err != nil {
		return err
	}
	out, err := c.edit(old)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(c.File), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(c.File), err)
	}
	if err = os.WriteFile(c.File, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", c.File, err)
	}
	return nil
}

// writeEscaped writes a CLI's output line by line, each line escaped, so the CLI cannot move the cursor
// or hide text on the user's terminal.
func writeEscaped(w io.Writer, out string) {
	out = strings.TrimRight(out, "\r\n")
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fmt.Fprintln(w, visible.Escape(strings.TrimSuffix(line, "\r")))
	}
}

// Backup copies file, when it exists, to BackupName(file, now), created new and readable by the user
// only, and returns the copy's path, or "" when there is no file. An earlier copy is never replaced.
func Backup(file string, now time.Time) (string, error) {
	data, err := readFile(file)
	if err != nil || data == nil {
		return "", err
	}
	dst := BackupName(file, now)
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // next to the user's own config file
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", file, err)
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", file, err)
	}
	return dst, nil
}

// BackupName is where Backup copies file: <file>.derbent-backup-<UTC time>.
func BackupName(file string, now time.Time) string {
	return file + ".derbent-backup-" + now.UTC().Format("20060102T150405Z")
}

// AppendTOML returns old with text appended after a blank line, once old and the result both parse as
// TOML. old is never re-encoded, so its bytes, comments included, stay as they are.
func AppendTOML(file string, old []byte, text string) ([]byte, error) {
	var before map[string]any
	if _, err := toml.Decode(strings.TrimPrefix(string(old), "\uFEFF"), &before); err != nil {
		return nil, fmt.Errorf("%s does not parse as TOML: %w", file, err)
	}
	sep := ""
	switch {
	case len(old) == 0:
	case bytes.HasSuffix(old, []byte("\n")):
		sep = "\n"
	default:
		sep = "\n\n"
	}
	out := append(slices.Clone(old), sep+text...)
	var after map[string]any
	if _, err := toml.Decode(strings.TrimPrefix(string(out), "\uFEFF"), &after); err != nil {
		return nil, fmt.Errorf("%s would not parse with Derbent's hook appended: %w", file, err)
	}
	return out, nil
}

// ShellWord is bin as one word of a command line: as it is when it holds only letters, digits and
// / . _ - : + ~ @, which no shell reads specially, and otherwise in double quotes, which sh, bash,
// cmd.exe and PowerShell all read as one word. cmd.exe, which runs Codex's hooks on Windows, also splits
// words at , and =, so those are quoted too. CheckBinary refuses what double quotes cannot hold.
func ShellWord(bin string) string {
	special := strings.IndexFunc(bin, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("/._-:+~@", r)
	})
	if special < 0 && bin != "" {
		return bin
	}
	return `"` + bin + `"`
}

// CheckBinary refuses a binary path that some shell reads even inside double quotes: one that holds ",
// $, `, %, ! or a control character. It also refuses &, ^, |, < and >: a CLI installed as a .cmd shim,
// as npm installs them, runs through cmd.exe, and Go quotes an argument for it only when the argument
// holds a space, so cmd.exe would end the command at & or drop a ^, and the CLI would register a path
// that does not exist.
func CheckBinary(bin string) error {
	for _, r := range bin {
		if strings.ContainsRune("\"$`%!&^|<>", r) || unicode.IsControl(r) {
			return fmt.Errorf("the derbent binary's path %q holds %q, which a shell reads specially: move the binary to a plain path and run derbent init again", bin, r)
		}
	}
	return nil
}

// readFile reads file; a missing file is nil, with no error.
func readFile(file string) ([]byte, error) {
	data, err := os.ReadFile(file) //nolint:gosec // a CLI's config file in the user's home
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return data, nil
}

// editJSON returns an edit that decodes a JSON object file (a missing file as {}), lets add change it,
// and writes it back with two-space indentation. The other keys and values stay; their order may not.
func editJSON(file string, add func(root map[string]any) error) func([]byte) ([]byte, error) {
	return func(old []byte) ([]byte, error) {
		root, err := decodeJSON(file, old)
		if err != nil {
			return nil, err
		}
		if err = add(root); err != nil {
			return nil, err
		}
		return []byte(jsonText(root) + "\n"), nil
	}
}

// object returns m[key] as an object, adding an empty one where the key is missing or null. A key that
// holds anything else is an error.
func object(file string, m map[string]any, key string) (map[string]any, error) {
	switch v := m[key].(type) {
	case nil:
		o := map[string]any{}
		m[key] = o
		return o, nil
	case map[string]any:
		return v, nil
	}
	return nil, fmt.Errorf("%s: %q is not a JSON object", file, key)
}

// appendTo appends v to the array m[key], starting one where the key is missing or null.
func appendTo(file string, m map[string]any, key string, v any) error {
	switch list := m[key].(type) {
	case nil:
		m[key] = []any{v}
	case []any:
		m[key] = append(list, v)
	default:
		return fmt.Errorf("%s: %q is not a JSON array", file, key)
	}
	return nil
}

// jsonText is v as JSON with two-space indentation, with <, > and & left as they are.
func jsonText(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err) // maps, slices, strings and numbers, decoded or built here, always encode
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// tomlString is s as a TOML basic string: JSON's string escapes are all valid TOML ones.
func tomlString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // a string always encodes
	}
	return string(b)
}
