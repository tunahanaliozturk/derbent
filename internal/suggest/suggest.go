// Package suggest reads the calls the user approved or denied and finds the rules they point to (ADR
// 0019): an allow for what the user keeps approving, a deny for what they keep denying. It only suggests;
// Derbent never writes a rule into the config.
package suggest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/redact"
	"github.com/tunahanaliozturk/derbent/internal/rule"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// Min is how many approvals, or denials, of one agent's calls to one tool make a suggestion, unless derbent
// suggest is given --min. The UI counts with it.
const Min = 5

// commandKeys are the arguments that hold a shell command: command for the shell tools of Claude Code,
// Codex and Copilot CLI, and CommandLine for Antigravity CLI's run_command. A tool whose calls carry one is
// a shell, and a rule for it is suggested only with the command, or the start its commands share.
var commandKeys = [...]string{"command", "CommandLine"}

// shellTools are the four CLIs' shell tools, each with the one key it runs, which are shells by name
// whatever their calls carry: a call whose command cannot be read, even one with no arguments at all, or
// that carries only the other key, which the tool does not run, gives the group no suggestion.
// typedTools are Copilot CLI's tools that type text into a shell that is already running, where it can be
// a command or the answer to a prompt; they never get a suggestion.
var (
	shellTools = map[string]string{
		"native__Bash": "command", "native__bash": "command", "native__PowerShell": "command", "native__powershell": "command",
		"native__Monitor": "command", "native__run_command": "CommandLine",
	}
	typedTools = [...]string{"native__write_bash", "native__write_powershell"}
)

// launchers are the shells, interpreters and programs that run another program or code given as text. A
// command with one of them in any word, as program spells it, gets no suggestion. The list cannot be
// complete: a text rule can be fooled, and an option of an ordinary program can run another one.
var launchers = [...]string{
	"sh", "bash", "zsh", "dash", "ksh", "csh", "tcsh", "fish", "busybox", "ash", "mksh", "yash", "nu", "xonsh",
	"pwsh", "powershell", "cmd", "wsl", "python", "pythonw", "py", "pypy", "pypy3", "ipython", "node", "nodejs",
	"ts-node", "tsx", "deno", "bun", "perl", "ruby", "php", "lua", "rscript", "osascript", "awk", "gawk", "mawk",
	"tclsh", "expect", "sudo", "doas", "su", "runas", "gsudo", "pkexec", "env", "xargs", "eval", "exec", "command",
	"builtin", ".", "source", "nohup", "nice", "ionice", "chrt", "taskset", "time", "timeout", "watch", "setsid",
	"stdbuf", "flock", "script", "unshare", "nsenter", "chroot", "systemd-run", "ssh", "npx", "bunx", "pnpx", "uvx",
	"pipx", "start", "start-process", "start-job", "sajb", "saps", "invoke-expression", "iex", "invoke-command", "icm",
	"invoke-item", "ii", "call", "mshta", "rundll32", "cscript", "wscript", "wmic", "forfiles", "schtasks",
	"invoke-wmimethod", "invoke-cimmethod", "conhost", "pcalua", "msiexec", "regsvr32",
}

// operators are what can end a command and start another, feed it, or run one inside it: the separators
// and pipe, a backtick, the ( of $( ), <( ), >( ) and PowerShell's ( ), which runs what it holds, the
// redirects, and the line breaks. A command that holds one gets no suggestion, and a shell's allow for a
// prefix gets an ask above it for each, so a command that starts with the prefix and holds one still asks.
const operators = ";&|`(<>\n\r"

// Suggestion is a rule the user's answers point to.
type Suggestion struct {
	Agent, Tool string
	Action      rule.Action // allow for approvals, deny for denials
	Key         string      // the command argument, command or CommandLine, for a shell tool; "" for any other
	Prefix      string      // what every answered command starts with, for a shell tool
	Exact       bool        // every answered command was Prefix, so the pattern is Prefix alone, with no *
	Approved    int
	Denied      int
	Rules       []int // the rules in the user's config that asked, lowest first
}

// From groups the answered calls by agent and tool and returns a suggestion for each group with at least
// least approvals and no denial, or at least least denials and no approval, sorted by agent and tool.
// Calls a project's rules asked about are left out: a rule in the user's config cannot loosen a project's
// rules. A group gets no suggestion when a rule could not name it (an agent that is no agent label, an
// empty tool name or one with a wildcard), when it is a tool that types into a running shell, or when it
// is a shell whose commands cannot all be read, or share no command or start that a rule can take safely
// (see pattern and refused).
func From(calls []approval.Answered, least int) []Suggestion {
	type group struct{ agent, tool string }
	byGroup := map[group][]approval.Answered{}
	for _, c := range calls {
		if !c.ProjectRule {
			k := group{c.Agent, c.Tool}
			byGroup[k] = append(byGroup[k], c)
		}
	}
	var out []Suggestion
	for k, answered := range byGroup {
		if s, ok := suggestFor(k.agent, k.tool, answered, least); ok {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Suggestion) int {
		return cmp.Or(strings.Compare(a.Agent, b.Agent), strings.Compare(a.Tool, b.Tool))
	})
	return out
}

// suggestFor is the suggestion one agent's answered calls to one tool make, if they make one.
func suggestFor(agent, tool string, calls []approval.Answered, least int) (Suggestion, bool) {
	s := Suggestion{Agent: agent, Tool: tool}
	for _, c := range calls {
		if c.Approved {
			s.Approved++
		} else {
			s.Denied++
		}
		if c.Rule > 0 && !slices.Contains(s.Rules, c.Rule) {
			s.Rules = append(s.Rules, c.Rule)
		}
	}
	slices.Sort(s.Rules)
	switch {
	case !config.AgentLabel.MatchString(agent) || tool == "" || strings.ContainsAny(tool, "*?"):
		return Suggestion{}, false // no rule matches that agent, or matches only that tool
	case slices.Contains(typedTools[:], tool):
		return Suggestion{}, false
	case s.Approved >= least && s.Denied == 0:
		s.Action = rule.Allow
	case s.Denied >= least && s.Approved == 0:
		s.Action = rule.Deny
	default:
		return Suggestion{}, false
	}
	key, cmds, shell, readable := shellCommands(calls)
	runs, known := shellTools[tool]
	switch {
	case !shell && !known:
		return s, true
	case !readable || (known && key != runs):
		return Suggestion{}, false
	}
	s.Key = key
	s.Prefix, s.Exact = pattern(cmds)
	return s, !refused(s.Prefix)
}

// shellCommands reads the command each call carries. shell reports that any call carries a command
// argument, which makes the tool a shell; readable, that every call carries exactly one, as a string,
// under one name, key.
func shellCommands(calls []approval.Answered) (key string, cmds []string, shell, readable bool) {
	readable = true
	for _, c := range calls {
		var args map[string]any
		_ = json.Unmarshal([]byte(c.Args), &args) // arguments that do not decode carry no command
		name, value, ok := command(args)
		shell = shell || name != ""
		if !ok || name == "" || (key != "" && name != key) {
			readable = false
			continue
		}
		key, cmds = name, append(cmds, value)
	}
	return key, cmds, shell, readable
}

// command returns the call's command argument and its name, or no name for a call that carries none. ok is
// false when the call carries more than one, or one that is not a string, or a key a command may hide
// behind: one that holds redaction's mask, since redaction masks keys too, or a command key written in
// other case, which an args condition, matching names exactly, would never read. Such a key still names a
// command, so the tool counts as a shell whose commands cannot be read, never as no shell at all.
func command(args map[string]any) (name, value string, ok bool) {
	for k, v := range args {
		exact := slices.Contains(commandKeys[:], k)
		hidden := strings.Contains(k, redact.Mask) ||
			slices.ContainsFunc(commandKeys[:], func(c string) bool { return strings.EqualFold(k, c) })
		if !exact && !hidden {
			continue
		}
		s, isString := v.(string)
		if !exact || name != "" || !isString {
			return k, "", false
		}
		name, value = k, s
	}
	return name, value, true
}

// pattern is what a shell's rule matches the command against: the command itself, exact, when every
// answered command is that same string and it holds neither a wildcard nor redaction's mask, which a
// pattern cannot take as written; otherwise the start they share (commonPrefix), which the rule follows
// with *.
func pattern(cmds []string) (p string, exact bool) {
	same := !slices.ContainsFunc(cmds, func(c string) bool { return c != cmds[0] })
	if same && !strings.ContainsAny(cmds[0], "*?") && !strings.Contains(cmds[0], redact.Mask) {
		return cmds[0], true
	}
	return commonPrefix(cmds), false
}

// commonPrefix is the start every command shares, cut so that the pattern it becomes, the start followed by
// *, matches no more than the answered commands start with: before the first * or ?, which a pattern reads
// as wildcards, and before redaction's mask, which no real call holds; then back to ASCII white space
// unless every command ends there or has white space right after it, so neither a word nor a UTF-8
// character is cut in two.
func commonPrefix(cmds []string) string {
	p := cmds[0]
	for _, c := range cmds[1:] {
		n := 0
		for n < len(p) && n < len(c) && p[n] == c[n] {
			n++
		}
		p = p[:n]
	}
	if i := strings.IndexAny(p, "*?"); i >= 0 {
		p = p[:i]
	}
	if i := strings.Index(p, redact.Mask); i >= 0 {
		p = p[:i]
	}
	if !endsAWord(p, cmds) {
		p = p[:strings.LastIndexAny(p, " \t\n")+1]
	}
	return p
}

// refused reports whether a rule must not take p, a shell's command or the start its commands share,
// because the rule could then run any command. It is refused when it holds an operator, after which the *
// would be a command of its own, as in "cd /work &&"; when it ends in $ or \, which join what the * adds to
// the last word; when a word names a launcher or could name any program (see program); and when it has
// fewer than two words after any leading NAME=value and any word of only @ , or =, which cmd.exe drops, since
// `git *`, `find *`, `make *`, `npm *`, `docker *`, `rsync *` and `tar *` each run any command through an
// option. The same checks refuse an exact command.
func refused(p string) bool {
	if strings.ContainsAny(p, operators) || strings.HasSuffix(p, "$") || strings.HasSuffix(p, `\`) {
		return true
	}
	words := strings.Fields(p)
	for _, w := range words {
		name, ok := program(w)
		if !ok || slices.Contains(launchers[:], name) {
			return true
		}
	}
	words = dropMarkers(words)
	if len(words) > 0 && cmdLauncher(words[0]) {
		return true // cmd.exe has no NAME=value, so its program is the first word as written
	}
	for len(words) > 0 && assignment.MatchString(words[0]) {
		words = words[1:]
	}
	words = dropMarkers(words)
	return (len(words) > 0 && cmdLauncher(words[0])) || len(words) < 2
}

// dropMarkers is words without the leading words made only of @ , or =, which cmd.exe drops before a
// program's name, so in `@ cmd/c dir` it runs cmd.
func dropMarkers(words []string) []string {
	for len(words) > 0 && strings.Trim(words[0], "@,=") == "" {
		words = words[1:]
	}
	return words
}

// cmdLauncher reports whether cmd.exe, which drops any @ , or = before a program's name and ends the name
// at the first / , or =, reads word as a launcher: cmd/c, cmd,/c, cmd=/c and @cmd all run cmd. Only a
// command's first word names its program, so a later word, such as the path in go vet cmd/derbent, is
// left to program. A word such as script/test or env/bin/pytest is refused too, since cmd.exe would run
// script or env for it; refusing is the safe side, and the owner can still write that rule by hand.
func cmdLauncher(word string) bool {
	w := strings.TrimLeft(strings.TrimPrefix(strings.Trim(word, `"'`), `\`), "@,=")
	i := strings.IndexAny(w, "/,=")
	if i < 0 {
		i = len(w)
	}
	if i == 0 {
		return false // nothing left, or a Unix path from the root, which program reads
	}
	name := base(w[strings.LastIndex(w[:i], `\`)+1 : i])
	return name != "." && slices.Contains(launchers[:], name)
}

// shortName is the ~ and digit of a Windows 8.3 short name, such as POWERS~1.EXE.
var shortName = regexp.MustCompile(`~[0-9]`)

// assignment is a shell's NAME=value before a command, which sets a variable for it.
var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// program is word as the program a shell would run for it: without the quotes around it or a leading \,
// which only turns off an alias, without its directory, in lower case, without a Windows extension, and
// without a version at the end, so "/usr/bin/Python3.12.exe" is python. A \ inside a word is a directory
// separator to Windows and an escape to a Unix shell, where s\h runs sh, so when name is not a launcher the
// word is read once more with every \ dropped. ok is false when the word still holds a character that can
// make it any program: $ and % for a variable, a backtick, a quote inside it, a glob's [, a brace
// expansion's { and cmd's ^ escape. PowerShell also reads the curly quotes U+2018 to U+201E as quotes, even
// inside a command name, so any of them refuses the word wherever it stands. So does a ~ before a digit,
// as in POWERS~1.EXE, since a Windows 8.3 short name can name any program.
func program(word string) (name string, ok bool) {
	w := strings.TrimPrefix(strings.Trim(word, `"'`), `\`)
	if strings.ContainsAny(w, "$%`\"'[{^\u2018\u2019\u201a\u201b\u201c\u201d\u201e") || shortName.MatchString(w) {
		return "", false
	}
	name = base(w[strings.LastIndexAny(w, `/\`)+1:])
	if !slices.Contains(launchers[:], name) {
		unescaped := strings.ReplaceAll(w, `\`, "")
		name = base(unescaped[strings.LastIndex(unescaped, "/")+1:])
	}
	return name, true
}

// base is a program's file name in lower case without the trailing dots and spaces Windows drops (so
// cmd.exe. runs cmd), without a Windows extension, and without a version at its end when that leaves a
// launcher's name: python3.12 is python, and rundll32 stays rundll32.
func base(file string) string {
	name := strings.ToLower(file)
	if n := strings.TrimRight(name, ". "); n != "" {
		name = n
	}
	for _, ext := range [...]string{".exe", ".cmd", ".bat", ".com"} {
		name = strings.TrimSuffix(name, ext)
	}
	if v := strings.TrimRight(name, "0123456789."); slices.Contains(launchers[:], v) {
		name = v
	}
	return name
}

// endsAWord reports whether every command ends right after p, or has white space there.
func endsAWord(p string, cmds []string) bool {
	for _, c := range cmds {
		if len(c) > len(p) && !strings.ContainsRune(" \t\n", rune(c[len(p)])) {
			return false
		}
	}
	return true
}

// TOML is the suggestion as [[rule]] tables to paste into the config, under a comment that says what it
// rests on, where it goes and what else it lets through. A shell's allow for a prefix comes after an ask
// for each operator, so a command that the prefix starts but that chains, pipes, redirects or substitutes
// another one is still asked about. Strings are TOML basic strings that read back as they were and cannot
// end early; the comment is escaped like any text for a terminal, so it stays on its own lines.
func (s Suggestion) TOML() string {
	var b strings.Builder
	answers := fmt.Sprintf("approved %d %s and never denied", s.Approved, times(s.Approved))
	if s.Action == rule.Deny {
		answers = fmt.Sprintf("denied %d %s and never approved", s.Denied, times(s.Denied))
	}
	fmt.Fprintf(&b, "# %s's %s: %s.\n", visible.Escape(s.Agent), visible.Escape(s.Tool), answers)
	below := "the rules below it"
	if len(s.Rules) > 0 {
		fmt.Fprintf(&b, "# Put it above rule %d in your config, so first match reaches it before %s.\n", s.Rules[0], asked(s.Rules))
		below = fmt.Sprintf("the rules at and below rule %d", s.Rules[0])
	}
	pattern := s.Prefix
	if !s.Exact {
		pattern += "*"
	}
	switch {
	case s.Action != rule.Allow || s.Exact:
	case s.Key == "":
		fmt.Fprintf(&b, "# This allows every call to %s, whatever its arguments; %s no longer see them.\n", visible.Escape(s.Tool), below)
	default:
		b.WriteString("# The allow at the end also matches any options after the prefix, such as --force.\n" +
			"# The asks above it stop chained, piped, redirected and substituted commands.\n" +
			// A covers every later call the same rule asks about, for the session (ADR 0011).
			"# Answer those asks with a (once), not A: a session grant lets later calls matching that ask through.\n")
		fmt.Fprintf(&b, "# Calls the allow matches no longer reach %s.\n", below)
		for _, op := range operators {
			s.table(&b, pattern+string(op)+"*", rule.Ask)
		}
	}
	s.table(&b, pattern, s.Action)
	return b.String()
}

// table writes one [[rule]] table for the suggestion's agent and tool, with pattern for its command key,
// if it has one, and action.
func (s Suggestion) table(b *strings.Builder, pattern string, action rule.Action) {
	b.WriteString("[[rule]]\n")
	fmt.Fprintf(b, "agent  = %s\n", quote(s.Agent))
	fmt.Fprintf(b, "tool   = %s\n", quote(s.Tool))
	if s.Key != "" {
		fmt.Fprintf(b, "args   = { %s = %s }\n", s.Key, quote(pattern))
	}
	fmt.Fprintf(b, "action = %s\n", quote(string(action)))
}

func times(n int) string {
	if n == 1 {
		return "time"
	}
	return "times"
}

// asked names the rules that asked: "rule 3, which asked" or "rules 3, 7 and 9, which asked".
func asked(rules []int) string {
	if len(rules) == 1 {
		return fmt.Sprintf("rule %d, which asked", rules[0])
	}
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = strconv.Itoa(r)
	}
	return "rules " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1] + ", which asked"
}

// quote writes s as a TOML basic string that reads back as s. Quotes and backslashes are escaped, and so is
// every rune a terminal must not see as it is (visible.Unsafe), control characters among them, so the
// string can neither end early nor hide or reorder text in the snippet.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case visible.Unsafe(r) && r > 0xFFFF:
			fmt.Fprintf(&b, `\U%08X`, r)
		case visible.Unsafe(r):
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
