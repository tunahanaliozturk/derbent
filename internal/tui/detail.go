package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// detailCall is the call the detail view shows, and false once it has stopped waiting. While it waits
// it is the highlighted call: nothing else changes the highlight while the view is open.
func (m Model) detailCall() (approval.Pending, bool) {
	if i := m.index(); i >= 0 && m.selected == m.detail {
		return m.pending[i], true
	}
	return approval.Pending{}, false
}

// detailKey handles a key while a call's whole arguments are open: the scroll keys, and a, A and d on
// that call under the same rules as the main screen. Once the call has stopped waiting only esc and q
// do anything. asked is the call a first A asked about.
func (m Model) detailKey(k tea.KeyPressMsg, asked int64) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.detail = 0
		return m, nil
	case "q":
		return m, tea.Quit
	}
	p, waiting := m.detailCall()
	if !waiting {
		return m, nil
	}
	if v, ok := verdicts[k.String()]; ok {
		return m.decide(v, asked)
	}
	page := m.detailRoom()
	last := max(len(m.detailLines(p))-page, 0)
	switch k.String() {
	case "up":
		m.scroll--
	case "down":
		m.scroll++
	case "pgup":
		m.scroll -= page
	case "pgdown":
		m.scroll += page
	case "home":
		m.scroll = 0
	case "end":
		m.scroll = last
	default:
		return m, nil
	}
	m.scroll = min(max(m.scroll, 0), last)
	return m, nil
}

// detailView draws the call the detail view shows, with its whole arguments wrapped to the window from
// line scroll, or says that the call has stopped waiting.
func (m Model) detailView() string {
	l := &lines{width: m.width}
	p, waiting := m.detailCall()
	if !waiting {
		l.add(pendingStyle, fmt.Sprintf("#%d is no longer waiting   esc back", m.detail))
	} else {
		left := max(p.Deadline.Sub(m.now()), 0).Round(time.Second)
		l.add(selectedStyle, fmt.Sprintf("#%d  %s  %s  %s left", p.ID, visible.Escape(p.Agent), visible.Escape(p.Tool), left))
		l.add(pendingStyle, "a approve   A twice for this session   d deny   esc back")
		args := m.detailLines(p)
		room := m.detailRoom()
		start := min(m.scroll, max(len(args)-room, 0))
		end := min(start+room, len(args))
		l.add(faintStyle, fmt.Sprintf("up, down, pgup, pgdown, home, end scroll   lines %d-%d of %d", start+1, end, len(args)))
		l.blank()
		for _, a := range args[start:end] {
			l.add(plainStyle, a)
		}
	}
	l.blank()
	for _, s := range m.statusLines() {
		l.add(faintStyle, visible.Escape(s))
	}
	return l.String()
}

// detailLines is a call's whole arguments, escaped and wrapped to the window.
func (m Model) detailLines(p approval.Pending) []string {
	text, _ := argText(p.Args, math.MaxInt)
	return strings.Split(ansi.Hardwrap(text, max(m.width, 1), true), "\n")
}

// detailRoom is how many lines of arguments the detail view shows at once: the window less three lines
// above them, a blank line on each side and the status lines.
func (m Model) detailRoom() int {
	return max(m.height-5-len(m.statusLines()), 1)
}

// argText escapes arguments for the preview and the detail view, and writes a run of more than eight
// spaces as ␠×N, so that padding cannot push what follows out of sight. It stops once it has written
// limit bytes or more and returns how many bytes of s it used, so the preview pays for a screenful of
// megabytes of arguments and no more.
func argText(s string, limit int) (string, int) {
	const nine = "         "
	var b strings.Builder
	used := 0
	for used < len(s) && b.Len() < limit {
		seg := head(s[used:], limit-b.Len())
		if i := strings.Index(seg, nine); i >= 0 {
			seg = seg[:i]
		}
		b.WriteString(visible.Escape(seg))
		used += len(seg)
		run := len(s[used:]) - len(strings.TrimLeft(s[used:], " "))
		if run > 8 {
			fmt.Fprintf(&b, "␠×%d", run)
			used += run
		}
	}
	return b.String(), used
}

// head returns at least the first n bytes of s, or all of it, cut at a rune boundary and not inside a
// run of spaces, so that argText counts the whole run.
func head(s string, n int) string {
	n = min(n, len(s))
	for n < len(s) && (s[n] == ' ' || !utf8.RuneStart(s[n])) {
		n++
	}
	return s[:n]
}
