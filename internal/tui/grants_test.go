package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/approval"
)

// grantOf asks r, approves it for the session as A does, and returns the approval's id.
func grantOf(t *testing.T, q *approval.Queue, r approval.Request) int64 {
	t.Helper()
	done := waiting(t, q, r)
	waitPending(t, q, 1)
	p, err := q.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Decide(t.Context(), p[0].ID, approval.ApproveSession); err != nil {
		t.Fatal(err)
	}
	<-done
	return p[0].ID
}

func TestGListsTheSessionGrants(t *testing.T) {
	m, d := newModel(t)
	id := grantOf(t, d.q, askReq)
	m, cmd := press(m, "g")
	m = settle(m, cmd)
	s := screen(m)
	for _, want := range []string{"SESSION GRANTS", fmt.Sprintf("#%d", id), "codex", "s1", "github__create_issue", "rule 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("grants screen lacks %q:\n%s", want, s)
		}
	}
	m, _ = press(m, "esc")
	if strings.Contains(screen(m), "SESSION GRANTS") {
		t.Fatalf("esc did not leave the grants screen:\n%s", screen(m))
	}
}

func TestRevokeNeedsASecondR(t *testing.T) {
	for _, tc := range []struct {
		name    string
		steps   []any // a key name, or a time.Duration to move the clock by
		revoked bool
	}{
		{"once", []any{"r"}, false},
		{"twice", []any{"r", "r"}, true},
		{"another key between", []any{"r", "down", "r"}, false},
		{"more than five seconds apart", []any{"r", confirmFor + time.Millisecond, "r"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d := newModel(t)
			id := grantOf(t, d.q, askReq)
			m, cmd := press(m, "g")
			m = settle(m, cmd)
			for _, step := range tc.steps {
				switch s := step.(type) {
				case string:
					m, cmd = press(m, s)
				case time.Duration:
					m, cmd = later(m, s), nil
				}
				m = settle(m, cmd)
			}
			_, granted, err := d.q.Granted(t.Context(), "codex", "s1", askReq.Tool, askReq.RuleKey)
			if err != nil || granted == tc.revoked {
				t.Fatalf("granted = %v, err %v; want revoked %v", granted, err, tc.revoked)
			}
			scope := fmt.Sprintf("#%d: github__create_issue for codex in session s1", id)
			want := "press r again to revoke " + scope
			if tc.revoked {
				want = "revoked " + scope
			}
			if !strings.Contains(screen(m), want) {
				t.Fatalf("screen lacks %q:\n%s", want, screen(m))
			}
			if tc.revoked && !strings.Contains(screen(m), "no session grants") {
				t.Fatalf("the revoked grant is still listed:\n%s", screen(m))
			}
		})
	}
}

func TestTheGrantsScreenEscapesStoredText(t *testing.T) {
	m, d := newModel(t)
	r := askReq
	r.Session = "s\x1b]0;x\x07\u202e" + sneaky
	r.Tool = "t\x1b[2J\u009b"
	grantOf(t, d.q, r)
	m, cmd := press(m, "g")
	m = settle(m, cmd)
	noRawText(t, screen(m), 140)
	m, _ = press(m, "r")
	noRawText(t, screen(m), 140)
}

func TestHelpListsTheGrantsKey(t *testing.T) {
	m, _ := newModel(t)
	m, _ = press(m, "?")
	if !strings.Contains(screen(m), "list session grants") {
		t.Fatalf("help lacks the g key:\n%s", screen(m))
	}
}
