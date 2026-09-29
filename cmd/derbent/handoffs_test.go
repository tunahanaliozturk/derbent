package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/handoff"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// handoffDB creates a database with three handoffs titled title 0 to title 2, the first open, the second
// taken and the third done, and returns its path.
func handoffDB(t *testing.T, title string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := handoff.NewStore(db)
	for i, to := range []string{"reviewer", handoff.Anyone, "codex"} {
		id, createErr := s.Create(t.Context(), handoff.Handoff{
			Project: "/work/shop", From: "claude", FromSession: "s1", To: to, Title: fmt.Sprint(title, " ", i), Body: "b",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if i > 0 {
			if _, err = s.Take(t.Context(), id, "codex", "s2"); err != nil {
				t.Fatal(err)
			}
		}
		if i > 1 {
			if _, err = s.Finish(t.Context(), id, "codex", "done it"); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

// derbent handoffs lists every project's open handoffs, and every state with --all, newest first, as rows
// and as JSON lines, with the text an agent wrote escaped in both.
func TestHandoffsListsThemEscaped(t *testing.T) {
	title := "Review \x1b]0;x\x07\u202e" + sneaky
	db := handoffDB(t, title)
	open := runOK(t, "handoffs", "--db", db)
	if strings.Count(open, "\n") != 2 || !strings.Contains(open, "#1  open") || strings.ContainsAny(open, "\x1b\a\u202e"+sneaky) {
		t.Fatalf("open handoffs:\n%q", open)
	}
	all := runOK(t, "handoffs", "--all", "--db", db)
	if strings.Count(all, "\n") != 4 || !strings.Contains(all, "#3  done") || !strings.Contains(all, "#2  taken") ||
		strings.Index(all, "#3") > strings.Index(all, "#1") {
		t.Fatalf("every handoff, newest first:\n%s", all)
	}
	js := runOK(t, "handoffs", "--all", "--json", "--db", db)
	if strings.Count(js, "\n") != 3 || strings.ContainsAny(js, "\x1b\a\u202e"+sneaky) {
		t.Fatalf("JSON lines: %q", js)
	}
	var first handoffLine
	if err := json.Unmarshal([]byte(strings.SplitN(js, "\n", 2)[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.ID != 3 || first.State != "done" || first.TakenBy != "codex" || first.Note != "done it" || first.Title != title+" 2" || first.Done == "" {
		t.Fatalf("the newest line = %+v", first)
	}
}

// The commands that only read never migrate, so right after an upgrade they meet a database without the
// handoffs table. It holds no handoffs.
func TestHandoffsReadsADatabaseFromBeforeHandoffs(t *testing.T) {
	db := filepath.Join(t.TempDir(), "p.db")
	databaseAtVersion(t, db, 6) // before 0007, which added the handoffs table
	if out := runOK(t, "handoffs", "--db", db); strings.TrimSpace(out) != "no open handoffs" {
		t.Fatalf("handoffs output %q", out)
	}
	if out := runOK(t, "handoffs", "--all", "--json", "--db", db); out != "" {
		t.Fatalf("handoffs --json output %q", out)
	}
}
