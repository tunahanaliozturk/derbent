package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/store"
)

// runOK runs derbent with args in this process and returns its stdout, failing the test on an error.
func runOK(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(t.Context(), args, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("derbent %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

var grantedAt = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

type seededGrant struct {
	id                   int64
	agent, session, tool string
	rule                 int
}

// grantDB creates a database at a temporary path holding grants, each an approved approval and the
// grant it made, and returns the path.
func grantDB(t *testing.T, grants ...seededGrant) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, g := range grants {
		key := fmt.Sprint("key-", g.id)
		if _, err = db.ExecContext(t.Context(), `INSERT INTO approvals
			(id, created_ms, deadline_ms, project, agent, session, tool, args, rule, rule_key, state, decided_ms)
			VALUES (?, 1, 2, '/work/shop', ?, ?, ?, '{}', ?, ?, 'approved', ?)`,
			g.id, g.agent, g.session, g.tool, g.rule, key, grantedAt.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(t.Context(), `INSERT INTO grants (agent, session, tool, rule_key, approval_id) VALUES (?, ?, ?, ?, ?)`,
			g.agent, g.session, g.tool, key, g.id); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

var twoGrants = []seededGrant{{7, "claude", "sess-1", "native__Bash", 1}, {9, "codex", "g-2", "memory_write", 3}}

func TestGrantsListsEverySessionGrant(t *testing.T) {
	out := runOK(t, "grants", "--db", grantDB(t, twoGrants...))
	for _, want := range []string{"#7", "claude", "sess-1", "native__Bash", "rule 1", "#9", "codex", "g-2", "memory_write", "rule 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("grants output lacks %q:\n%s", want, out)
		}
	}
	if out := runOK(t, "grants", "--db", grantDB(t)); strings.TrimSpace(out) != "no session grants" {
		t.Fatalf("grants with none = %q", out)
	}
}

func TestGrantsJSONHasOneLinePerGrant(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(runOK(t, "grants", "--json", "--db", grantDB(t, twoGrants...))), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	var got grantLine
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	want := grantLine{
		ID: 7, Agent: "claude", Session: "sess-1", Tool: "native__Bash", Rule: 1,
		Granted: time.UnixMilli(grantedAt.UnixMilli()).Format(time.RFC3339Nano),
	}
	if got != want {
		t.Fatalf("first line = %+v, want %+v", got, want)
	}
}

// A grant's agent, session and tool come from agents, so both outputs escape them, and a JSON line
// still decodes to the stored text.
func TestGrantsEscapeStoredText(t *testing.T) {
	session := "s\x1b]0;x\x07\u202e"
	path := grantDB(t, seededGrant{4, "claude", session, "native__Bash", 1})
	for _, args := range [][]string{{"grants", "--db", path}, {"grants", "--json", "--db", path}} {
		if out := runOK(t, args...); strings.ContainsAny(out, "\x1b\a\u202e") {
			t.Errorf("%v printed raw control text: %q", args, out)
		}
	}
	var got grantLine
	if err := json.Unmarshal([]byte(runOK(t, "grants", "--json", "--db", path)), &got); err != nil || got.Session != session {
		t.Fatalf("decoded session = %q, err %v; want the stored text", got.Session, err)
	}
	if out := runOK(t, "revoke", "--db", path, "4"); strings.ContainsAny(out, "\x1b\a\u202e") {
		t.Fatalf("revoke printed raw control text: %q", out)
	}
}

func TestRevokeDeletesOneGrantAndSaysWhich(t *testing.T) {
	path := grantDB(t, twoGrants...)
	if out := runOK(t, "revoke", "--db", path, "#7"); !strings.Contains(out, "revoked #7: native__Bash for claude in session sess-1") {
		t.Fatalf("revoke output %q", out)
	}
	out := runOK(t, "grants", "--db", path)
	if strings.Contains(out, "#7") || !strings.Contains(out, "#9") {
		t.Fatalf("grants after revoking #7:\n%s", out)
	}
	err := run(t.Context(), []string{"revoke", "--db", path, "7"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no session grant #7") {
		t.Fatalf("revoking #7 again: err = %v, want it to name the id", err)
	}
}

func TestRevokeAllSaysHowMany(t *testing.T) {
	path := grantDB(t, twoGrants...)
	if out := runOK(t, "revoke", "--all", "--db", path); strings.TrimSpace(out) != "revoked 2 grants" {
		t.Fatalf("revoke --all output %q", out)
	}
	if out := runOK(t, "grants", "--db", path); strings.TrimSpace(out) != "no session grants" {
		t.Fatalf("grants after revoke --all = %q", out)
	}
}

func TestRevokeNeedsOneIDOrAll(t *testing.T) {
	path := grantDB(t, twoGrants...)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"revoke", "--db", path}, "give one grant id"},
		{[]string{"revoke", "--db", path, "x"}, `"x" is not a grant id`},
		{[]string{"revoke", "--all", "--db", path, "7"}, "not both"},
		{[]string{"revoke", "7", "--db", path}, "flags go before the id"},
	} {
		err := run(t.Context(), tc.args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	missing := filepath.Join(t.TempDir(), "typo", "p.db")
	if err := run(t.Context(), []string{"revoke", "--db", missing, "7"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("revoke on a missing database succeeded")
	}
	if _, err := os.Stat(filepath.Dir(missing)); err == nil {
		t.Fatal("revoke created a database directory")
	}
}

// The hook reads the grants table on every call, so a grant revoked from a shell stops covering the
// CLI session's calls at once: the next one asks again.
func TestRevokingAGrantMakesTheNextHookCallAsk(t *testing.T) {
	dir := t.TempDir()
	cfg := strings.Replace(hookConfig, `timeout = "20s"`, `timeout = "5s"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "p.db")
	done := make(chan string, 1)
	go func() {
		out, _, _ := runHook(t, dir, claudeHookInput(dir, "git push origin main"), "--agent", "claude")
		done <- out
	}()
	id := awaitHookApproval(t, dir)
	runOK(t, "approve", "--session", "--db", db, fmt.Sprint(id))
	select {
	case out := <-done:
		if !strings.Contains(out, `"permissionDecision":"allow"`) {
			t.Fatalf("approved call answered %q", out)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the approved hook call did not return")
	}
	if out, _, _ := runHook(t, dir, claudeHookInput(dir, "git push --tags"), "--agent", "claude"); !strings.Contains(out, `"permissionDecision":"allow"`) {
		t.Fatalf("the grant did not cover the next push: %q", out)
	}
	want := fmt.Sprintf("revoked #%d: native__Bash for claude in session sess-1", id)
	if out := runOK(t, "revoke", "--db", db, fmt.Sprint(id)); !strings.Contains(out, want) {
		t.Fatalf("revoke output %q, want %q", out, want)
	}
	out, _, _ := runHook(t, dir, claudeHookInput(dir, "git push --tags"), "--agent", "claude")
	if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "none came within 5s") {
		t.Fatalf("the push after the revoke answered %q, want it asked about again and timed out", out)
	}
	var by []string
	for _, r := range hookReceiptRows(t, dir) {
		by = append(by, r.decidedBy)
	}
	if len(by) != 3 || by[0] != fmt.Sprintf("user:%d", id) || by[1] != fmt.Sprintf("grant:%d", id) || !strings.HasPrefix(by[2], "timeout:") {
		t.Fatalf("decided_by = %v", by)
	}
}
