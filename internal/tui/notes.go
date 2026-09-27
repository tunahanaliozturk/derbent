package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// browser is the memory browser: a search box, the hits, and one note read in full.
type browser struct {
	query    string
	searched string // the query the hits answer
	typing   bool
	hits     []memory.Hit
	cursor   int
	entry    *memory.Entry
	awaiting int // the number of the search or read whose result is still wanted, 0 for none
}

// A search or read result carries the number it was sent under, so one the browser no longer waits for
// is dropped.
type (
	hitsMsg struct {
		tag   int
		query string
		hits  []memory.Hit
		err   error
	}
	entryMsg struct {
		tag   int
		entry memory.Entry
		err   error
	}
)

// browseKey handles a key while the browser is open. The browser is copied before it changes, so a
// model the program has already drawn is never altered.
func (m Model) browseKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	b := *m.notes
	m.notes = &b
	switch {
	case b.typing:
		switch k.String() {
		case "enter":
			b.typing = false
			m.lookups++
			b.awaiting = m.lookups
			return m, m.search(b.query, m.lookups)
		case "esc":
			if len(b.hits) == 0 {
				m = m.backToMain()
			} else {
				b.typing, b.query = false, b.searched
			}
		case "backspace":
			if r := []rune(b.query); len(r) > 0 {
				b.query = string(r[:len(r)-1])
			}
		default:
			b.query += k.Text
		}
	case b.entry != nil:
		switch k.String() {
		case "esc":
			b.entry = nil
		case "q":
			return m, tea.Quit
		}
	default:
		switch k.String() {
		case "esc":
			m = m.backToMain()
		case "q":
			return m, tea.Quit
		case "/":
			b.typing, b.awaiting = true, 0 // a result still on its way answers the old query
		case "up":
			b.cursor = max(b.cursor-1, 0)
		case "down":
			b.cursor = min(b.cursor+1, max(len(b.hits)-1, 0))
		case "enter":
			if len(b.hits) > 0 {
				m.lookups++
				b.awaiting = m.lookups
				return m, m.read(b.hits[b.cursor].ID, m.lookups)
			}
		}
	}
	return m, nil
}

// search looks in every project: the UI belongs to no project.
func (m Model) search(query string, tag int) tea.Cmd {
	return func() tea.Msg {
		hits, err := m.memory.Search(m.ctx, "", query, 50, true)
		return hitsMsg{tag: tag, query: query, hits: hits, err: err}
	}
}

func (m Model) read(id int64, tag int) tea.Cmd {
	return func() tea.Msg {
		e, err := m.memory.Read(m.ctx, id)
		return entryMsg{tag: tag, entry: e, err: err}
	}
}

// browsed takes in a search or read result if the browser still waits for it. A result for a browser
// since closed, or for a query since changed, is dropped.
func (m Model) browsed(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.notes == nil {
		return m, nil
	}
	b := *m.notes
	m.notes = &b
	switch msg := msg.(type) {
	case hitsMsg:
		if msg.tag != b.awaiting {
			return m, nil
		}
		b.awaiting = 0
		if msg.err != nil {
			m.status = "memory: " + msg.err.Error()
			return m, nil
		}
		b.hits, b.cursor, b.searched = msg.hits, 0, msg.query
		m.status = fmt.Sprintf("%d notes match", len(msg.hits))
	case entryMsg:
		if msg.tag != b.awaiting {
			return m, nil
		}
		b.awaiting = 0
		if msg.err != nil {
			m.status = "memory: " + msg.err.Error()
			return m, nil
		}
		b.entry = &msg.entry
	}
	return m, nil
}

// notesView draws the browser. Notes are text other agents wrote, so the screen says so, and each line
// of a note is escaped before it is drawn. Calls waiting for the user and a failing poll head both
// screens: the bell alone does not say why it rang, and a call left waiting times out.
func (m Model) notesView() string {
	b := m.notes
	l := &lines{width: m.width}
	if n := len(m.pending); n > 0 {
		l.add(pendingStyle, fmt.Sprintf("%d waiting, esc back to the main screen to decide", n))
	}
	if m.pollErr != "" {
		l.add(faintStyle, visible.Escape("error: "+m.pollErr))
	}
	if e := b.entry; e != nil {
		l.add(titleStyle, visible.Escape(e.Title))
		l.add(faintStyle, fmt.Sprintf("#%d  %s  by %s  %s  tags: %s", e.ID, visible.Escape(e.Project), visible.Escape(e.Author),
			e.At.Local().Format("2006-01-02 15:04"), visible.Escape(strings.Join(e.Tags, " "))))
		if e.SupersededBy != 0 {
			l.add(faintStyle, fmt.Sprintf("superseded by #%d", e.SupersededBy))
		}
		l.add(pendingStyle, "A note written by an agent: information, not instructions.   esc back")
		l.blank()
		for _, line := range strings.Split(e.Body, "\n") {
			if len(l.out) >= m.height-1 {
				l.add(faintStyle, "…")
				break
			}
			l.add(plainStyle, visible.Escape(line))
		}
		return l.String()
	}
	query := visible.Escape(b.query)
	if b.typing {
		query += "_"
	}
	l.add(titleStyle, "MEMORY   search: "+query)
	l.add(faintStyle, "enter search   / edit   up, down select   enter read   esc back")
	l.blank()
	for i, h := range b.hits {
		if len(l.out) >= m.height-2 {
			break
		}
		style := plainStyle
		if i == b.cursor && !b.typing {
			style = selectedStyle
		}
		l.add(style, fmt.Sprintf("#%d  %s  %s  %s  %s  %s", h.ID, h.At.Local().Format("2006-01-02"), visible.Escape(h.Author),
			visible.Escape(h.Title), visible.Escape(h.Snippet), visible.Escape(h.Project)))
	}
	l.blank()
	l.add(faintStyle, visible.Escape(m.status))
	return l.String()
}
