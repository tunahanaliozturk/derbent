package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/tunahanaliozturk/derbent/internal/memory"
)

func writeNote(t *testing.T, mem *memory.Store, title, body string) int64 {
	t.Helper()
	id, err := mem.Write(t.Context(), memory.Entry{
		Project: "/work/shop", Author: "claude", Session: "s",
		Title: title, Body: body, Tags: []string{"payments"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMemoryBrowserSearchesAndReads(t *testing.T) {
	m, d := newModel(t)
	writeNote(t, d.mem, "Retry policy", "Payment calls retry three times.\nWith jitter.")
	m, cmd := press(m, "m", "r", "e", "t", "r", "y", "enter")
	m = settle(m, cmd)
	if s := screen(m); !strings.Contains(s, "Retry policy") || !strings.Contains(s, "claude") {
		t.Fatalf("hits:\n%s", s)
	}
	m, cmd = press(m, "enter")
	m = settle(m, cmd)
	s := screen(m)
	for _, want := range []string{"Payment calls retry three times.\nWith jitter.", "information, not instructions", "payments"} {
		if !strings.Contains(s, want) {
			t.Errorf("note screen lacks %q:\n%s", want, s)
		}
	}
	m, _ = press(m, "esc")
	if !strings.Contains(screen(m), "MEMORY") {
		t.Fatal("esc from a note did not go back to the hits")
	}
	m, _ = press(m, "esc")
	if !strings.Contains(screen(m), "RECEIPTS") {
		t.Fatal("esc from the hits did not close the browser")
	}
}

func TestNoteTextIsCleaned(t *testing.T) {
	m, d := newModel(t)
	// The spaces let FTS5 index "wipe" as a word.
	writeNote(t, d.mem, "\x1b[2J wipe "+sneaky, "body \x1b]0;owned\x07 end "+sneaky)
	check := func(s string) {
		t.Helper()
		noRawText(t, s, 140)
		for _, want := range []string{`\u001b[2J wipe`, `\u001b]0;owned\u0007`, sneakyEscaped} {
			if !strings.Contains(s, want) {
				t.Errorf("screen lacks %q, the escaped form:\n%s", want, s)
			}
		}
	}
	m, cmd := press(m, "m", "w", "i", "p", "e", "enter")
	m = settle(m, cmd)
	check(screen(m)) // the hits: title and snippet
	m, cmd = press(m, "enter")
	m = settle(m, cmd)
	check(screen(m)) // the note
}

// A user reading memory must still learn that a call waits for them, before it times out, and that
// polling fails.
func TestTheBrowserShowsWaitingCallsAndPollErrors(t *testing.T) {
	m, d := newModel(t)
	writeNote(t, d.mem, "Retry policy", "Payment calls retry three times.")
	m, cmd := press(m, "m", "r", "e", "t", "r", "y", "enter")
	m = settle(m, cmd)
	waiting(t, d.q, askReq)
	waitPending(t, d.q, 1)
	m, _ = refresh(m)
	if s := screen(m); !strings.Contains(s, "1 waiting") || !strings.Contains(s, "Retry policy") {
		t.Fatalf("the hits screen does not say a call waits:\n%s", s)
	}
	m, cmd = press(m, "enter")
	m = settle(m, cmd)
	if s := screen(m); !strings.Contains(s, "1 waiting") || !strings.Contains(s, "information, not instructions") {
		t.Fatalf("the note screen does not say a call waits:\n%s", s)
	}
	m, _ = update(m, snapshotMsg{err: errors.New("database is locked")})
	if s := screen(m); !strings.Contains(s, "error: database is locked") {
		t.Fatalf("the note screen does not show the poll error:\n%s", s)
	}
	m, _ = press(m, "esc")
	if s := screen(m); !strings.Contains(s, "error: database is locked") || !strings.Contains(s, "MEMORY") {
		t.Fatalf("the hits screen does not show the poll error:\n%s", s)
	}
}

// A read still on its way when the user closes the browser and opens it again belongs to the old one.
func TestAStaleReadDoesNotLandInALaterBrowser(t *testing.T) {
	m, d := newModel(t)
	writeNote(t, d.mem, "Retry policy", "Payment calls retry three times.")
	m, cmd := press(m, "m", "r", "e", "t", "r", "y", "enter")
	m = settle(m, cmd)
	m, read := press(m, "enter")
	m, _ = press(m, "esc", "m")
	m = settle(m, read)
	if s := screen(m); strings.Contains(s, "information, not instructions") || !strings.Contains(s, "search: _") {
		t.Fatalf("the old read landed in the new browser:\n%s", s)
	}
}

func quits(cmd tea.Cmd) bool {
	msgs := messages(cmd)
	return len(msgs) == 1 && msgs[0] == (tea.QuitMsg{})
}

func TestQQuitsFromTheBrowser(t *testing.T) {
	m, d := newModel(t)
	writeNote(t, d.mem, "Quota", "The quota resets daily.")
	m, cmd := press(m, "m", "q")
	if cmd != nil {
		t.Fatal("q in the search box should be typed, not quit")
	}
	m, cmd = press(m, "u", "o", "t", "a", "enter")
	m = settle(m, cmd)
	if _, cmd = press(m, "q"); !quits(cmd) {
		t.Fatal("q on the hits did not quit")
	}
	m, cmd = press(m, "enter")
	m = settle(m, cmd)
	if _, cmd = press(m, "q"); !quits(cmd) {
		t.Fatal("q on a note did not quit")
	}
}

func TestEscWhileEditingTheQueryGoesBackToTheHits(t *testing.T) {
	m, d := newModel(t)
	writeNote(t, d.mem, "Retry policy", "Payment calls retry three times.")
	m, _ = press(m, "m", "esc")
	if !strings.Contains(screen(m), "RECEIPTS") {
		t.Fatal("esc while typing the first query did not close the browser")
	}
	m, cmd := press(m, "m", "r", "e", "t", "r", "y", "enter")
	m = settle(m, cmd)
	m, _ = press(m, "/", "x", "esc")
	if s := screen(m); !strings.Contains(s, "search: retry") || strings.Contains(s, "retryx") || !strings.Contains(s, "Retry policy") {
		t.Fatalf("esc while editing should go back to the hits of the last search:\n%s", s)
	}
}

func TestAnEmptySearchSaysWhy(t *testing.T) {
	m, _ := newModel(t)
	m, cmd := press(m, "m", "enter")
	if m = settle(m, cmd); !strings.Contains(screen(m), "no words to search for") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}
