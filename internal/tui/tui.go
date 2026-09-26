// Package tui is the terminal UI: calls waiting for the user at the top, a live feed of receipts, the
// agents seen in the last hour, a memory browser and verify. It reads and writes the same database as
// the gates and keeps no state they need, so quitting it changes nothing for running agents: calls
// still waiting are denied at their timeout.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
)

const (
	pollEvery = 200 * time.Millisecond // as often as a waiting call polls (ADR 0001)
	feedSize  = 500                    // receipts the feed keeps
	bell      = "\a"
)

var (
	plainStyle    = lipgloss.NewStyle()
	titleStyle    = lipgloss.NewStyle().Bold(true)
	pendingStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	denyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	faintStyle    = lipgloss.NewStyle().Faint(true)
)

// Model is the UI. Build it with New and run it with tea.NewProgram.
type Model struct {
	ctx       context.Context
	approvals *approval.Queue
	receipts  *receipt.Log
	memory    *memory.Store
	now       func() time.Time
	poll      time.Duration

	width, height int
	pending       []approval.Pending
	selected      int // index into pending
	feed          []receipt.Receipt
	lastSeq       int64
	agents        []receipt.AgentSeen
	status        string // the outcome of the last action
	filter        string
	editing       bool // typing a filter
	help          bool
}

// New returns the UI over the shared database's approvals, receipts and memory.
func New(ctx context.Context, q *approval.Queue, l *receipt.Log, m *memory.Store) Model {
	return Model{ctx: ctx, approvals: q, receipts: l, memory: m, now: time.Now, poll: pollEvery, width: 100, height: 30}
}

type (
	tickMsg     struct{}
	statusMsg   string
	snapshotMsg struct {
		pending []approval.Pending
		feed    []receipt.Receipt
		agents  []receipt.AgentSeen
		err     error
	}
)

// Init loads the first snapshot.
func (m Model) Init() tea.Cmd {
	return m.load
}

// load reads the waiting calls, the receipts after the last one shown, and the agents seen in the last
// hour. The program runs it off the UI goroutine.
func (m Model) load() tea.Msg {
	pending, err := m.approvals.Pending(m.ctx)
	if err != nil {
		return snapshotMsg{err: err}
	}
	feed, err := m.receipts.List(m.ctx, receipt.Filter{AfterSeq: m.lastSeq, Limit: feedSize})
	if err != nil {
		return snapshotMsg{err: err}
	}
	agents, err := m.receipts.Agents(m.ctx, m.now().Add(-time.Hour))
	if err != nil {
		return snapshotMsg{err: err}
	}
	return snapshotMsg{pending: pending, feed: feed, agents: agents}
}

// Update handles one message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, m.load
	case snapshotMsg:
		return m.apply(msg)
	case statusMsg:
		m.status = string(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// apply takes in a snapshot, rings the bell when a call has started waiting since the last one, and
// schedules the next poll. Only one load is ever in flight: the next is scheduled here.
func (m Model) apply(s snapshotMsg) (tea.Model, tea.Cmd) {
	next := tea.Tick(m.poll, func(time.Time) tea.Msg { return tickMsg{} })
	if s.err != nil {
		m.status = "error: " + s.err.Error()
		return m, next
	}
	known := make(map[int64]bool, len(m.pending))
	for _, p := range m.pending {
		known[p.ID] = true
	}
	ring := false
	for _, p := range s.pending {
		ring = ring || !known[p.ID]
	}
	var keep int64
	if m.selected < len(m.pending) {
		keep = m.pending[m.selected].ID
	}
	m.pending, m.selected = s.pending, 0
	for i, p := range m.pending {
		if p.ID == keep {
			m.selected = i
		}
	}
	m.feed = append(m.feed, s.feed...)
	if over := len(m.feed) - feedSize; over > 0 {
		m.feed = m.feed[over:]
	}
	if n := len(s.feed); n > 0 {
		m.lastSeq = s.feed[n-1].Seq
	}
	m.agents = s.agents
	if ring {
		return m, tea.Batch(next, tea.Raw(bell))
	}
	return m, next
}

// key handles a key on the main screen.
func (m Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.editing:
		return m.editFilter(k), nil
	case m.help:
		m.help = false
		return m, nil
	}
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "/":
		m.editing = true
	case "esc":
		m.filter = ""
	case "up":
		m.selected = max(m.selected-1, 0)
	case "down":
		m.selected = min(m.selected+1, max(len(m.pending)-1, 0))
	case "a":
		return m, m.decide(approval.ApproveOnce)
	case "A":
		return m, m.decide(approval.ApproveSession)
	case "d":
		return m, m.decide(approval.Deny)
	case "v":
		m.status = "verifying the receipt chain..."
		return m, m.verify
	}
	return m, nil
}

// editFilter handles a key while the user types a feed filter.
func (m Model) editFilter(k tea.KeyPressMsg) Model {
	switch k.String() {
	case "enter":
		m.editing = false
	case "esc":
		m.editing, m.filter = false, ""
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	default:
		m.filter += k.Text
	}
	return m
}

// decide sends the user's verdict on the selected call.
func (m Model) decide(v approval.Verdict) tea.Cmd {
	if len(m.pending) == 0 {
		return nil
	}
	p := m.pending[m.selected]
	return func() tea.Msg {
		if err := m.approvals.Decide(m.ctx, p.ID, v); err != nil {
			return statusMsg(fmt.Sprintf("#%d: %v", p.ID, err))
		}
		return statusMsg(fmt.Sprintf("#%d %s: %s %s", p.ID, verdictText(v), p.Agent, p.Tool))
	}
}

func verdictText(v approval.Verdict) string {
	switch v {
	case approval.ApproveOnce:
		return "approved once"
	case approval.ApproveSession:
		return "approved for the rest of the session"
	case approval.Deny:
		return "denied"
	}
	return string(v)
}

// verify walks the receipt chain off the UI goroutine.
func (m Model) verify() tea.Msg {
	res, err := m.receipts.Verify(m.ctx)
	switch {
	case err != nil:
		return statusMsg("verify: " + err.Error())
	case res.FirstBad != 0:
		return statusMsg(fmt.Sprintf("verify: the chain is broken at receipt %d: %s", res.FirstBad, res.Reason))
	default:
		return statusMsg(fmt.Sprintf("verify: %d receipts, chain intact, head %s", res.Count, res.Head))
	}
}

// View draws the screen.
func (m Model) View() tea.View {
	content := m.main()
	if m.help {
		content = helpText
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "derbent"
	return v
}

const helpText = `derbent keys

  a          approve the selected call once
  A          approve this tool for the rest of that agent's session
  d          deny the selected call
  up, down   select a waiting call
  m          search and read memory
  v          verify the receipt chain
  /          filter the receipt feed by agent or tool; esc clears it
  ?          this help; any key closes it
  q          quit; calls still waiting are denied at their timeout

Nothing here changes the config file.`

// lines collects screen lines, each clipped to the window before it is styled. Every piece of text
// from outside Derbent goes through clean first.
type lines struct {
	width int
	out   []string
}

func (l *lines) add(style lipgloss.Style, s string) {
	l.out = append(l.out, style.Render(clip(s, l.width)))
}

func (l *lines) blank() {
	l.out = append(l.out, "")
}

func (l *lines) String() string {
	return strings.Join(l.out, "\n")
}

// main draws the header, the waiting calls, the receipt feed and the status line.
func (m Model) main() string {
	l := &lines{width: m.width}
	agents := make([]string, 0, len(m.agents))
	for _, a := range m.agents {
		agents = append(agents, clean(a.Agent)+" "+a.Last.Local().Format("15:04"))
	}
	seen := strings.Join(agents, ", ")
	if seen == "" {
		seen = "none"
	}
	l.add(titleStyle, fmt.Sprintf("derbent   %d waiting   agents in the last hour: %s   ? keys", len(m.pending), seen))
	l.blank()
	if len(m.pending) > 0 {
		l.add(pendingStyle, "WAITING FOR YOU   a approve   A approve for this session   d deny")
		for i, p := range m.pending {
			left := max(p.Deadline.Sub(m.now()), 0).Round(time.Second)
			style := plainStyle
			if i == m.selected {
				style = selectedStyle
			}
			l.add(style, fmt.Sprintf("#%d  %s  %s  %s left  %s", p.ID, clean(p.Agent), clean(p.Tool), left, clean(p.Args)))
		}
		l.blank()
	}
	title := "RECEIPTS"
	if m.filter != "" || m.editing {
		title += "   filter: " + clean(m.filter)
		if m.editing {
			title += "_"
		}
	}
	l.add(titleStyle, title)
	rows := m.visibleFeed()
	room := max(m.height-len(l.out)-2, 0) // a blank line and the status line stay
	if len(rows) > room {
		rows = rows[len(rows)-room:]
	}
	for _, r := range rows {
		style := plainStyle
		if r.Decision == "deny" {
			style = denyStyle
		}
		l.add(style, fmt.Sprintf("%s  %-10s %-34s %-5s %-14s %-8s %6dms", r.At.Local().Format("15:04:05"),
			clean(r.Agent), clean(r.Tool), clean(r.Decision), clean(r.DecidedBy), clean(r.Outcome), r.Duration.Milliseconds()))
	}
	l.blank()
	l.add(faintStyle, clean(m.status))
	return l.String()
}

// visibleFeed is the feed narrowed by the filter, which matches agent or tool names, ignoring case.
func (m Model) visibleFeed() []receipt.Receipt {
	if m.filter == "" {
		return m.feed
	}
	f := strings.ToLower(m.filter)
	var out []receipt.Receipt
	for _, r := range m.feed {
		if strings.Contains(strings.ToLower(r.Agent), f) || strings.Contains(strings.ToLower(r.Tool), f) {
			out = append(out, r)
		}
	}
	return out
}
