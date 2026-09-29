package rule

import (
	"fmt"
	"slices"
	"time"
)

// BudgetSpec is a budget as written in the config file (ADR 0012).
type BudgetSpec struct {
	Agent string `toml:"agent"`
	Tool  string `toml:"tool"`
	Calls int    `toml:"calls"`
	Per   string `toml:"per"`
}

// The shortest and longest windows a budget may have.
const (
	minBudgetWindow = time.Minute
	maxBudgetWindow = 24 * time.Hour
)

// Budgets is a compiled list of budgets. The zero value has none.
type Budgets struct {
	list []budget
}

type budget struct {
	spec        BudgetSpec
	agent, tool glob
	per         time.Duration
}

// Passed is a call an agent had let through: its tool and when.
type Passed struct {
	Tool string
	At   time.Time
}

// Reached is a budget that is used up.
type Reached struct {
	N    int // the budget's 1-based position in the config
	Spec BudgetSpec
	Wait time.Duration // until enough calls leave the window for one more
}

// CompileBudgets checks specs and compiles their patterns. agent and tool are globs with the rule
// syntax, calls is at least 1, and per is a Go duration from one minute to 24 hours.
func CompileBudgets(specs []BudgetSpec) (Budgets, error) {
	out := Budgets{list: make([]budget, 0, len(specs))}
	for i, sp := range specs {
		if !agentPattern.MatchString(sp.Agent) {
			return Budgets{}, fmt.Errorf("%w: budget %d: agent %q can never match: agent names are lower-case letters, digits, dashes and underscores", ErrInvalid, i+1, sp.Agent)
		}
		if sp.Calls < 1 {
			return Budgets{}, fmt.Errorf("%w: budget %d: calls must be at least 1, got %d", ErrInvalid, i+1, sp.Calls)
		}
		per, err := time.ParseDuration(sp.Per)
		if err != nil {
			return Budgets{}, fmt.Errorf("%w: budget %d: per %q is not a duration such as \"1h\"", ErrInvalid, i+1, sp.Per)
		}
		if per < minBudgetWindow || per > maxBudgetWindow {
			return Budgets{}, fmt.Errorf("%w: budget %d: per %q is not between one minute and 24 hours", ErrInvalid, i+1, sp.Per)
		}
		out.list = append(out.list, budget{spec: sp, agent: newGlob(sp.Agent), tool: newGlob(sp.Tool), per: per})
	}
	return out, nil
}

// Len is the number of budgets.
func (b Budgets) Len() int {
	return len(b.list)
}

// Window is the longest window among the budgets that apply to agent calling tool, or 0 when none
// does, so a caller knows how far back to read and whether to read at all.
func (b Budgets) Window(agent, tool string) time.Duration {
	var longest time.Duration
	for _, bu := range b.list {
		if bu.agent.match(agent) && bu.tool.match(tool) {
			longest = max(longest, bu.per)
		}
	}
	return longest
}

// Use is how one budget that applies to a call stands: how many of the calls the agent had let through
// count against it within its window, and, when it is used up, how long until one more call fits.
type Use struct {
	N     int // the budget's 1-based position in the config
	Spec  BudgetSpec
	Count int
	Full  bool
	Wait  time.Duration // until enough calls leave the window for one more; 0 while there is room
}

// Uses returns every budget that applies to agent calling tool, in config order, with the calls of passed
// that count against it before now. Every budget that applies is counted, unlike rules, where the first
// match decides. derbent explain prints them, and Reached picks from them.
func (b Budgets) Uses(agent, tool string, passed []Passed, now time.Time) []Use {
	var out []Use
	for i, bu := range b.list {
		if !bu.agent.match(agent) || !bu.tool.match(tool) {
			continue
		}
		var inside []time.Time
		for _, p := range passed {
			if bu.tool.match(p.Tool) && p.At.After(now.Add(-bu.per)) {
				inside = append(inside, p.At)
			}
		}
		u := Use{N: i + 1, Spec: bu.spec, Count: len(inside)}
		if len(inside) >= bu.spec.Calls {
			// One more call fits once all but Calls-1 of them have left the window.
			slices.SortFunc(inside, time.Time.Compare)
			u.Full, u.Wait = true, inside[len(inside)-bu.spec.Calls].Add(bu.per).Sub(now)
		}
		out = append(out, u)
	}
	return out
}

// Reached returns the budget that applies to agent calling tool, is used up and refuses the call for
// longest, the first in config order on a tie: the call is refused until every used-up budget has room
// again.
func (b Budgets) Reached(agent, tool string, passed []Passed, now time.Time) (Reached, bool) {
	var longest Reached
	found := false
	for _, u := range b.Uses(agent, tool, passed, now) {
		if u.Full && (!found || u.Wait > longest.Wait) {
			longest, found = Reached{N: u.N, Spec: u.Spec, Wait: u.Wait}, true
		}
	}
	return longest, found
}

// Message is what the agent reads when r refuses its call, after "derbent: ".
func (r Reached) Message(agent string) string {
	tool, calls := r.Spec.Tool, "calls"
	if tool == "" {
		tool = "*"
	}
	if r.Spec.Calls == 1 {
		calls = "call"
	}
	wait := max(r.Wait, time.Second).Round(time.Second)
	return fmt.Sprintf("budget %d in the config is used up: %d %s to %s per %s for %s; the next call is possible in about %s",
		r.N, r.Spec.Calls, calls, tool, r.Spec.Per, agent, wait)
}
