package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/tunahanaliozturk/derbent/internal/memory"
)

// browser is the memory browser: a search box, the hits, and one note read in full.
type browser struct {
	query  string
	typing bool
	hits   []memory.Hit
	cursor int
	entry  *memory.Entry
}

type (
	hitsMsg struct {
		hits []memory.Hit
		err  error
	}
	entryMsg struct {
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
			return m, m.search(b.query)
		case "esc":
			m.notes = nil
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
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	default:
		switch k.String() {
		case "esc":
			m.notes = nil
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/":
			b.typing = true
		case "up":
			b.cursor = max(b.cursor-1, 0)
		case "down":
			b.cursor = min(b.cursor+1, max(len(b.hits)-1, 0))
		case "enter":
			if len(b.hits) > 0 {
				return m, m.read(b.hits[b.cursor].ID)
			}
		}
	}
	return m, nil
}

// search looks in every project: the UI belongs to no project.
func (m Model) search(query string) tea.Cmd {
	return func() tea.Msg {
		hits, err := m.memory.Search(m.ctx, "", query, 50, true)
		return hitsMsg{hits: hits, err: err}
	}
}

func (m Model) read(id int64) tea.Cmd {
	return func() tea.Msg {
		e, err := m.memory.Read(m.ctx, id)
		return entryMsg{entry: e, err: err}
	}
}

// browsed takes in a search or read result, if the browser is still open.
func (m Model) browsed(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.notes == nil {
		return m, nil
	}
	b := *m.notes
	m.notes = &b
	switch msg := msg.(type) {
	case hitsMsg:
		if msg.err != nil {
			m.status = "memory: " + msg.err.Error()
			return m, nil
		}
		b.hits, b.cursor = msg.hits, 0
		m.status = fmt.Sprintf("%d notes match", len(msg.hits))
	case entryMsg:
		if msg.err != nil {
			m.status = "memory: " + msg.err.Error()
			return m, nil
		}
		b.entry = &msg.entry
	}
	return m, nil
}

// notesView draws the browser. Notes are text other agents wrote, so the screen says so, and each line
// of a note is cleaned before it is drawn.
func (m Model) notesView() string {
	b := m.notes
	l := &lines{width: m.width}
	if e := b.entry; e != nil {
		l.add(titleStyle, clean(e.Title))
		l.add(faintStyle, fmt.Sprintf("#%d  %s  by %s  %s  tags: %s", e.ID, clean(e.Project), clean(e.Author),
			e.At.Local().Format("2006-01-02 15:04"), clean(strings.Join(e.Tags, " "))))
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
			l.add(plainStyle, clean(line))
		}
		return l.String()
	}
	query := clean(b.query)
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
		l.add(style, fmt.Sprintf("#%d  %s  %s  %s  %s  %s", h.ID, h.At.Local().Format("2006-01-02"), clean(h.Author),
			clean(h.Title), clean(h.Snippet), clean(h.Project)))
	}
	l.blank()
	l.add(faintStyle, clean(m.status))
	return l.String()
}
