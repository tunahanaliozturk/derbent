package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

// insertAnswered writes calls into the approvals table of the database at path as answered approvals. It
// names no project_rule column, so it writes a database from before migration 0006 as well.
func insertAnswered(t *testing.T, path string, calls ...approval.Answered) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range calls {
		state := "denied"
		if c.Approved {
			state = "approved"
		}
		if _, err = db.ExecContext(t.Context(), `INSERT INTO approvals
			(created_ms, deadline_ms, project, agent, session, tool, args, rule, state, decided_ms)
			VALUES (1, 2, '/work/shop', ?, 's', ?, ?, ?, ?, 3)`, c.Agent, c.Tool, c.Args, c.Rule, state); err != nil {
			t.Fatal(err)
		}
	}
}

// The milestone's evidence for suggestions: five approvals of go test commands that share "go test " print
// an allow snippet for native__Bash with that prefix, after an ask for each operator that could add another
// command, above the rule that asked, as TOML and as a JSON line, one line for each suggestion, whose rules
// are a list even when no rule asked. The text says to restart the CLI after pasting a rule for a tool the
// MCP gate serves, and only then. A tool name is escaped, in the text and in a JSON line that decodes back
// to it, and a database from before migration 0006 is read as well.
func TestSuggestPrintsTheSnippet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var calls []approval.Answered
	for _, c := range []string{"go test ./internal/a", "go test ./internal/b", "go test ./cmd/derbent", "go test -run TestX ./internal/a", "go test -count=1 ./..."} {
		calls = append(calls, approval.Answered{Agent: "claude", Tool: "native__Bash", Args: `{"command":"` + c + `"}`, Rule: 1, Approved: true})
	}
	native := filepath.Join(t.TempDir(), "native.db")
	if db, err = store.Open(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	db.Close()
	insertAnswered(t, native, calls...)
	for range 5 {
		calls = append(calls, approval.Answered{Agent: "codex", Tool: "github__delete_repo", Args: `{}`})
	}
	insertAnswered(t, path, calls...)
	want := "# claude's native__Bash: approved 5 times and never denied.\n" +
		"# Put it above rule 1 in your config, so first match reaches it before rule 1, which asked.\n" +
		"# The allow at the end also matches any options after the prefix, such as --force.\n" +
		"# The asks above it stop chained, piped, redirected and substituted commands.\n" +
		"# Answer those asks with a (once), not A: a session grant lets later calls matching that ask through.\n" +
		"# Calls the allow matches no longer reach the rules at and below rule 1.\n"
	for _, op := range []string{";", "&", "|", "`", "(", "<", ">", `\n`, `\r`} {
		want += "[[rule]]\nagent  = \"claude\"\ntool   = \"native__Bash\"\nargs   = { command = \"go test *" + op + "*\" }\naction = \"ask\"\n"
	}
	want += "[[rule]]\nagent  = \"claude\"\ntool   = \"native__Bash\"\nargs   = { command = \"go test *\" }\naction = \"allow\"\n"
	const restart = "# A running derbent mcp gate decides with the config it read when it started, so after you paste a rule\n" +
		"# for a tool that is not native__, restart the CLI.\n"
	if out := runOK(t, "suggest", "--db", path); !strings.HasPrefix(out, "# Rules your answers point to.") || !strings.Contains(out, want) ||
		!strings.Contains(out, "# codex's github__delete_repo: denied 5 times and never approved.\n") || !strings.Contains(out, restart) {
		t.Fatalf("suggest printed:\n%s\nwant it to hold:\n%s", out, want)
	}
	if out := runOK(t, "suggest", "--db", native); !strings.Contains(out, want) || strings.Contains(out, "restart") {
		t.Fatalf("suggest with only a native__ tool's snippet:\n%s", out)
	}
	lines := strings.Split(strings.TrimSuffix(runOK(t, "suggest", "--json", "--db", path), "\n"), "\n")
	var line, deny suggestionLine
	if len(lines) != 2 {
		t.Fatalf("suggest --json printed %d lines, want one for each of the 2 suggestions: %q", len(lines), lines)
	}
	if err = json.Unmarshal([]byte(lines[0]), &line); err != nil ||
		line.Arg != "command" || line.Prefix != "go test " || line.Exact || line.Approved != 5 || line.TOML != want {
		t.Fatalf("suggest --json: %v, %+v", err, line)
	}
	if err = json.Unmarshal([]byte(lines[1]), &deny); err != nil || deny.Action != "deny" || deny.Denied != 5 || deny.Arg != "" ||
		deny.Rules == nil || len(deny.Rules) != 0 || !strings.Contains(lines[1], `"rules":[]`) {
		t.Fatalf("suggest --json, second line: %v, %+v", err, deny)
	}
	if out := runOK(t, "suggest", "--min", "6", "--db", path); !strings.HasPrefix(out, "no suggestions: no agent's calls to one tool were approved at least 6 times with no denial") ||
		!strings.Contains(out, "a shell tool's calls also need") {
		t.Fatalf("suggest --min 6: %q", out)
	}
	if err = run(t.Context(), []string{"suggest", "--min", "0", "--db", path}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("--min 0 was accepted")
	}

	old := filepath.Join(t.TempDir(), "old.db")
	databaseAtVersion(t, old, 5) // before 0006, which added approvals.project_rule
	hostile := "gh\x1b]0;x\x07issue\u202e" + sneaky
	var denied []approval.Answered
	for range 5 {
		denied = append(denied, approval.Answered{Agent: "codex", Tool: hostile, Args: `{}`, Rule: 2})
	}
	insertAnswered(t, old, denied...)
	text := runOK(t, "suggest", "--db", old)
	if strings.ContainsAny(text, "\x1b\a\u202e"+sneaky) || !strings.Contains(text, "denied 5 times and never approved") ||
		!strings.Contains(text, `action = "deny"`) {
		t.Fatalf("suggest on an old database, with a hostile tool name:\n%q", text)
	}
	out := runOK(t, "suggest", "--json", "--db", old)
	if strings.ContainsAny(out, "\x1b\a\u202e"+sneaky) || strings.Count(out, "\n") != 1 {
		t.Fatalf("suggest --json on an old database, with a hostile tool name:\n%q", out)
	}
	var decoded suggestionLine
	if err = json.Unmarshal([]byte(out), &decoded); err != nil || decoded.Agent != "codex" || decoded.Tool != hostile ||
		decoded.Action != "deny" || decoded.Denied != 5 || decoded.Approved != 0 || !slices.Equal(decoded.Rules, []int{2}) ||
		!strings.HasSuffix(text, "\n\n"+decoded.TOML) {
		t.Fatalf("suggest --json on an old database decodes to %v, %+v", err, decoded)
	}
}
