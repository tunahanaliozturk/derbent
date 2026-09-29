package rule_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/rule"
)

var now = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func passed(tool string, ago time.Duration) rule.Passed {
	return rule.Passed{Tool: tool, At: now.Add(-ago)}
}

func mustBudgets(t *testing.T, specs ...rule.BudgetSpec) rule.Budgets {
	t.Helper()
	b, err := rule.CompileBudgets(specs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBudgetReached(t *testing.T) {
	b := mustBudgets(t,
		rule.BudgetSpec{Agent: "*", Tool: "native__Bash", Calls: 2, Per: "1h"},
		rule.BudgetSpec{Agent: "codex", Tool: "github__*", Calls: 1, Per: "10m"},
		rule.BudgetSpec{Agent: "claude", Tool: "native__*", Calls: 3, Per: "1h"},
	)
	bash, read := "native__Bash", "native__Read"
	tests := map[string]struct {
		agent, tool string
		passed      []rule.Passed
		want        int // the budget reached, 0 for none
		wait        time.Duration
	}{
		"under the limit":                  {"claude", bash, []rule.Passed{passed(bash, time.Minute)}, 0, 0},
		"at the limit":                     {"claude", bash, []rule.Passed{passed(bash, 50*time.Minute), passed(bash, time.Minute)}, 1, 10 * time.Minute},
		"a call outside the window":        {"claude", bash, []rule.Passed{passed(bash, 61*time.Minute), passed(bash, time.Minute)}, 0, 0},
		"a call exactly a window ago":      {"claude", bash, []rule.Passed{passed(bash, time.Hour), passed(bash, time.Minute)}, 0, 0},
		"calls to other tools":             {"claude", bash, []rule.Passed{passed("memory_write", time.Minute), passed("memory_write", time.Minute)}, 0, 0},
		"a tool no budget covers":          {"codex", "memory_write", []rule.Passed{passed("memory_write", time.Minute)}, 0, 0},
		"a glob counts every tool it hits": {"codex", "github__create_issue", []rule.Passed{passed("github__get_me", 5*time.Minute)}, 2, 5 * time.Minute},
		"every matching budget applies":    {"claude", bash, []rule.Passed{passed(bash, time.Minute), passed(read, 2*time.Minute), passed(read, 3*time.Minute)}, 3, 57 * time.Minute},
		"two used up, the longer wait": {"claude", bash, []rule.Passed{
			passed(bash, 50*time.Minute), passed(read, 5*time.Minute), passed(read, 3*time.Minute), passed(bash, time.Minute),
		}, 3, 55 * time.Minute},
		"over the limit, the wait frees one call": {"claude", bash, []rule.Passed{
			passed(bash, 40*time.Minute), passed(bash, 30*time.Minute), passed(bash, 20*time.Minute),
		}, 1, 30 * time.Minute},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r, reached := b.Reached(tc.agent, tc.tool, tc.passed, now)
			if tc.want == 0 {
				if reached {
					t.Fatalf("reached budget %d, want none", r.N)
				}
				return
			}
			if !reached || r.N != tc.want || r.Wait != tc.wait {
				t.Fatalf("Reached = %+v, %v; want budget %d with wait %s", r, reached, tc.want, tc.wait)
			}
		})
	}
}

func TestBudgetWindowIsTheLongestThatApplies(t *testing.T) {
	b := mustBudgets(t,
		rule.BudgetSpec{Tool: "native__Bash", Calls: 5, Per: "10m"},
		rule.BudgetSpec{Agent: "claude", Tool: "native__*", Calls: 50, Per: "2h"},
	)
	for _, tc := range []struct {
		agent, tool string
		want        time.Duration
	}{{"claude", "native__Bash", 2 * time.Hour}, {"codex", "native__Bash", 10 * time.Minute}, {"codex", "memory_write", 0}} {
		if got := b.Window(tc.agent, tc.tool); got != tc.want {
			t.Errorf("Window(%s, %s) = %s, want %s", tc.agent, tc.tool, got, tc.want)
		}
	}
	if got := (rule.Budgets{}).Window("claude", "native__Bash"); got != 0 {
		t.Fatalf("no budgets: Window = %s, want 0", got)
	}
}

func TestBudgetMessage(t *testing.T) {
	r := rule.Reached{N: 1, Spec: rule.BudgetSpec{Tool: "native__Bash", Calls: 200, Per: "1h"}, Wait: 12*time.Minute + 300*time.Millisecond}
	want := "budget 1 in the config is used up: 200 calls to native__Bash per 1h for claude; the next call is possible in about 12m0s"
	if got := r.Message("claude"); got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	one := rule.Reached{N: 2, Spec: rule.BudgetSpec{Calls: 1, Per: "1m"}, Wait: 10 * time.Millisecond}
	want = "budget 2 in the config is used up: 1 call to * per 1m for codex; the next call is possible in about 1s"
	if got := one.Message("codex"); got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
}

func TestCompileBudgetsRejects(t *testing.T) {
	for name, spec := range map[string]rule.BudgetSpec{
		"no calls":              {Calls: 0, Per: "1h"},
		"no window":             {Calls: 1},
		"a window under 1m":     {Calls: 1, Per: "30s"},
		"a window over 24h":     {Calls: 1, Per: "25h"},
		"not a duration":        {Calls: 1, Per: "soon"},
		"agent with upper case": {Agent: "Claude", Calls: 1, Per: "1h"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := rule.CompileBudgets([]rule.BudgetSpec{spec}); !errors.Is(err, rule.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if b, err := rule.CompileBudgets(nil); err != nil || b.Len() != 0 {
		t.Fatalf("no budgets: %d, %v", b.Len(), err)
	}
}

// Uses reports every budget that applies to a call, in config order, with the calls in its window, and
// for one that is used up the wait until the next call fits. Reached picks the longest wait from it.
func TestBudgetUsesCountEveryBudgetThatApplies(t *testing.T) {
	bash := "native__Bash"
	specs := []rule.BudgetSpec{
		{Agent: "*", Tool: bash, Calls: 2, Per: "1h"},
		{Agent: "codex", Tool: "native__*", Calls: 5, Per: "1h"},
		{Agent: "claude", Tool: "native__*", Calls: 3, Per: "10m"},
	}
	b := mustBudgets(t, specs...)
	calls := []rule.Passed{passed(bash, 50*time.Minute), passed(bash, time.Minute), passed("native__Read", 2*time.Minute)}
	want := []rule.Use{
		{N: 1, Spec: specs[0], Count: 2, Full: true, Wait: 10 * time.Minute},
		{N: 3, Spec: specs[2], Count: 2},
	}
	if got := b.Uses("claude", bash, calls, now); !slices.Equal(got, want) {
		t.Fatalf("Uses = %+v, want %+v", got, want)
	}
	if r, reached := b.Reached("claude", bash, calls, now); !reached || r.N != 1 || r.Wait != 10*time.Minute {
		t.Fatalf("Reached = %+v, %v", r, reached)
	}
	if got := b.Uses("codex", "memory_write", calls, now); len(got) != 0 {
		t.Fatalf("Uses for a tool no budget covers = %+v", got)
	}
}
