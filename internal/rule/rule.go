// Package rule decides whether an agent may call a tool. Rules are tried in order and the first match
// wins, so the order in the config file is the policy.
package rule

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Action is what a rule does with a call it matches.
type Action string

// Actions a rule can take. Milestone 3 adds ask.
const (
	Allow Action = "allow"
	Deny  Action = "deny"
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
}

// ErrInvalid marks a rule list that cannot be compiled.
var ErrInvalid = errors.New("invalid rules")

// Set is a compiled, ordered list of rules. The zero Set denies every call and hides every tool.
type Set struct {
	rules []compiled
}

type compiled struct {
	agent, tool glob
	args        map[string]glob
	action      Action
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
	for i, sp := range specs {
		switch sp.Action {
		case Allow, Deny:
		default:
			return Set{}, fmt.Errorf("%w: rule %d: action %q is not allow or deny", ErrInvalid, i+1, sp.Action)
		}
		c := compiled{agent: newGlob(sp.Agent), tool: newGlob(sp.Tool), action: sp.Action}
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

// Decide returns the decision of the first rule matching the call. args holds the call's arguments
// decoded as a JSON object, or nil when there are none.
func (s Set) Decide(agent, tool string, args map[string]any) Decision {
	for i, r := range s.rules {
		if r.agent.match(agent) && r.tool.match(tool) && r.argsMatch(args) {
			return Decision{Action: r.action, Rule: i + 1}
		}
	}
	return Decision{Action: Deny}
}

// Hidden reports whether tool should be left out of agent's tool list: the first rule matching agent
// and tool, whatever its args condition, is a deny without an args condition. A rule with an args
// condition keeps the tool listed, because calls with other arguments may still be allowed.
func (s Set) Hidden(agent, tool string) bool {
	for _, r := range s.rules {
		if r.agent.match(agent) && r.tool.match(tool) {
			return r.action == Deny && len(r.args) == 0
		}
	}
	return true
}

func (c compiled) argsMatch(args map[string]any) bool {
	for name, g := range c.args {
		v, ok := args[name].(string)
		if !ok || !g.match(v) {
			return false
		}
	}
	return true
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
