package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type deps struct {
	q   *approval.Queue
	log *receipt.Log
	mem *memory.Store
}

func newModel(t *testing.T) (Model, deps) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d := deps{q: approval.NewQueue(db), log: receipt.NewLog(db), mem: memory.NewStore(db)}
	m := New(t.Context(), d.q, d.log, d.mem)
	m.poll = time.Millisecond // the next-poll command runs inside messages; keep it short
	m, _ = update(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return m, d
}

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// refresh is one poll of the running program: load a snapshot and apply it.
func refresh(m Model) (Model, tea.Cmd) {
	return update(m, m.load())
}

// messages runs cmd and returns what it produced, opening batches. Nothing is fed back into a model.
func messages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, messages(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func rang(cmd tea.Cmd) bool {
	for _, msg := range messages(cmd) {
		if raw, ok := msg.(tea.RawMsg); ok && raw.Msg == bell {
			return true
		}
	}
	return false
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r, _ := utf8.DecodeRuneInString(s)
	return tea.KeyPressMsg{Code: r, Text: s}
}

// press sends keys in order and returns the command of the last one.
func press(m Model, keys ...string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = update(m, keyMsg(k))
	}
	return m, cmd
}

// settle feeds the messages of cmd back into m, as the program would, one round deep.
func settle(m Model, cmd tea.Cmd) Model {
	for _, msg := range messages(cmd) {
		m, _ = update(m, msg)
	}
	return m
}

func screen(m Model) string {
	return m.View().Content
}

var askReq = approval.Request{
	Project: "/work/shop", Agent: "codex", Session: "s1", Tool: "github__create_issue", Args: `{"title":"Fix login"}`, Rule: 2,
}

// waiting runs q.Ask in the background until the test ends; the call's outcome arrives on the channel.
func waiting(t *testing.T, q *approval.Queue, r approval.Request) <-chan approval.Outcome {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan approval.Outcome, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		out, _ := q.Ask(ctx, r, 10*time.Second)
		done <- out
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return done
}

func waitPending(t *testing.T, q *approval.Queue, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		p, err := q.Pending(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(p) >= n {
			return
		}
	}
	t.Fatalf("fewer than %d approvals became pending", n)
}

func appendReceipt(t *testing.T, log *receipt.Log, agent, tool, decision string) {
	t.Helper()
	if _, err := log.Append(t.Context(), receipt.Receipt{
		Project: "/work/shop", Agent: agent, Session: "s", Tool: tool,
		Args: "{}", Decision: decision, DecidedBy: "rule:1", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingCallIsShownAndRingsTheBellOnce(t *testing.T) {
	m, d := newModel(t)
	waiting(t, d.q, askReq)
	waitPending(t, d.q, 1)
	m, cmd := refresh(m)
	if !rang(cmd) {
		t.Fatal("no bell for a new approval")
	}
	for _, want := range []string{"1 waiting", "#1", "codex", "github__create_issue", `{"title":"Fix login"}`} {
		if !strings.Contains(screen(m), want) {
			t.Errorf("screen lacks %q:\n%s", want, screen(m))
		}
	}
	if _, cmd = refresh(m); rang(cmd) {
		t.Fatal("the bell rang again for the same approval")
	}
}

func TestKeysDecideTheSelectedCall(t *testing.T) {
	for _, tc := range []struct {
		key               string
		approved, granted bool
		status            string
	}{
		{"a", true, false, "approved once"},
		{"A", true, true, "approved for the rest of the session"},
		{"d", false, false, "denied"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m, d := newModel(t)
			first, second := askReq, askReq
			second.Tool = "github__create_pull_request"
			waiting(t, d.q, first)
			waitPending(t, d.q, 1)
			done := waiting(t, d.q, second)
			waitPending(t, d.q, 2)
			m, _ = refresh(m)
			m, cmd := press(m, "down", tc.key)
			m = settle(m, cmd)
			select {
			case out := <-done:
				if out.Approved != tc.approved {
					t.Fatalf("outcome = %+v, want approved %v", out, tc.approved)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the selected call was not decided")
			}
			if _, granted, err := d.q.Granted(t.Context(), "codex", "s1", second.Tool); err != nil || granted != tc.granted {
				t.Fatalf("granted = %v, err %v; want %v", granted, err, tc.granted)
			}
			if !strings.Contains(screen(m), tc.status) {
				t.Fatalf("screen lacks %q:\n%s", tc.status, screen(m))
			}
			if p, err := d.q.Pending(t.Context()); err != nil || len(p) != 1 || p[0].Tool != first.Tool {
				t.Fatalf("pending = %+v, %v; the other call should still wait", p, err)
			}
		})
	}
}

func TestFeedFollowsReceiptsAndFilters(t *testing.T) {
	m, d := newModel(t)
	appendReceipt(t, d.log, "claude", "memory_write", "allow")
	appendReceipt(t, d.log, "codex", "github__get_me", "allow")
	appendReceipt(t, d.log, "codex", "github__delete_repo", "deny")
	m, _ = refresh(m)
	for _, want := range []string{"memory_write", "github__get_me", "github__delete_repo", "agents in the last hour: claude"} {
		if !strings.Contains(screen(m), want) {
			t.Errorf("screen lacks %q:\n%s", want, screen(m))
		}
	}
	m, _ = press(m, "/", "c", "o", "d", "e", "x", "enter")
	s := screen(m)
	if !strings.Contains(s, "filter: codex") || !strings.Contains(s, "github__get_me") || strings.Contains(s, "memory_write") {
		t.Fatalf("filtered screen:\n%s", s)
	}
	m, _ = press(m, "esc")
	if !strings.Contains(screen(m), "memory_write") {
		t.Fatal("esc did not clear the filter")
	}
	appendReceipt(t, d.log, "claude", "memory_search", "allow")
	if m, _ = refresh(m); !strings.Contains(screen(m), "memory_search") {
		t.Fatal("a new receipt did not reach the feed")
	}
}

func TestVerifyKey(t *testing.T) {
	m, d := newModel(t)
	appendReceipt(t, d.log, "claude", "memory_write", "allow")
	m, cmd := press(m, "v")
	if m = settle(m, cmd); !strings.Contains(screen(m), "1 receipts, chain intact") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestHelpAndQuit(t *testing.T) {
	m, _ := newModel(t)
	m, _ = press(m, "?")
	if !strings.Contains(screen(m), "approve this tool for the rest of that agent's session") {
		t.Fatalf("help:\n%s", screen(m))
	}
	m, _ = press(m, "x")
	if !strings.Contains(screen(m), "RECEIPTS") {
		t.Fatal("a key did not close the help")
	}
	_, cmd := press(m, "q")
	if msgs := messages(cmd); len(msgs) != 1 || msgs[0] != (tea.QuitMsg{}) {
		t.Fatalf("q gave %v, want tea.QuitMsg", msgs)
	}
	for _, mode := range []string{"?", "/"} {
		inMode, _ := press(m, mode)
		_, cmd = press(inMode, "ctrl+c")
		if msgs := messages(cmd); len(msgs) != 1 || msgs[0] != (tea.QuitMsg{}) {
			t.Fatalf("ctrl+c after %q gave %v, want tea.QuitMsg", mode, msgs)
		}
	}
}

// A poll every 200 ms beats a human: the call the user highlighted can be decided elsewhere or expire
// between reading it and pressing a key. That key must not land on another call.
func TestAKeyAfterTheSelectedCallLeftDecidesNothing(t *testing.T) {
	m, d := newModel(t)
	first, second := askReq, askReq
	second.Tool = "github__create_pull_request"
	waiting(t, d.q, first)
	waitPending(t, d.q, 1)
	waiting(t, d.q, second)
	waitPending(t, d.q, 2)
	m, _ = refresh(m)
	m, _ = press(m, "down")
	p, err := d.q.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = d.q.Decide(t.Context(), p[1].ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	m, _ = refresh(m)
	if want := fmt.Sprintf("#%d is no longer waiting", p[1].ID); !strings.Contains(screen(m), want) {
		t.Fatalf("screen lacks %q:\n%s", want, screen(m))
	}
	m, cmd := press(m, "a")
	m = settle(m, cmd)
	if left, err := d.q.Pending(t.Context()); err != nil || len(left) != 1 || left[0].ID != p[0].ID {
		t.Fatalf("pending = %+v, %v; the first call must still wait", left, err)
	}
	if !strings.Contains(screen(m), "up or down") {
		t.Fatalf("a with nothing selected should ask to pick a call:\n%s", screen(m))
	}
}

func TestSelectionFollowsTheCallWhenAnOlderOneLeaves(t *testing.T) {
	m, d := newModel(t)
	first, second := askReq, askReq
	second.Tool = "github__create_pull_request"
	waiting(t, d.q, first)
	waitPending(t, d.q, 1)
	done := waiting(t, d.q, second)
	waitPending(t, d.q, 2)
	m, _ = refresh(m)
	m, _ = press(m, "down")
	p, err := d.q.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = d.q.Decide(t.Context(), p[0].ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	m, _ = refresh(m)
	m, cmd := press(m, "a")
	m = settle(m, cmd)
	select {
	case out := <-done:
		if !out.Approved || out.ID != p[1].ID {
			t.Fatalf("outcome = %+v, want #%d approved", out, p[1].ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the selected call was not decided")
	}
	if want := fmt.Sprintf("#%d approved once", p[1].ID); !strings.Contains(screen(m), want) {
		t.Fatalf("screen lacks %q:\n%s", want, screen(m))
	}
}

// The approver has to see what is being approved, not a one-line prefix of it.
func TestTheSelectedCallShowsItsWholeArguments(t *testing.T) {
	m, d := newModel(t)
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	long := askReq
	long.Args = `{"cmd":"` + strings.Repeat("a", 300) + "MARKER" + strings.Repeat("b", 3000) + `"}`
	waiting(t, d.q, long)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	s := screen(m)
	var joined strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Fatalf("a line of %d cells in a 60-cell window: %q", w, line)
		}
		joined.WriteString(strings.TrimSpace(line))
	}
	if !strings.Contains(joined.String(), "MARKER") {
		t.Fatalf("the arguments past the first line are not shown:\n%s", s)
	}
	if !strings.Contains(s, "more lines") {
		t.Fatalf("no sign that the arguments go on:\n%s", s)
	}
}

func pendingCalls(n int) []approval.Pending {
	out := make([]approval.Pending, n)
	for i := range out {
		out[i] = approval.Pending{Request: askReq, ID: int64(i + 1), Deadline: time.Now().Add(time.Minute)}
	}
	return out
}

func TestWaitingRowsFitTheWindow(t *testing.T) {
	m, _ := newModel(t)
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	m, _ = update(m, snapshotMsg{pending: pendingCalls(15)})
	m, _ = press(m, "down", "down", "down", "down", "down", "down", "down", "down", "down", "down")
	m, _ = update(m, statusMsg("the status line"))
	s := screen(m)
	lines := strings.Split(s, "\n")
	if len(lines) > 20 {
		t.Fatalf("%d lines in a 20-line window:\n%s", len(lines), s)
	}
	if !strings.Contains(s, "#11  ") || !strings.Contains(s, "more waiting") {
		t.Fatalf("the selected call #11 or the count of hidden calls is missing:\n%s", s)
	}
	if !strings.Contains(lines[len(lines)-1], "the status line") {
		t.Fatalf("the status line is not the last line:\n%s", s)
	}
}

func TestAPollErrorClearsWhenAPollSucceeds(t *testing.T) {
	m, _ := newModel(t)
	m, _ = update(m, statusMsg("#7 approved once: codex x"))
	m, _ = update(m, snapshotMsg{err: errors.New("database is locked")})
	if !strings.Contains(screen(m), "error: database is locked") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	m, _ = refresh(m)
	if s := screen(m); strings.Contains(s, "error:") || !strings.Contains(s, "#7 approved once") {
		t.Fatalf("after a good poll the error should go and the last action's status come back:\n%s", s)
	}
}

// A failing poll must not hide what the user's own last key did.
func TestAPollErrorKeepsTheUsersStatus(t *testing.T) {
	m, _ := newModel(t)
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	m, _ = update(m, snapshotMsg{pending: pendingCalls(15)})
	m, _ = update(m, statusMsg("#7 approved once: codex x"))
	m, _ = update(m, snapshotMsg{err: errors.New("database is locked")})
	s := screen(m)
	if !strings.Contains(s, "error: database is locked") || !strings.Contains(s, "#7 approved once") {
		t.Fatalf("the poll error and the user's status should both show:\n%s", s)
	}
	if n := len(strings.Split(s, "\n")); n > 20 {
		t.Fatalf("%d lines in a 20-line window:\n%s", n, s)
	}
}

// After a decision nothing is highlighted, and the next a, A or d would do nothing; the screen says so
// before the user presses one.
func TestTheScreenSaysToPickACallAfterADecision(t *testing.T) {
	m, d := newModel(t)
	first, second := askReq, askReq
	second.Tool = "github__create_pull_request"
	waiting(t, d.q, first)
	waitPending(t, d.q, 1)
	waiting(t, d.q, second)
	waitPending(t, d.q, 2)
	m, _ = refresh(m)
	if strings.Contains(screen(m), "nothing highlighted") {
		t.Fatalf("the first call is highlighted, yet the screen says nothing is:\n%s", screen(m))
	}
	m, cmd := press(m, "a")
	m = settle(m, cmd)
	m, _ = refresh(m)
	if s := screen(m); !strings.Contains(s, "nothing highlighted") || !strings.Contains(s, "up, down pick a call") {
		t.Fatalf("the screen does not say to pick a call:\n%s", s)
	}
}

func TestPollingTwiceDoesNotDuplicateTheFeed(t *testing.T) {
	m, d := newModel(t)
	appendReceipt(t, d.log, "claude", "memory_write", "allow")
	m, _ = refresh(m)
	m, _ = refresh(m)
	appendReceipt(t, d.log, "claude", "memory_search", "allow")
	m, _ = refresh(m)
	m, _ = refresh(m)
	if s := screen(m); strings.Count(s, "memory_write") != 1 || strings.Count(s, "memory_search") != 1 {
		t.Fatalf("each receipt should be shown once:\n%s", s)
	}
}

// An agent controls the text of its arguments. Escape sequences in them could set the clipboard, move
// the cursor or rewrite the screen, and a bidirectional override could disguise what is being
// approved; none of that may reach the terminal raw, and no line may run past the window.
func TestHostileTextCannotReachTheTerminal(t *testing.T) {
	m, d := newModel(t)
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	hostile := askReq
	hostile.Agent = "co\x1b[2Jdex"
	hostile.Tool = "gh\u009b2Jissue" + strings.Repeat("T", 200)
	hostile.Args = "{\"x\":\"\x1b]52;c;ZXZpbA==\x07\u202egnp.exe\nsecond line" + strings.Repeat("A", 5000) + "\"}"
	appendReceipt(t, d.log, "cl\x1b[2Jaude", "mem\u202eory\x1b]0;title\x07", "al\alow"+strings.Repeat("D", 200))
	waiting(t, d.q, hostile)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	check := func(s string) {
		t.Helper()
		for _, bad := range []string{"\x1b]", "\x1b[2J", "\a", "\u202e", "\u009b"} {
			if strings.Contains(s, bad) {
				t.Errorf("screen contains %q raw", bad)
			}
		}
		for _, line := range strings.Split(s, "\n") {
			if w := lipgloss.Width(line); w > 60 {
				t.Fatalf("a line of %d cells in a 60-cell window: %q", w, line)
			}
		}
	}
	s := screen(m)
	check(s)
	for _, want := range []string{`\u001b]52`, `co\u001b[2Jdex`, `cl\u001b[2Jaude`, `mem\u202eory`} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q, the escaped form:\n%s", want, s)
		}
	}
	// The decision's status line carries the agent and tool names too.
	m, cmd := press(m, "a")
	m = settle(m, cmd)
	if s = screen(m); !strings.Contains(s, `approved once: co\u001b[2Jdex`) {
		t.Errorf("the status line does not show the decision, escaped:\n%s", s)
	}
	check(s)
}
