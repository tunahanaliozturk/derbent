package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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
	"github.com/tunahanaliozturk/derbent/internal/visible"
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
	m = later(m, 0)           // the model's clock stands still until a test moves it
	m, _ = update(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return m, d
}

// later stops the model's clock at d after its current time.
func later(m Model, d time.Duration) Model {
	at := m.now().Add(d)
	m.now = func() time.Time { return at }
	return m
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
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
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
	RuleKey: "key-of-rule-2",
}

// sneaky holds runes a terminal draws as nothing, as a blank, or uses to change how text reads: a tag
// character, a zero-width space, a zero-width joiner, a line separator, a variation selector, the four
// Hangul fillers, the combining grapheme joiner and the braille blank.
const (
	sneaky = "\U000e0041\U0000200b\U0000200d\U00002028\U0000fe0f" +
		"\U0000115f\U00001160\U00003164\U0000ffa0\U0000034f\U00002800"
	sneakyEscaped = "\\U000e0041\\u200b\\u200d\\u2028\\ufe0f" +
		"\\u115f\\u1160\\u3164\\uffa0\\u034f\\u2800"
)

// noRawText fails the test when s holds an escape sequence, a bell, a C1 control, a bidirectional
// override or a rune of sneaky as it is, or a line wider than width cells.
func noRawText(t *testing.T, s string, width int) {
	t.Helper()
	for _, bad := range []string{"\x1b]", "\x1b[2J", "\a", "\U0000202e", "\u009b"} {
		if strings.Contains(s, bad) {
			t.Errorf("screen contains %q raw", bad)
		}
	}
	if i := strings.IndexAny(s, sneaky); i >= 0 {
		t.Errorf("screen contains an invisible rune raw at byte %d: %q", i, s)
	}
	for _, line := range strings.Split(s, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("a line of %d cells in a %d-cell window: %q", w, width, line)
		}
	}
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
		keys              []string
		approved, granted bool
		status            string
	}{
		{[]string{"a"}, true, false, "#2 approved once: codex github__create_pull_request"},
		{[]string{"A", "A"}, true, true, "#2 approved github__create_pull_request calls that rule 2 asks about, for the rest of codex's session"},
		{[]string{"d"}, false, false, "#2 denied: codex github__create_pull_request"},
	} {
		t.Run(tc.keys[0], func(t *testing.T) {
			m, d := newModel(t)
			first, second := askReq, askReq
			second.Tool = "github__create_pull_request"
			waiting(t, d.q, first)
			waitPending(t, d.q, 1)
			done := waiting(t, d.q, second)
			waitPending(t, d.q, 2)
			m, _ = refresh(m)
			m, _ = press(m, "down")
			m, cmd := press(later(m, armAfter), tc.keys...)
			m = settle(m, cmd)
			select {
			case out := <-done:
				if out.Approved != tc.approved {
					t.Fatalf("outcome = %+v, want approved %v", out, tc.approved)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the selected call was not decided")
			}
			if _, granted, err := d.q.Granted(t.Context(), "codex", "s1", second.Tool, second.RuleKey); err != nil || granted != tc.granted {
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
	for _, want := range []string{
		"approve this tool's calls that the same rule asks about, under",
		"the same rules above it, for the rest of that agent's session",
	} {
		if !strings.Contains(screen(m), want) {
			t.Fatalf("help lacks %q:\n%s", want, screen(m))
		}
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
	m, cmd := press(later(m, armAfter), "a")
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
	m, cmd := press(later(m, armAfter), "a")
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

// joined is the screen's lines trimmed and run together, so a word wrapped over two lines is found.
func joined(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString(strings.TrimSpace(line))
	}
	return b.String()
}

// The approver has to see what is being approved, not a prefix of it: the preview says the arguments
// go on and how to read them, and enter opens all of them.
func TestTheSelectedCallShowsItsWholeArguments(t *testing.T) {
	m, d := newModel(t)
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	long := askReq
	long.Args = `{"cmd":"` + strings.Repeat("a", 1000) + "MARKER" + strings.Repeat("b", 3000) + `"}`
	waiting(t, d.q, long)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	s := screen(m)
	noRawText(t, s, 60)
	if strings.Contains(joined(s), "MARKER") || !strings.Contains(s, "more lines, enter to read all") {
		t.Fatalf("the preview should stop before the marker and say how to read the rest:\n%s", s)
	}
	m, _ = press(m, "enter")
	s = screen(m)
	noRawText(t, s, 60)
	for _, want := range []string{"#1", "codex", "github__create_issue", "left"} {
		if !strings.Contains(s, want) {
			t.Errorf("the detail view lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(joined(s), "MARKER") {
		t.Fatalf("the detail view does not show the arguments past the preview:\n%s", s)
	}
	if m, _ = press(m, "esc"); !strings.Contains(screen(m), "RECEIPTS") {
		t.Fatalf("esc did not go back to the main screen:\n%s", screen(m))
	}
}

// Padding cannot hide the end of the arguments: a long run of spaces is shown by its length, and the
// detail view scrolls to the end whatever comes before it.
func TestTheDetailViewReachesTheTailPastARunOfSpaces(t *testing.T) {
	m, d := newModel(t)
	long := askReq
	long.Args = `{"cmd":"rm -rf` + strings.Repeat(" ", 2000) + strings.Repeat("b", 2000) + ` TAIL"}`
	waiting(t, d.q, long)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	if s := screen(m); !strings.Contains(s, "rm -rf␠×2000bbb") {
		t.Fatalf("the preview does not show the run of spaces by its length:\n%s", s)
	}
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m, _ = press(m, "enter")
	for _, step := range []struct{ key, want string }{
		{"", "lines 1-14 of 34"},
		{"down", "lines 2-15 of 34"},
		{"pgdown", "lines 16-29 of 34"},
		{"pgdown", "lines 21-34 of 34"},
		{"pgup", "lines 7-20 of 34"},
		{"up", "lines 6-19 of 34"},
		{"home", "lines 1-14 of 34"},
		{"up", "lines 1-14 of 34"},
		{"end", "lines 21-34 of 34"},
		{"down", "lines 21-34 of 34"},
	} {
		if step.key != "" {
			m, _ = press(m, step.key)
		}
		s := screen(m)
		noRawText(t, s, 60)
		if !strings.Contains(s, step.want) {
			t.Fatalf("after %q the screen lacks %q:\n%s", step.key, step.want, s)
		}
		if atEnd := strings.HasSuffix(step.want, "34 of 34"); strings.Contains(s, "TAIL") != atEnd {
			t.Fatalf("after %q the tail should show only at the end:\n%s", step.key, s)
		}
	}
}

// The preview wraps only what it can show, so megabytes of arguments cost a frame no more than a few
// bytes do.
func TestAHugeArgumentKeepsTheScreenFast(t *testing.T) {
	m, _ := newModel(t)
	calls := pendingCalls(2)
	calls[0].Args = strings.Repeat("x", 8<<20)
	calls[1].Args = calls[0].Args
	m, _ = update(m, snapshotMsg{pending: calls})
	start := time.Now()
	for range 20 {
		screen(m)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("20 frames took %s", took)
	}
	if s := screen(m); !strings.Contains(s, "more lines, enter to read all") {
		t.Fatalf("the preview does not say the arguments go on:\n%s", s)
	}
}

// Padding costs a frame no more than letters do: the preview reads no further into the arguments than
// its limit, however the spaces run, and says when a run of spaces goes on past what it read.
func TestThePreviewReadsNoFurtherIntoSpacesThanItsLimit(t *testing.T) {
	const limit = 4096
	for _, tc := range []struct{ name, args, want string }{
		{"one run of 8 MiB", strings.Repeat(" ", 8<<20), "␠×4096+"},
		{"ideographic spaces", strings.Repeat("\U00003000", 3<<20), "␠×1366+"},
		{"8 MiB of short runs", strings.Repeat("x"+strings.Repeat(" ", 1000), 8<<10), ""},
		{"a run that ends at the limit", strings.Repeat(" ", limit) + "x", "␠×4096"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, used := argText(tc.args, limit)
			if used > limit+utf8.UTFMax {
				t.Fatalf("read %d bytes of %d, limit %d", used, len(tc.args), limit)
			}
			if tc.want != "" && text != tc.want {
				t.Fatalf("argText = %q, want %q", text, tc.want)
			}
		})
	}
	m, _ := newModel(t)
	calls := pendingCalls(2)
	calls[0].Args = strings.Repeat(" ", 8<<20)
	calls[1].Args = calls[0].Args
	m, _ = update(m, snapshotMsg{pending: calls})
	s := screen(m)
	if !regexp.MustCompile(`␠×\d+\+`).MatchString(s) || !strings.Contains(s, "more lines, enter to read all") {
		t.Fatalf("the preview does not say the spaces go on:\n%s", s)
	}
}

// Only spaces that draw as a blank are shortened to ␠×N. A run of carriage returns or other control
// or line-breaking spaces keeps its escaped form, so the user sees what the agent actually sent.
func TestARunOfControlSpacesShowsEscaped(t *testing.T) {
	for _, r := range "\r\v\f\u0085\u2028\u2029" {
		args := strings.Repeat(string(r), 9)
		if text, _ := argText(args, 4096); strings.Contains(text, "␠") || text != visible.Escape(args) {
			t.Errorf("argText(nine %U) = %q, want %q", r, text, visible.Escape(args))
		}
	}
	if text, _ := argText(strings.Repeat("\r", 9), 4096); text != strings.Repeat(`\u000d`, 9) {
		t.Errorf("argText(nine \\r) = %q", text)
	}
}

// A approves a tool for the rest of an agent's session, so it takes a second press within five
// seconds: a terminal that reports no modifiers sends an a typed with Caps Lock on as the byte A, which
// Bubble Tea reads as shift+a.
func TestSessionApprovalNeedsASecondA(t *testing.T) {
	legacyCapsA := tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift}
	for _, tc := range []struct {
		name    string
		steps   []any // a key name, a key message, or a time.Duration to move the clock by
		granted bool
	}{
		{"once", []any{"A"}, false},
		{"twice", []any{"A", "A"}, true},
		{"another key between", []any{"A", "down", "A"}, false},
		{"more than five seconds apart", []any{"A", confirmFor + time.Millisecond, "A"}, false},
		{"Caps Lock a, modifiers not reported", []any{legacyCapsA}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d := newModel(t)
			done := waiting(t, d.q, askReq)
			waitPending(t, d.q, 1)
			m, _ = refresh(m)
			m = later(m, armAfter)
			var cmd tea.Cmd
			for _, step := range tc.steps {
				switch s := step.(type) {
				case string:
					m, cmd = press(m, s)
				case tea.KeyPressMsg:
					m, cmd = update(m, s)
				case time.Duration:
					m, cmd = later(m, s), nil
				}
				m = settle(m, cmd)
			}
			if _, granted, err := d.q.Granted(t.Context(), "codex", "s1", askReq.Tool, askReq.RuleKey); err != nil || granted != tc.granted {
				t.Fatalf("granted = %v, err %v; want %v", granted, err, tc.granted)
			}
			if tc.granted {
				<-done
				return
			}
			if p, err := d.q.Pending(t.Context()); err != nil || len(p) != 1 {
				t.Fatalf("pending = %+v, %v; the call should still wait", p, err)
			}
			want := "press A again to approve github__create_issue calls that rule 2 asks about, for the rest of codex's session"
			if !strings.Contains(screen(m), want) {
				t.Fatalf("screen lacks %q:\n%s", want, screen(m))
			}
		})
	}
}

// An approval has no rule key when its rule matched on an argument it could not read, or when a gate
// from before grants followed the rule, still running after the upgrade, asked. Such an approval grants
// nothing, so A on one says it approves the call once, and does.
func TestSessionApprovalWithNoRuleKeySaysItApprovesOnce(t *testing.T) {
	m, d := newModel(t)
	keyless := askReq
	keyless.RuleKey = ""
	done := waiting(t, d.q, keyless)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	m, _ = press(later(m, armAfter), "A")
	scope := "codex's github__create_issue call once; this call cannot be approved for the session"
	if want := "press A again to approve " + scope; !strings.Contains(screen(m), want) {
		t.Fatalf("screen lacks %q:\n%s", want, screen(m))
	}
	m, cmd := press(m, "A")
	m = settle(m, cmd)
	if out := <-done; !out.Approved {
		t.Fatalf("outcome = %+v, want it approved", out)
	}
	if want := "approved " + scope; !strings.Contains(screen(m), want) {
		t.Fatalf("screen lacks %q:\n%s", want, screen(m))
	}
}

// A terminal that reports modifiers sends an a typed with Caps Lock on as the a key with Caps Lock and
// the text A. That is an a: it approves the call once and grants nothing.
func TestCapsLockAApprovesOnce(t *testing.T) {
	m, d := newModel(t)
	done := waiting(t, d.q, askReq)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	m, cmd := update(later(m, armAfter), tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModCapsLock})
	m = settle(m, cmd)
	if p, err := d.q.Pending(t.Context()); err != nil || len(p) != 0 {
		t.Fatalf("pending = %+v, %v; a Caps Lock a should approve the call once:\n%s", p, err, screen(m))
	}
	if out := <-done; !out.Approved {
		t.Fatalf("outcome = %+v, want it approved", out)
	}
	if _, granted, err := d.q.Granted(t.Context(), "codex", "s1", askReq.Tool, askReq.RuleKey); err != nil || granted {
		t.Fatalf("granted = %v, err %v; a Caps Lock a granted the session", granted, err)
	}
	if !strings.Contains(screen(m), "approved once") {
		t.Fatalf("screen lacks %q:\n%s", "approved once", screen(m))
	}
}

// A call that has just been highlighted, because the one before it left, takes no decision until it has
// been on screen for a moment: a key meant for the call before must not land on it.
func TestANewlyHighlightedCallIgnoresKeysAtFirst(t *testing.T) {
	m, d := newModel(t)
	first, second := askReq, askReq
	second.Tool = "github__create_pull_request"
	waiting(t, d.q, first)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	p, err := d.q.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = d.q.Decide(t.Context(), p[0].ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	m, _ = refresh(m) // an empty poll
	done := waiting(t, d.q, second)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	p, err = d.q.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, cmd := press(m, "a")
	m = settle(m, cmd)
	if want := fmt.Sprintf("#%d just appeared; press again to decide", p[0].ID); !strings.Contains(screen(m), want) {
		t.Fatalf("screen lacks %q:\n%s", want, screen(m))
	}
	if left, err := d.q.Pending(t.Context()); err != nil || len(left) != 1 {
		t.Fatalf("pending = %+v, %v; the new call should still wait", left, err)
	}
	m, cmd = press(later(m, armAfter), "a")
	settle(m, cmd)
	if out := <-done; !out.Approved {
		t.Fatalf("outcome = %+v, want it approved once it had been on screen", out)
	}
}

// A call highlighted while the help, the detail view or the memory browser hides the main screen has not
// been seen: it takes a, A or d only once the main screen has shown it for a moment.
func TestACallHighlightedOutOfSightArmsWhenTheMainScreenReturns(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T, m Model, d deps) Model // opens the other screen
		back string                                    // the key that closes it
	}{
		{"help", func(_ *testing.T, m Model, _ deps) Model {
			m, _ = press(m, "?")
			return m
		}, "x"},
		{"memory browser", func(_ *testing.T, m Model, _ deps) Model {
			m, _ = press(m, "m")
			return m
		}, "esc"},
		{"detail view", func(t *testing.T, m Model, d deps) Model {
			t.Helper()
			waiting(t, d.q, askReq)
			waitPending(t, d.q, 1)
			m, _ = refresh(m)
			m, _ = press(m, "enter")
			p, err := d.q.Pending(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = d.q.Decide(t.Context(), p[0].ID, approval.Deny); err != nil {
				t.Fatal(err)
			}
			m, _ = refresh(m) // an empty poll: the view says its call has gone
			return m
		}, "esc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d := newModel(t)
			m = tc.open(t, m, d)
			next := askReq
			next.Tool = "github__create_pull_request"
			done := waiting(t, d.q, next)
			waitPending(t, d.q, 1)
			m, _ = refresh(m) // highlights the call while the other screen is open
			if strings.Contains(screen(m), "RECEIPTS") {
				t.Fatalf("the %s should still hide the main screen:\n%s", tc.name, screen(m))
			}
			m, cmd := press(later(m, armAfter), tc.back, "a")
			m = settle(m, cmd)
			if left, err := d.q.Pending(t.Context()); err != nil || len(left) != 1 {
				t.Fatalf("pending = %+v, %v; a at once after the %s decided a call not yet seen", left, err, tc.name)
			}
			if !strings.Contains(screen(m), "just appeared") {
				t.Fatalf("the screen does not say why the key did nothing:\n%s", screen(m))
			}
			m, cmd = press(later(m, armAfter), "a")
			settle(m, cmd)
			select {
			case out := <-done:
				if !out.Approved {
					t.Fatalf("outcome = %+v, want it approved once it had been on screen", out)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a 750 ms after the main screen came back did not decide the call")
			}
		})
	}
}

// An a pressed twice approves the call it meant to and not the one that arrives after it.
func TestADoubleTapDoesNotDecideTheNextCall(t *testing.T) {
	m, d := newModel(t)
	done := waiting(t, d.q, askReq)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	m, cmd := press(later(m, armAfter), "a")
	m = settle(m, cmd)
	if out := <-done; !out.Approved {
		t.Fatalf("outcome = %+v, want the first call approved", out)
	}
	m, _ = refresh(m)
	second := askReq
	second.Tool = "github__create_pull_request"
	waiting(t, d.q, second)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	m, cmd = press(m, "a")
	m = settle(m, cmd)
	if p, err := d.q.Pending(t.Context()); err != nil || len(p) != 1 || p[0].Tool != second.Tool {
		t.Fatalf("pending = %+v, %v; the second tap decided the next call", p, err)
	}
	if !strings.Contains(screen(m), "just appeared") {
		t.Fatalf("the screen does not say why the key did nothing:\n%s", screen(m))
	}
}

// In the detail view a, A and d act on the call it shows, under the same rules as the main screen.
func TestTheDetailViewDecidesItsCall(t *testing.T) {
	m, d := newModel(t)
	first, second := askReq, askReq
	second.Tool = "github__create_pull_request"
	done := waiting(t, d.q, first)
	waitPending(t, d.q, 1)
	waiting(t, d.q, second)
	waitPending(t, d.q, 2)
	m, _ = refresh(m)
	m, _ = press(m, "enter")
	m, cmd := press(m, "a")
	if m = settle(m, cmd); !strings.Contains(screen(m), "just appeared") {
		t.Fatalf("a key at once should wait for the call to be seen:\n%s", screen(m))
	}
	m, cmd = press(later(m, armAfter), "A")
	if m = settle(m, cmd); !strings.Contains(screen(m), "press A again") {
		t.Fatalf("A should ask for a second press:\n%s", screen(m))
	}
	m, cmd = press(m, "a")
	m = settle(m, cmd)
	if out := <-done; !out.Approved {
		t.Fatalf("outcome = %+v, want the call in the detail view approved", out)
	}
	if !strings.Contains(screen(m), "RECEIPTS") {
		t.Fatalf("after a decision the main screen should come back:\n%s", screen(m))
	}
	if p, err := d.q.Pending(t.Context()); err != nil || len(p) != 1 || p[0].Tool != second.Tool {
		t.Fatalf("pending = %+v, %v; the other call should still wait", p, err)
	}
}

// A call can stop waiting while its detail view is open. The view says so, and a key meant for it
// decides nothing.
func TestTheDetailViewSaysWhenItsCallHasGone(t *testing.T) {
	m, d := newModel(t)
	first, second := askReq, askReq
	second.Tool = "github__create_pull_request"
	waiting(t, d.q, first)
	waitPending(t, d.q, 1)
	waiting(t, d.q, second)
	waitPending(t, d.q, 2)
	m, _ = refresh(m)
	m, _ = press(later(m, armAfter), "enter")
	p, err := d.q.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = d.q.Decide(t.Context(), p[0].ID, approval.Deny); err != nil {
		t.Fatal(err)
	}
	m, _ = refresh(m)
	if want := fmt.Sprintf("#%d is no longer waiting", p[0].ID); !strings.Contains(screen(m), want) {
		t.Fatalf("screen lacks %q:\n%s", want, screen(m))
	}
	for _, k := range []string{"a", "A", "A", "d"} {
		var cmd tea.Cmd
		m, cmd = press(m, k)
		m = settle(m, cmd)
	}
	if left, err := d.q.Pending(t.Context()); err != nil || len(left) != 1 || left[0].ID != p[1].ID {
		t.Fatalf("pending = %+v, %v; a key in the detail view of a call that left decided another", left, err)
	}
	if m, _ = press(m, "esc"); !strings.Contains(screen(m), "RECEIPTS") {
		t.Fatalf("esc did not go back to the main screen:\n%s", screen(m))
	}
}

func TestHelpFitsTheWindow(t *testing.T) {
	m, _ := newModel(t)
	m, _ = update(m, tea.WindowSizeMsg{Width: 20, Height: 30})
	m, _ = press(m, "?")
	s := screen(m)
	noRawText(t, s, 20)
	if !strings.Contains(s, "derbent keys") {
		t.Fatalf("help:\n%s", s)
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
	m, cmd := press(later(m, armAfter), "a")
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
	// Ideographic, no-break and em spaces pad as well as ASCII ones do.
	wide := strings.Repeat("\U00003000\U000000a0\U00002003", 7)
	hostile.Args = sneaky + wide + "{\"x\":\"\x1b]52;c;ZXZpbA==\x07\U0000202egnp.exe\nsecond line" + strings.Repeat("A", 5000) + "\"}"
	appendReceipt(t, d.log, "cl\x1b[2Jaude", "mem\U0000202eory\x1b]0;title\x07"+sneaky, "al\alow"+strings.Repeat("D", 200))
	waiting(t, d.q, hostile)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	s := screen(m)
	noRawText(t, s, 60)
	for _, want := range []string{`\u001b]52`, `co\u001b[2Jdex`, `cl\u001b[2Jaude`, "mem\\u202eory", sneakyEscaped + "␠×21{"} {
		if !strings.Contains(joined(s), want) {
			t.Errorf("screen lacks %q, the escaped form:\n%s", want, s)
		}
	}
	// The detail view shows the same text, escaped the same way.
	m, _ = press(m, "enter")
	s = screen(m)
	noRawText(t, s, 60)
	for _, want := range []string{`\u001b]52`, `co\u001b[2Jdex`, `gh\u009b2Jissue`, sneakyEscaped + "␠×21{"} {
		if !strings.Contains(joined(s), want) {
			t.Errorf("the detail view lacks %q, the escaped form:\n%s", want, s)
		}
	}
	// A's question and the decision's status line carry the agent and tool names too.
	m, _ = press(later(m, armAfter), "A")
	if s = screen(m); !strings.Contains(s, `press A again to approve gh\u009b2Jissue`) {
		t.Errorf("the status line does not ask for a second A, escaped:\n%s", s)
	}
	noRawText(t, s, 60)
	m, _ = press(m, "esc")
	m, cmd := press(later(m, armAfter), "a") // back on the main screen, the call arms again
	m = settle(m, cmd)
	if s = screen(m); !strings.Contains(s, `approved once: co\u001b[2Jdex`) {
		t.Errorf("the status line does not show the decision, escaped:\n%s", s)
	}
	noRawText(t, s, 60)
}
