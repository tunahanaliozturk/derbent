package tui

import (
	"strings"
	"testing"

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
	writeNote(t, d.mem, "\x1b[2J wipe", "body \x1b]0;owned\x07 end") // the space lets FTS5 index "wipe" as a word
	m, cmd := press(m, "m", "w", "i", "p", "e", "enter")
	m = settle(m, cmd)
	m, cmd = press(m, "enter")
	m = settle(m, cmd)
	s := screen(m)
	if strings.Contains(s, "\x1b[2J") || strings.Contains(s, "\x1b]0;") || strings.Contains(s, "\a") {
		t.Fatalf("raw escape sequences reached the screen: %q", s)
	}
	if !strings.Contains(s, `\u001b[2J wipe`) {
		t.Fatalf("the title is not shown escaped:\n%s", s)
	}
}

func TestAnEmptySearchSaysWhy(t *testing.T) {
	m, _ := newModel(t)
	m, cmd := press(m, "m", "enter")
	if m = settle(m, cmd); !strings.Contains(screen(m), "no words to search for") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}
