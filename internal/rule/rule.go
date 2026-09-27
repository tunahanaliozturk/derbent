// Package rule decides whether an agent may call a tool. Rules are tried in order and the first match
// wins, so the order in the config file is the policy.
package rule

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Action is what a rule does with a call it matches.
type Action string

// Actions a rule can take.
const (
	Allow Action = "allow"
	Deny  Action = "deny"
	// Ask holds the call until the user approves or denies it.
	Ask Action = "ask"
)

// Spec is a rule as written in the config file. An empty pattern matches anything.
type Spec struct {
	Agent  string            `toml:"agent"`
	Tool   string            `toml:"tool"`
	Args   map[string]string `toml:"args"`
	Action Action            `toml:"action"`
}

// Decision is the outcome for one call.
type Decision struct {
	Action Action
	// Rule is the 1-based position of the matching rule in the config, or 0 when no rule matched.
	Rule int
	// Unread reports that the rule matched because an args condition could not read the value it names
	// (a number, an array, an object, null). Nobody has seen what such a call does, so a session grant
	// must never cover it (ADR 0011).
	Unread bool
}

// ErrInvalid marks rules or budgets that cannot be compiled.
var ErrInvalid = errors.New("invalid rules")

// agentPattern admits the characters an agent name can have, plus the wildcards, so that a rule
// written for "Copilot" is refused instead of silently never matching the agent "copilot".
var agentPattern = regexp.MustCompile(`^[a-z0-9_*?-]*$`)

// Set is a compiled, ordered list of rules. The zero Set denies every call and hides every tool.
type Set struct {
	rules []compiled
}

type compiled struct {
	agent, tool glob
	args        map[string]glob
	action      Action
	key         string
}

// Compile checks specs and compiles their patterns. The last rule must be unconditional, so every
// call gets its decision from the config rather than from a default hidden in the code.
func Compile(specs []Spec) (Set, error) {
	if len(specs) == 0 {
		return Set{}, fmt.Errorf("%w: at least one rule is required", ErrInvalid)
	}
	if last := specs[len(specs)-1]; last.Agent != "" || last.Tool != "" || len(last.Args) > 0 {
		return Set{}, fmt.Errorf("%w: the last rule must have no agent, tool or args condition", ErrInvalid)
	}
	set := Set{rules: make([]compiled, 0, len(specs))}
	prefix := sha256.New() // the forms of the rules so far, each written as its length, a colon and its bytes
	for i, sp := range specs {
		switch sp.Action {
		case Allow, Deny, Ask:
		default:
			return Set{}, fmt.Errorf("%w: rule %d: action %q is not allow, deny or ask", ErrInvalid, i+1, sp.Action)
		}
		if !agentPattern.MatchString(sp.Agent) {
			return Set{}, fmt.Errorf("%w: rule %d: agent %q can never match: agent names are lower-case letters, digits, dashes and underscores", ErrInvalid, i+1, sp.Agent)
		}
		f := form(sp)
		fmt.Fprintf(prefix, "%d:%s", len(f), f)
		c := compiled{agent: newGlob(sp.Agent), tool: newGlob(sp.Tool), action: sp.Action, key: hex.EncodeToString(prefix.Sum(nil))}
		if len(sp.Args) > 0 {
			c.args = make(map[string]glob, len(sp.Args))
			for name, pattern := range sp.Args {
				c.args[name] = newGlob(pattern)
			}
		}
		set.rules = append(set.rules, c)
	}
	return set, nil
}

// Len is the number of rules in the set.
func (s Set) Len() int {
	return len(s.rules)
}

// Key returns a fingerprint of rule n (1-based, as Decision.Rule), or "" when there is no such rule.
// Under first match, which calls rule n catches depends on rules 1 to n, so the key is the SHA-256, in
// hex, of their forms in order, each written as its length, a colon and its bytes. A session grant keyed
// on it covers only what the same rule asks about under the same rules above it: a change to rule n or
// to any rule above it gives a new key, and a change below it cannot (ADR 0011).
func (s Set) Key(n int) string {
	if n < 1 || n > len(s.rules) {
		return ""
	}
	return s.rules[n-1].key
}

// form is a canonical form of sp: its agent, tool and action, then its args conditions sorted by name,
// each string written as its length, a colon and its bytes so that no two different rules share a form.
func form(sp Spec) string {
	var b strings.Builder
	field := func(s string) { fmt.Fprintf(&b, "%d:%s", len(s), s) }
	field(sp.Agent)
	field(sp.Tool)
	field(string(sp.Action))
	for _, name := range slices.Sorted(maps.Keys(sp.Args)) {
		field(name)
		field(sp.Args[name])
	}
	return b.String()
}

// Decide returns the decision of the first rule matching the call. args holds the call's arguments
// decoded as a JSON object, or nil when there are none.
func (s Set) Decide(agent, tool string, args map[string]any) Decision {
	for i, r := range s.rules {
		if !r.agent.match(agent) || !r.tool.match(tool) {
			continue
		}
		if match, unread := r.argsMatch(args); match {
			return Decision{Action: r.action, Rule: i + 1, Unread: unread}
		}
	}
	return Decision{Action: Deny}
}

// Hidden reports whether tool should be left out of agent's tool list, which is when no call to it
// can be allowed. Rules matching agent and tool are read in order: a deny with an args condition can
// only refuse some calls, so it is passed over; the first allow or ask, with or without args, means
// some calls can get through and the tool is listed; a plain deny means none do.
func (s Set) Hidden(agent, tool string) bool {
	for _, r := range s.rules {
		if !r.agent.match(agent) || !r.tool.match(tool) {
			continue
		}
		switch r.action {
		case Allow, Ask:
			return false
		case Deny:
			if len(r.args) == 0 {
				return true
			}
		}
	}
	return true
}

// argsMatch checks every args condition of the rule against the call. A condition matches a string
// value through its pattern. A value the pattern cannot read (a number, an array, an object, null)
// matches a deny or an ask and never an allow, so an args condition never lets through what it cannot
// check: the call is refused or a person looks at it. A missing argument matches neither. unread
// reports that the rule matched through such a value.
func (c compiled) argsMatch(args map[string]any) (match, unread bool) {
	for name, g := range c.args {
		v, present := args[name]
		if !present {
			return false, false
		}
		s, isString := v.(string)
		if !isString {
			if c.action == Allow {
				return false, false
			}
			unread = true
			continue
		}
		if !g.match(s) {
			return false, false
		}
	}
	return true, unread
}

// glob matches * against any run of characters, newlines included, and ? against exactly one.
// Every other character is literal. An empty pattern matches anything.
type glob struct {
	re *regexp.Regexp
}

func newGlob(pattern string) glob {
	if pattern == "" {
		return glob{}
	}
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`\z`)
	return glob{re: regexp.MustCompile(b.String())}
}

func (g glob) match(s string) bool {
	return g.re == nil || g.re.MatchString(s)
}
