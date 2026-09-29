// Package suggest reads the calls the user approved or denied and finds the rules they point to (ADR
// 0019): an allow for what the user keeps approving, a deny for what they keep denying. It only suggests;
// Derbent never writes a rule into the config.
package suggest

import (
	"cmp"
	"encoding/json"
	"fmt"
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
// a shell, and a rule for it is suggested only with the start its commands share.
var commandKeys = [...]string{"command", "CommandLine"}

// Suggestion is a rule the user's answers point to.
type Suggestion struct {
	Agent, Tool string
	Action      rule.Action // allow for approvals, deny for denials
	Key         string      // the command argument, command or CommandLine, for a shell tool; "" for any other
	Prefix      string      // what every answered command starts with, for a shell tool
	Approved    int
	Denied      int
	Rules       []int // the rules in the user's config that asked, lowest first
}

// From groups the answered calls by agent and tool and returns a suggestion for each group with at least
// least approvals and no denial, or at least least denials and no approval, sorted by agent and tool.
// Calls a project's rules asked about are left out: a rule in the user's config cannot loosen a project's
// rules. A group gets no suggestion when a rule could not name it (an agent that is no agent label, an
// empty tool name or one with a wildcard), or when it is a shell whose commands cannot all be read, or
// share no start that a rule can take safely (see commonPrefix).
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
	case s.Approved >= least && s.Denied == 0:
		s.Action = rule.Allow
	case s.Denied >= least && s.Approved == 0:
		s.Action = rule.Deny
	default:
		return Suggestion{}, false
	}
	key, cmds, shell, readable := shellCommands(calls)
	switch {
	case !shell:
		return s, true
	case !readable:
		return Suggestion{}, false
	}
	s.Key, s.Prefix = key, commonPrefix(cmds)
	return s, s.Prefix != ""
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

// commonPrefix is the start every command shares, cut so that the pattern it becomes, the start followed by
// *, matches no more than the answered commands start with: before the first * or ?, which a pattern reads
// as wildcards, and before redaction's mask, which no real call holds; then back to ASCII white space
// unless every command ends there or has white space right after it, so neither a word nor a UTF-8
// character is cut in two. It is "" when nothing but white space is left, or when what is left holds a
// shell operator or a line break, after which the * would be a command of its own: a whole shell is never
// suggested, not even behind "cd /work &&".
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
	if strings.TrimSpace(p) == "" || strings.ContainsAny(p, ";&|`\n\r") || strings.Contains(p, "$(") {
		return ""
	}
	return p
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

// TOML is the suggestion as a [[rule]] table to paste into the config, under a comment that says what it
// rests on and where it goes. Strings are TOML basic strings that read back as they were and cannot end
// early; the comment is escaped like any text for a terminal, so it stays on its own lines.
func (s Suggestion) TOML() string {
	var b strings.Builder
	answers := fmt.Sprintf("approved %d %s and never denied", s.Approved, times(s.Approved))
	if s.Action == rule.Deny {
		answers = fmt.Sprintf("denied %d %s and never approved", s.Denied, times(s.Denied))
	}
	fmt.Fprintf(&b, "# %s's %s: %s.\n", visible.Escape(s.Agent), visible.Escape(s.Tool), answers)
	if len(s.Rules) > 0 {
		fmt.Fprintf(&b, "# Put it above rule %d in your config, so first match reaches it before %s.\n", s.Rules[0], asked(s.Rules))
	}
	b.WriteString("[[rule]]\n")
	fmt.Fprintf(&b, "agent  = %s\n", quote(s.Agent))
	fmt.Fprintf(&b, "tool   = %s\n", quote(s.Tool))
	if s.Key != "" {
		fmt.Fprintf(&b, "args   = { %s = %s }\n", s.Key, quote(s.Prefix+"*"))
	}
	fmt.Fprintf(&b, "action = %s\n", quote(string(s.Action)))
	return b.String()
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
