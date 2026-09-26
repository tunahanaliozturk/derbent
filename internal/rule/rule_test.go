package rule_test

import (
	"errors"
	"testing"

	"github.com/tunahanaliozturk/portcullis/internal/rule"
)

func mustCompile(t *testing.T, specs ...rule.Spec) rule.Set {
	t.Helper()
	set, err := rule.Compile(specs)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func decided(action rule.Action, n int) rule.Decision {
	return rule.Decision{Action: action, Rule: n}
}

func TestDecide(t *testing.T) {
	set := mustCompile(t,
		rule.Spec{Tool: "memory_*", Action: rule.Allow},
		rule.Spec{Agent: "copilot", Tool: "github__*", Action: rule.Deny},
		rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push*"}, Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	tests := map[string]struct {
		agent, tool string
		args        map[string]any
		want        rule.Decision
	}{
		"memory tool for anyone":            {"claude", "memory_write", nil, decided(rule.Allow, 1)},
		"copilot on github is denied":       {"copilot", "github__create_issue", nil, decided(rule.Deny, 2)},
		"claude on github falls through":    {"claude", "github__create_issue", nil, decided(rule.Allow, 4)},
		"push is denied":                    {"codex", "native__Bash", map[string]any{"command": "git push origin main"}, decided(rule.Deny, 3)},
		"star spans newlines":               {"codex", "native__Bash", map[string]any{"command": "git push origin\nmain --force"}, decided(rule.Deny, 3)},
		"other commands pass":               {"codex", "native__Bash", map[string]any{"command": "go test ./..."}, decided(rule.Allow, 4)},
		"non-string argument never matches": {"codex", "native__Bash", map[string]any{"command": 42}, decided(rule.Allow, 4)},
		"missing argument never matches":    {"codex", "native__Bash", nil, decided(rule.Allow, 4)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := set.Decide(tc.agent, tc.tool, tc.args); got != tc.want {
				t.Fatalf("Decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPatternsAreLiteralExceptStarAndQuestionMark(t *testing.T) {
	set := mustCompile(t,
		rule.Spec{Tool: "a.b?", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	tests := map[string]rule.Action{
		"a.bc":  rule.Deny,
		"axbc":  rule.Allow,
		"a.bcd": rule.Allow,
		"a.b":   rule.Allow,
	}
	for tool, want := range tests {
		if got := set.Decide("x", tool, nil).Action; got != want {
			t.Errorf("Decide(%q) = %s, want %s", tool, got, want)
		}
	}
}

func TestHidden(t *testing.T) {
	set := mustCompile(t,
		rule.Spec{Tool: "memory_search", Args: map[string]string{"query": "secret*"}, Action: rule.Deny},
		rule.Spec{Agent: "copilot", Tool: "memory_*", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	tests := map[string]struct {
		agent, tool string
		want        bool
	}{
		"plain deny hides":            {"copilot", "memory_write", true},
		"args deny keeps tool listed": {"copilot", "memory_search", false},
		"allowed tool is listed":      {"claude", "memory_write", false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := set.Hidden(tc.agent, tc.tool); got != tc.want {
				t.Fatalf("Hidden = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestZeroSetDeniesAndHides(t *testing.T) {
	var set rule.Set
	if got := set.Decide("a", "t", nil); got != decided(rule.Deny, 0) {
		t.Fatalf("Decide = %+v, want deny by no rule", got)
	}
	if !set.Hidden("a", "t") {
		t.Fatal("zero set lists tools")
	}
}

func TestCompileRejects(t *testing.T) {
	tests := map[string][]rule.Spec{
		"no rules":               nil,
		"conditional last rule":  {{Tool: "x", Action: rule.Allow}},
		"unknown action":         {{Action: "maybe"}},
		"ask before milestone 3": {{Action: "ask"}},
	}
	for name, specs := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := rule.Compile(specs); !errors.Is(err, rule.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
