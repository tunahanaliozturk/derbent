package rule_test

import (
	"errors"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/rule"
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
		"memory tool for anyone":             {"claude", "memory_write", nil, decided(rule.Allow, 1)},
		"copilot on github is denied":        {"copilot", "github__create_issue", nil, decided(rule.Deny, 2)},
		"claude on github falls through":     {"claude", "github__create_issue", nil, decided(rule.Allow, 4)},
		"push is denied":                     {"codex", "native__Bash", map[string]any{"command": "git push origin main"}, decided(rule.Deny, 3)},
		"star spans newlines":                {"codex", "native__Bash", map[string]any{"command": "git push origin\nmain --force"}, decided(rule.Deny, 3)},
		"other commands pass":                {"codex", "native__Bash", map[string]any{"command": "go test ./..."}, decided(rule.Allow, 4)},
		"non-string argument matches a deny": {"codex", "native__Bash", map[string]any{"command": []any{"git", "push"}}, decided(rule.Deny, 3)},
		"null argument matches a deny":       {"codex", "native__Bash", map[string]any{"command": nil}, decided(rule.Deny, 3)},
		"missing argument never matches":     {"codex", "native__Bash", nil, decided(rule.Allow, 4)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := set.Decide(tc.agent, tc.tool, tc.args); got != tc.want {
				t.Fatalf("Decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNonStringArgumentNeverMatchesAnAllow(t *testing.T) {
	set := mustCompile(t,
		rule.Spec{Tool: "memory_search", Args: map[string]string{"query": "public*"}, Action: rule.Allow},
		rule.Spec{Tool: "memory_search", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	if got := set.Decide("claude", "memory_search", map[string]any{"query": 42}); got != decided(rule.Deny, 2) {
		t.Fatalf("Decide = %+v, want the allow skipped and the deny to decide", got)
	}
}

func TestLen(t *testing.T) {
	if n := mustCompile(t, rule.Spec{Tool: "x", Action: rule.Deny}, rule.Spec{Action: rule.Allow}).Len(); n != 2 {
		t.Fatalf("Len = %d, want 2", n)
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
		"plain deny hides":                          {"copilot", "memory_write", true},
		"args deny before a plain deny still hides": {"copilot", "memory_search", true},
		"args deny before an allow keeps it listed": {"claude", "memory_search", false},
		"allowed tool is listed":                    {"claude", "memory_write", false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := set.Hidden(tc.agent, tc.tool); got != tc.want {
				t.Fatalf("Hidden = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllowWithArgsKeepsToolListed(t *testing.T) {
	set := mustCompile(t,
		rule.Spec{Tool: "memory_search", Args: map[string]string{"query": "public*"}, Action: rule.Allow},
		rule.Spec{Tool: "memory_search", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	if set.Hidden("claude", "memory_search") {
		t.Fatal("a tool that some calls may use is hidden")
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
		"no rules":              nil,
		"conditional last rule": {{Tool: "x", Action: rule.Allow}},
		"unknown action":        {{Action: "maybe"}},
		"agent with upper case": {{Agent: "Copilot", Action: rule.Deny}, {Action: rule.Allow}},
		"agent with a space":    {{Agent: "claude code", Action: rule.Deny}, {Action: rule.Allow}},
	}
	for name, specs := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := rule.Compile(specs); !errors.Is(err, rule.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestAsk(t *testing.T) {
	set := mustCompile(t,
		rule.Spec{Tool: "github__create_*", Action: rule.Ask},
		rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push*"}, Action: rule.Ask},
		rule.Spec{Tool: "github__*", Action: rule.Deny},
		rule.Spec{Action: rule.Allow},
	)
	tests := map[string]struct {
		tool string
		args map[string]any
		want rule.Decision
	}{
		"asked tool":                        {"github__create_issue", nil, decided(rule.Ask, 1)},
		"asked command":                     {"native__Bash", map[string]any{"command": "git push origin main"}, decided(rule.Ask, 2)},
		"unreadable value goes to a person": {"native__Bash", map[string]any{"command": []any{"git", "push"}}, decided(rule.Ask, 2)},
		"other command passes":              {"native__Bash", map[string]any{"command": "go test ./..."}, decided(rule.Allow, 4)},
		"other github tool is denied":       {"github__delete_repo", nil, decided(rule.Deny, 3)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := set.Decide("codex", tc.tool, tc.args); got != tc.want {
				t.Fatalf("Decide = %+v, want %+v", got, tc.want)
			}
		})
	}
	if set.Hidden("codex", "github__create_issue") {
		t.Fatal("a tool behind ask is hidden; the user could never approve it")
	}
	if !set.Hidden("codex", "github__delete_repo") {
		t.Fatal("a tool only a plain deny matches is listed")
	}
}

// A session grant is keyed on the rule that asked. Under first match, which calls rule n catches
// depends on rules 1 to n, so its key covers all of them: a change to rule n or to any rule above it
// gives a new key, and a change below it keeps the key.
func TestKeyCoversTheRuleAndTheRulesAboveIt(t *testing.T) {
	force := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push*--force*"}, Action: rule.Ask}
	push := rule.Spec{Agent: "codex", Tool: "native__Bash", Args: map[string]string{"command": "git push*", "cwd": "/work/*"}, Action: rule.Ask}
	rm := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "rm -rf*"}, Action: rule.Deny}
	memory := rule.Spec{Tool: "memory_*", Action: rule.Allow}
	last := rule.Spec{Action: rule.Allow}
	key := func(n int, specs ...rule.Spec) string {
		t.Helper()
		return mustCompile(t, specs...).Key(n)
	}
	want := key(2, force, push, rm, last)
	if want == "" || key(2, force, push, rm, last) != want {
		t.Fatal("the same config gave rule 2 no key, or a different key each time")
	}
	for name, n := range map[string]int{"no rule": 0, "past the end": 5, "negative": -1} {
		if got := key(n, force, push, rm, last); got != "" {
			t.Errorf("%s: Key(%d) = %q, want empty", name, n, got)
		}
	}

	// Go reads a map's keys in another order on each pass, and with eight args a pass can start at any
	// of them, so compiling the same rule many times reads its args in many orders. The key must not
	// follow that order.
	many := push
	many.Args = map[string]string{
		"branch": "main", "command": "git push*", "cwd": "/work/*", "env": "*",
		"host": "h?", "remote": "origin", "shell": "bash", "user": "*",
	}
	manyKey := key(2, force, many, rm, last)
	for i := range 64 {
		if key(2, force, many, rm, last) != manyKey {
			t.Fatalf("compile %d read rule 2's args in another order and gave it another key", i+2)
		}
	}

	for name, got := range map[string]string{
		"a rule below edited":   key(2, force, push, memory, last),
		"a rule below removed":  key(2, force, push, last),
		"a rule below added":    key(2, force, push, rm, memory, last),
		"rules below reordered": key(2, force, push, memory, rm, last),
	} {
		if got != want {
			t.Errorf("%s changed rule 2's key", name)
		}
	}

	with := func(change func(sp *rule.Spec)) rule.Spec {
		sp := push
		sp.Args = map[string]string{"command": "git push*", "cwd": "/work/*"}
		change(&sp)
		return sp
	}
	narrower := rule.Spec{Tool: "native__Bash", Args: map[string]string{"command": "git push --force*"}, Action: rule.Ask}
	for name, got := range map[string]string{
		"agent":                  key(2, force, with(func(sp *rule.Spec) { sp.Agent = "claude" }), rm, last),
		"tool":                   key(2, force, with(func(sp *rule.Spec) { sp.Tool = "native__Shell" }), rm, last),
		"args pattern":           key(2, force, with(func(sp *rule.Spec) { sp.Args["command"] = "terraform apply*" }), rm, last),
		"args name":              key(2, force, with(func(sp *rule.Spec) { delete(sp.Args, "cwd"); sp.Args["dir"] = "/work/*" }), rm, last),
		"fewer args":             key(2, force, with(func(sp *rule.Spec) { delete(sp.Args, "cwd") }), rm, last),
		"action":                 key(2, force, with(func(sp *rule.Spec) { sp.Action = rule.Deny }), rm, last),
		"the rule above edited":  key(2, narrower, push, rm, last),
		"the rule above removed": key(1, push, rm, last),
		"the two rules swapped":  key(1, push, force, rm, last),
		"a rule added above":     key(3, memory, force, push, rm, last),
	} {
		if got == want {
			t.Errorf("%s kept rule 2's key", name)
		}
	}

	// Each rule's form is written with its length, so two different lists never run together into the
	// same bytes: without it, the args of the first list's rule 2 read as the args of the second's rule 1.
	first := key(2, rule.Spec{Agent: "a", Tool: "t", Action: rule.Ask},
		rule.Spec{Agent: "p", Tool: "q", Args: map[string]string{"n": "deny"}, Action: rule.Allow}, last)
	second := key(2, rule.Spec{Agent: "a", Tool: "t", Args: map[string]string{"p": "q"}, Action: rule.Ask},
		rule.Spec{Agent: "allow", Tool: "n", Action: rule.Deny}, last)
	if first == second {
		t.Fatal("two different rule lists share a key")
	}
}
