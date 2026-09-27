package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// grantList is the grants screen: every session grant, one of them highlighted.
type grantList struct {
	list   []approval.Grant
	cursor int
}

// grantsMsg carries the session grants as read, and the outcome of a revoke that ran before the read.
type grantsMsg struct {
	list   []approval.Grant
	err    error
	status string
}

// loadGrants reads the session grants off the UI goroutine.
func (m Model) loadGrants() tea.Msg {
	list, err := m.approvals.Grants(m.ctx)
	return grantsMsg{list: list, err: err}
}

// grantsLoaded takes in the grants while the screen is open and keeps the highlight in range.
func (m Model) grantsLoaded(msg grantsMsg) (tea.Model, tea.Cmd) {
	if msg.status != "" {
		m.status = msg.status
	}
	if m.grants == nil {
		return m, nil
	}
	if msg.err != nil {
		m.status = "grants: " + msg.err.Error()
		return m, nil
	}
	g := *m.grants
	g.list = msg.list
	g.cursor = min(g.cursor, max(len(g.list)-1, 0))
	m.grants = &g
	return m, nil
}

// grantsKey handles a key on the grants screen. r revokes the highlighted grant on a second press
// within confirmFor of the first, as A approves for a session; asked is the grant a first r asked
// about. The list is copied before it changes, so a model the program has already drawn is never
// altered.
func (m Model) grantsKey(k tea.KeyPressMsg, asked int64) (tea.Model, tea.Cmd) {
	g := *m.grants
	m.grants = &g
	switch k.String() {
	case "esc":
		return m.backToMain(), nil
	case "q":
		return m, tea.Quit
	case "up":
		g.cursor = max(g.cursor-1, 0)
	case "down":
		g.cursor = min(g.cursor+1, max(len(g.list)-1, 0))
	case "r":
		if len(g.list) == 0 {
			m.status = "no session grant to revoke"
			return m, nil
		}
		gr := g.list[g.cursor]
		if asked != gr.ID || m.now().Sub(m.confirmAt) > confirmFor {
			m.confirm, m.confirmAt = gr.ID, m.now()
			m.status = "press r again to revoke " + grantText(gr)
			return m, nil
		}
		return m, m.revokeGrant(gr)
	}
	return m, nil
}

// revokeGrant deletes gr off the UI goroutine and reads the grants again, so the screen shows what is
// left.
func (m Model) revokeGrant(gr approval.Grant) tea.Cmd {
	return func() tea.Msg {
		status := "revoked " + grantText(gr)
		if _, err := m.approvals.Revoke(m.ctx, gr.ID); err != nil {
			status = fmt.Sprintf("#%d: %v", gr.ID, err)
		}
		list, err := m.approvals.Grants(m.ctx)
		return grantsMsg{list: list, err: err, status: status}
	}
}

// grantText names a grant in the status line, which escapes it when it is drawn.
func grantText(gr approval.Grant) string {
	return fmt.Sprintf("#%d: %s for %s in session %s", gr.ID, gr.Tool, gr.Agent, gr.Session)
}

// grantRow is a grant's row on the grants screen. Agent, session and tool are stored text from agents,
// so each is escaped.
func grantRow(gr approval.Grant) string {
	return fmt.Sprintf("#%d  %s  %s  %s  rule %d  granted %s", gr.ID, visible.Escape(gr.Agent), visible.Escape(gr.Session),
		visible.Escape(gr.Tool), gr.Rule, gr.Granted.Local().Format("2006-01-02 15:04:05"))
}

// grantsView draws the grants screen. Calls waiting for the user and a failing poll head it, as they
// head the memory browser. When the list is longer than the window it draws the rows around the
// highlighted one.
func (m Model) grantsView() string {
	g := m.grants
	l := &lines{width: m.width}
	if n := len(m.pending); n > 0 {
		l.add(pendingStyle, fmt.Sprintf("%d waiting, esc back to the main screen to decide", n))
	}
	if m.pollErr != "" {
		l.add(faintStyle, visible.Escape("error: "+m.pollErr))
	}
	l.add(titleStyle, fmt.Sprintf("SESSION GRANTS   %d", len(g.list)))
	l.add(faintStyle, "up, down select   r twice revoke   esc back")
	l.blank()
	if len(g.list) == 0 {
		l.add(plainStyle, "no session grants")
	}
	rows, start := g.list, 0
	if room := max(m.height-len(l.out)-2, 1); len(rows) > room { // a blank line and the status line stay
		start = min(max(g.cursor-room/2, 0), len(rows)-room)
		rows = rows[start : start+room]
	}
	for i, gr := range rows {
		style := plainStyle
		if start+i == g.cursor {
			style = selectedStyle
		}
		l.add(style, grantRow(gr))
	}
	l.blank()
	l.add(faintStyle, visible.Escape(m.status))
	l.fit(m.height, 1)
	return l.String()
}
