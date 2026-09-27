package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
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
		return m.backToMain(), nil
	case "q":
		return m, tea.Quit
	}
	p, waiting := m.detailCall()
	if !waiting {
		return m, nil
	}
	if v, ok := verdict(k); ok {
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
// blank spaces (see padding) as ␠×N, so that padding cannot push what follows out of sight. It stops once it
// has read or written limit bytes and returns how many bytes of s it used, so the preview pays for a
// screenful of megabytes of arguments and no more, spaces or not. A run of spaces that goes on past
// what it read is written ␠×N+.
func argText(s string, limit int) (string, int) {
	var b strings.Builder
	start, i := 0, 0 // s[start:i] has been read and not yet written
	for i < len(s) && i < limit && b.Len()+i-start < limit {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !padding(r) {
			i += size
			continue
		}
		end, n := i, 0
		for end < len(s) && end < limit {
			r, size := utf8.DecodeRuneInString(s[end:])
			if !padding(r) {
				break
			}
			end, n = end+size, n+1
		}
		if n > 8 {
			b.WriteString(visible.Escape(s[start:i]))
			fmt.Fprintf(&b, "␠×%d", n)
			if r, _ := utf8.DecodeRuneInString(s[end:]); padding(r) {
				b.WriteString("+")
			}
			start = end
		}
		i = end
	}
	b.WriteString(visible.Escape(s[start:i]))
	return b.String(), i
}

// padding reports whether r is a space that draws as a blank: the ASCII space or another space
// separator (category Zs). Control and line-breaking spaces (\r, \v, \f, U+0085, U+2028, U+2029) are
// not padding, so a run of them keeps the escaped form that shows what they are.
func padding(r rune) bool {
	return r == ' ' || unicode.Is(unicode.Zs, r)
}
