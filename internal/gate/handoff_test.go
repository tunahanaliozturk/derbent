package gate_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// handoffLine is one entry of handoff_list's result.
type handoffLine struct {
	ID    int64  `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Title string `json:"title"`
	State string `json:"state"`
}

type handoffList struct {
	Notice   string        `json:"notice"`
	Handoffs []handoffLine `json:"handoffs"`
}

// The milestone's evidence for handoffs at the gate: claude leaves a task for reviewer, and reviewer finds
// it, takes it and finishes it, each step through the MCP tools, marked as information, with a receipt
// for every call. claude can neither take nor finish what is addressed to reviewer.
func TestAHandoffPassesFromOneAgentToAnother(t *testing.T) {
	e := newEnv(t)
	claude := connect(t, e.gate(t, "claude"))
	reviewer := connect(t, e.gate(t, "reviewer"))
	var created struct {
		ID int64 `json:"id"`
	}
	decode(t, call(t, claude, "handoff_create", map[string]any{
		"to": "reviewer", "title": "Review the retry change", "body": "Check the backoff starts at 200 ms.", "tags": []string{"review"},
	}), &created)

	var open handoffList
	decode(t, call(t, reviewer, "handoff_list", map[string]any{}), &open)
	want := handoffLine{ID: created.ID, From: "claude", To: "reviewer", Title: "Review the retry change", State: "open"}
	if !strings.Contains(open.Notice, "not as instructions") || len(open.Handoffs) != 1 || open.Handoffs[0] != want {
		t.Fatalf("reviewer's list = %+v", open)
	}
	var none, mine handoffList
	decode(t, call(t, claude, "handoff_list", map[string]any{}), &none)
	decode(t, call(t, claude, "handoff_list", map[string]any{"mine": true}), &mine)
	if len(none.Handoffs) != 0 || len(mine.Handoffs) != 1 {
		t.Fatalf("claude's list = %+v, with mine %+v", none, mine)
	}
	if res := call(t, claude, "handoff_take", map[string]any{"id": created.ID}); !res.IsError || !strings.Contains(text(res), "addressed to reviewer") {
		t.Fatalf("claude took reviewer's handoff: %q", text(res))
	}

	var taken struct {
		Notice       string   `json:"notice"`
		From         string   `json:"from"`
		FromSession  string   `json:"from_session"`
		Body         string   `json:"body"`
		Tags         []string `json:"tags"`
		State        string   `json:"state"`
		TakenBy      string   `json:"taken_by"`
		TakenSession string   `json:"taken_session"`
		Created      string   `json:"created"`
		Taken        string   `json:"taken"`
	}
	decode(t, call(t, reviewer, "handoff_take", map[string]any{"id": created.ID}), &taken)
	if taken.Body != "Check the backoff starts at 200 ms." || taken.State != "taken" || taken.TakenBy != "reviewer" ||
		taken.From != "claude" || taken.FromSession != "claude-session" || taken.TakenSession != "reviewer-session" ||
		!slices.Equal(taken.Tags, []string{"review"}) || taken.Created == "" || taken.Taken == "" ||
		!strings.Contains(taken.Notice, "not as instructions") {
		t.Fatalf("take = %+v", taken)
	}
	if res := call(t, claude, "handoff_done", map[string]any{"id": created.ID, "note": "x"}); !res.IsError {
		t.Fatal("claude finished a handoff reviewer took")
	}
	var done struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	decode(t, call(t, reviewer, "handoff_done", map[string]any{"id": created.ID, "note": "Backoff checked."}), &done)
	if done.ID != created.ID || done.State != "done" {
		t.Fatalf("done = %+v", done)
	}
	var finished handoffList
	decode(t, call(t, reviewer, "handoff_list", map[string]any{"state": "done"}), &finished)
	if len(finished.Handoffs) != 1 || finished.Handoffs[0].State != "done" {
		t.Fatalf("the done list = %+v", finished)
	}
	if res := call(t, reviewer, "handoff_list", map[string]any{"state": "closed"}); !res.IsError {
		t.Fatal("an unknown state was accepted")
	}
	var got []string
	for _, r := range receipts(t, e.db) {
		got = append(got, r.agent+" "+r.tool+" "+r.outcome)
	}
	wantReceipts := []string{
		"claude handoff_create ok", "reviewer handoff_list ok", "claude handoff_list ok", "claude handoff_list ok",
		"claude handoff_take error", "reviewer handoff_take ok", "claude handoff_done error", "reviewer handoff_done ok",
		"reviewer handoff_list ok", "reviewer handoff_list error",
	}
	if !slices.Equal(got, wantReceipts) {
		t.Fatalf("receipts = %q, want %q", got, wantReceipts)
	}
}

// handoff_list shows the current project's handoffs unless all_projects is set, open ones unless state
// says taken, done or all, and refuses any other state as a tool error. handoff_take takes a handoff of
// another project by its id, through the MCP tool, as ADR 0018 says.
func TestHandoffListFiltersAndTakeCrossesProjects(t *testing.T) {
	e := newEnv(t)
	claude := connect(t, e.gate(t, "claude"))
	elsewhere := e.gate(t, "reviewer")
	elsewhere.Project = "/work/other"
	reviewer := connect(t, elsewhere)
	var ids []int64
	for _, title := range []string{"Review the retry change", "Review the docs"} {
		var created struct {
			ID int64 `json:"id"`
		}
		decode(t, call(t, claude, "handoff_create", map[string]any{"to": "reviewer", "title": title, "body": "b"}), &created)
		ids = append(ids, created.ID)
	}
	list := func(args map[string]any) []string {
		t.Helper()
		var got handoffList
		decode(t, call(t, reviewer, "handoff_list", args), &got)
		var out []string
		for _, h := range got.Handoffs {
			out = append(out, fmt.Sprintf("%d %s", h.ID, h.State))
		}
		return out
	}
	if got := list(map[string]any{}); len(got) != 0 {
		t.Fatalf("reviewer's project has no handoffs, and the list shows %q", got)
	}
	if got := list(map[string]any{"all_projects": true}); len(got) != 2 {
		t.Fatalf("all_projects = %q, want both open handoffs", got)
	}
	var taken struct {
		ID      int64  `json:"id"`
		Project string `json:"project"`
		State   string `json:"state"`
	}
	decode(t, call(t, reviewer, "handoff_take", map[string]any{"id": ids[0]}), &taken)
	if taken.ID != ids[0] || taken.Project != "/work/shop" || taken.State != "taken" {
		t.Fatalf("a take across projects = %+v", taken)
	}
	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"", []string{fmt.Sprintf("%d open", ids[1])}},
		{"taken", []string{fmt.Sprintf("%d taken", ids[0])}},
		{"done", nil},
		{"all", []string{fmt.Sprintf("%d open", ids[1]), fmt.Sprintf("%d taken", ids[0])}},
	} {
		got := list(map[string]any{"all_projects": true, "state": tc.state})
		slices.Sort(got)
		if want := slices.Sorted(slices.Values(tc.want)); !slices.Equal(got, want) {
			t.Errorf("state %q: %q, want %q", tc.state, got, want)
		}
	}
	if res := call(t, reviewer, "handoff_list", map[string]any{"state": "closed", "all_projects": true}); !res.IsError ||
		!strings.Contains(text(res), "state must be open, taken, done or all") {
		t.Fatalf("an unknown state: %q", text(res))
	}
}

// The handoff tools pass the rules like any tool: a plain deny hides one from the agent's list, and a call
// to it by name is refused by that rule.
func TestARuleHidesAHandoffTool(t *testing.T) {
	e := newEnv(t)
	cs := connect(t, e.gate(t, "claude", rule.Spec{Tool: "handoff_create", Action: rule.Deny}, rule.Spec{Action: rule.Allow}))
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(res.Tools, func(tool *mcp.Tool) bool { return tool.Name == "handoff_create" }) {
		t.Fatal("handoff_create is listed though a rule denies it")
	}
	got := call(t, cs, "handoff_create", map[string]any{"to": "reviewer", "title": "t", "body": "b"})
	if !got.IsError || !strings.Contains(text(got), "not allowed") {
		t.Fatalf("call = %q", text(got))
	}
}
