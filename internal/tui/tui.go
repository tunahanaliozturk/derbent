// Package tui is the terminal UI: calls waiting for the user at the top, a live feed of receipts, the
// agents seen in the last hour, a memory browser and verify. It reads and writes the same database as
// the gates and keeps no state they need, so quitting it changes nothing for running agents: calls
// still waiting are denied at their timeout.
package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

const (
	pollEvery = 200 * time.Millisecond // as often as a waiting call polls (ADR 0001)
	feedSize  = 500                    // receipts the feed keeps
	bell      = "\a"
	// armAfter is how long a call must have been highlighted on screen before a, A or d decides it, so
	// a key meant for the call before it cannot land on it.
	armAfter = 750 * time.Millisecond
	// confirmFor is how long a first A waits for the second that grants the session approval.
	confirmFor = 5 * time.Second
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
	// selected is the ID of the highlighted call, 0 for none. It is an ID, not a position, so that a
	// key never lands on a call the user did not highlight when the list changes under it. since is
	// when it became highlighted or the main screen last came back, which a, A and d wait armAfter from.
	selected int64
	since    time.Time
	// confirm is the call a first A asked about, 0 for none, and confirmAt when; a second A on it
	// within confirmFor approves it for the session.
	confirm   int64
	confirmAt time.Time
	// detail is the call whose whole arguments are open, 0 for none, and scroll the first line shown.
	detail  int64
	scroll  int
	feed    []receipt.Receipt
	lastSeq int64
	agents  []receipt.AgentSeen
	status  string // the outcome of the last action
	pollErr string // why the last poll failed, until one succeeds
	filter  string
	editing bool // typing a filter
	help    bool
	notes   *browser // the memory browser while it is open
	lookups int      // memory searches and reads sent; each result carries its number
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
	case hitsMsg, entryMsg:
		return m.browsed(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// apply takes in a snapshot, rings the bell when a call has started waiting since the last one, and
// schedules the next poll. Only one load is ever in flight: the next is scheduled here.
//
// When the highlighted call leaves (decided elsewhere, expired, its gate gone) nothing is highlighted
// until the user picks a call again. The first call is highlighted only when nothing was waiting
// before, so a single call takes one key.
func (m Model) apply(s snapshotMsg) (tea.Model, tea.Cmd) {
	next := tea.Tick(m.poll, func(time.Time) tea.Msg { return tickMsg{} })
	if s.err != nil {
		m.pollErr = s.err.Error()
		return m, next
	}
	m.pollErr = ""
	known := make(map[int64]bool, len(m.pending))
	for _, p := range m.pending {
		known[p.ID] = true
	}
	ring := false
	for _, p := range s.pending {
		ring = ring || !known[p.ID]
	}
	wasEmpty := len(m.pending) == 0
	m.pending = s.pending
	switch {
	case m.selected != 0 && m.index() < 0:
		m.status = fmt.Sprintf("#%d is no longer waiting", m.selected)
		m = m.highlight(0)
	case m.selected == 0 && wasEmpty && len(m.pending) > 0:
		m = m.highlight(m.pending[0].ID)
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

// index is the position of the highlighted call in pending, or -1 when none is highlighted.
func (m Model) index() int {
	return slices.IndexFunc(m.pending, func(p approval.Pending) bool { return p.ID == m.selected })
}

// highlight makes id the highlighted call, 0 for none. A call newly highlighted starts its arming
// time, and a first A asked about another call is forgotten.
func (m Model) highlight(id int64) Model {
	if id != m.selected {
		m.selected, m.since, m.confirm = id, m.now(), 0
	}
	return m
}

// backToMain closes the help, the detail view and the memory browser. The highlighted call may have
// been highlighted while one of them hid it, so its arming time starts again now that it is on screen.
func (m Model) backToMain() Model {
	m.help, m.detail, m.notes, m.since = false, 0, nil, m.now()
	return m
}

// verdicts are the keys that decide the highlighted call.
var verdicts = map[string]approval.Verdict{"a": approval.ApproveOnce, "A": approval.ApproveSession, "d": approval.Deny}

// verdict is the decision key k stands for, if any. An a typed with Caps Lock on, from a terminal that
// reports modifiers, is an a: Caps Lock without Shift does not make it an A.
func verdict(k tea.KeyPressMsg) (approval.Verdict, bool) {
	name := k.String()
	if k.Code == 'a' && k.Mod.Contains(tea.ModCapsLock) && !k.Mod.Contains(tea.ModShift) {
		name = "a"
	}
	v, ok := verdicts[name]
	return v, ok
}

// key handles a key. Any key but a second A forgets a first A.
func (m Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	asked := m.confirm
	m.confirm = 0
	switch {
	case k.String() == "ctrl+c":
		return m, tea.Quit
	case m.notes != nil:
		return m.browseKey(k)
	case m.editing:
		return m.editFilter(k), nil
	case m.help:
		return m.backToMain(), nil
	case m.detail != 0:
		return m.detailKey(k, asked)
	}
	if v, ok := verdict(k); ok {
		return m.decide(v, asked)
	}
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case "/":
		m.editing = true
	case "m":
		m.notes = &browser{typing: true}
	case "esc":
		m.filter = ""
	case "enter":
		if m.index() >= 0 {
			m.detail, m.scroll = m.selected, 0
		}
	case "up":
		if len(m.pending) > 0 {
			m = m.highlight(m.pending[max(m.index()-1, 0)].ID)
		}
	case "down":
		if len(m.pending) > 0 {
			m = m.highlight(m.pending[min(m.index()+1, len(m.pending)-1)].ID)
		}
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

// decide sends the user's verdict on the highlighted call, and on no other. A call highlighted less
// than armAfter ago takes no verdict yet, and a session approval needs a second A within confirmFor of
// the first; asked is the call the first A asked about.
func (m Model) decide(v approval.Verdict, asked int64) (tea.Model, tea.Cmd) {
	i := m.index()
	switch {
	case len(m.pending) == 0:
		m.status = "no call is waiting"
		return m, nil
	case i < 0:
		m.status = "pick a waiting call with up or down first"
		return m, nil
	case m.now().Sub(m.since) < armAfter:
		m.status = fmt.Sprintf("#%d just appeared; press again to decide", m.selected)
		return m, nil
	}
	p := m.pending[i]
	if v == approval.ApproveSession && (asked != p.ID || m.now().Sub(m.confirmAt) > confirmFor) {
		m.confirm, m.confirmAt = p.ID, m.now()
		m.status = "press A again to approve " + sessionScope(p)
		return m, nil
	}
	// The call is about to leave the list; that is no news to report.
	m = m.highlight(0)
	m.detail = 0
	return m, func() tea.Msg {
		if err := m.approvals.Decide(m.ctx, p.ID, v); err != nil {
			return statusMsg(fmt.Sprintf("#%d: %v", p.ID, err))
		}
		if v == approval.ApproveSession {
			return statusMsg(fmt.Sprintf("#%d approved %s", p.ID, sessionScope(p)))
		}
		if v == approval.ApproveOnce {
			return statusMsg(fmt.Sprintf("#%d approved once: %s %s", p.ID, p.Agent, p.Tool))
		}
		// approval.Deny, the only other verdict a key gives
		return statusMsg(fmt.Sprintf("#%d denied: %s %s", p.ID, p.Agent, p.Tool))
	}
}

// sessionScope is what A approves for p: the tool's calls that the rule which asked holds, under the
// same rules above it, for the rest of the agent's session. An approval with no rule key grants nothing
// (ADR 0011), so A approves it once. It has none when its rule matched on an argument it could not read,
// or when a gate from before grants followed the rule, still running after the upgrade, asked.
func sessionScope(p approval.Pending) string {
	if p.RuleKey == "" {
		return fmt.Sprintf("%s's %s call once; this call cannot be approved for the session", p.Agent, p.Tool)
	}
	return fmt.Sprintf("%s calls that rule %d asks about, for the rest of %s's session", p.Tool, p.Rule, p.Agent)
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
	var content string
	switch {
	case m.help:
		l := &lines{width: m.width}
		for _, s := range strings.Split(helpText, "\n") {
			l.add(plainStyle, s)
		}
		content = l.String()
	case m.notes != nil:
		content = m.notesView()
	case m.detail != 0:
		content = m.detailView()
	default:
		content = m.main()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "derbent"
	return v
}

const helpText = `derbent keys

  a          approve the selected call once
  A, A       approve this tool's calls that the same rule asks about, under
             the same rules above it, for the rest of that agent's session
  d          deny the selected call
  enter      read the selected call's whole arguments; esc goes back
  up, down   select a waiting call
  m          search and read memory
  v          verify the receipt chain
  /          filter the receipt feed by agent or tool; esc clears it
  ?          this help; any key closes it
  q          quit; calls still waiting are denied at their timeout

A newly selected call takes a, A or d after it has been on screen for
a moment, and A needs a second press within five seconds.
Nothing here changes the config file.`

// lines collects screen lines, each clipped to the window before it is styled. Every piece of text
// from outside Derbent goes through visible.Escape first.
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

// main draws the header, the waiting calls, the agents, the receipt feed and the status line.
func (m Model) main() string {
	l := &lines{width: m.width}
	l.add(titleStyle, fmt.Sprintf("derbent   %d waiting   ? keys", len(m.pending)))
	l.blank()
	if len(m.pending) > 0 {
		m.drawWaiting(l)
		l.blank()
	}
	agents := make([]string, 0, len(m.agents))
	for _, a := range m.agents {
		agents = append(agents, visible.Escape(a.Agent)+" "+a.Last.Local().Format("15:04"))
	}
	seen := strings.Join(agents, ", ")
	if seen == "" {
		seen = "none"
	}
	l.add(plainStyle, "agents in the last hour: "+seen)
	l.blank()
	title := "RECEIPTS"
	if m.filter != "" || m.editing {
		title += "   filter: " + visible.Escape(m.filter)
		if m.editing {
			title += "_"
		}
	}
	l.add(titleStyle, title)
	rows := m.visibleFeed()
	status := m.statusLines()
	room := max(m.height-len(l.out)-1-len(status), 0) // a blank line and the status lines stay
	if len(rows) > room {
		rows = rows[len(rows)-room:]
	}
	for _, r := range rows {
		style := plainStyle
		if r.Decision == "deny" {
			style = denyStyle
		}
		l.add(style, fmt.Sprintf("%s  %-10s %-34s %-5s %-14s %-8s %6dms", r.At.Local().Format("15:04:05"),
			visible.Escape(r.Agent), visible.Escape(r.Tool), visible.Escape(r.Decision), visible.Escape(r.DecidedBy),
			visible.Escape(r.Outcome), r.Duration.Milliseconds()))
	}
	l.blank()
	for _, s := range status {
		l.add(faintStyle, visible.Escape(s))
	}
	return l.String()
}

// statusLines is the bottom of the screen: the outcome of the user's last action and, while polls
// fail, why on a line of its own above it, so a failing poll never hides what the user's key did.
func (m Model) statusLines() []string {
	if m.pollErr == "" {
		return []string{m.status}
	}
	return []string{"error: " + m.pollErr, m.status}
}

// drawWaiting draws the calls waiting for the user, one line each, with the highlighted call's
// arguments wrapped below it. When the window is too short for all of them it draws the rows around
// the highlighted one and a count of the rest, so the highlighted call and the status line stay on
// screen. After a decision nothing is highlighted, and the header says so.
func (m Model) drawWaiting(l *lines) {
	var args []string
	i := m.index()
	if i >= 0 {
		l.add(pendingStyle, "WAITING FOR YOU   a approve   A twice for this session   d deny   enter read all")
		args = m.argLines(m.pending[i].Args)
	} else {
		l.add(pendingStyle, "WAITING FOR YOU   nothing highlighted   up, down pick a call")
	}
	// What is drawn below the rows: the arguments, the count of hidden calls, a blank line, the
	// agents, a blank line, the feed title, a blank line and the status lines.
	room := max(m.height-len(l.out)-len(args)-6-len(m.statusLines()), 1)
	rows := m.pending
	if len(rows) > room {
		start := min(max(i-room/2, 0), len(rows)-room)
		rows = rows[start : start+room]
	}
	for _, p := range rows {
		if p.ID != m.selected {
			text, _ := argText(p.Args, 4*m.width)
			l.add(plainStyle, m.callLine(p)+"  "+text)
			continue
		}
		l.add(selectedStyle, m.callLine(p))
		for _, a := range args {
			l.add(plainStyle, a)
		}
	}
	if hidden := len(m.pending) - len(rows); hidden > 0 {
		l.add(faintStyle, fmt.Sprintf("+%d more waiting", hidden))
	}
}

// callLine is a waiting call's row: its id, agent, tool, time left, the rule that asked and its project.
func (m Model) callLine(p approval.Pending) string {
	left := max(p.Deadline.Sub(m.now()), 0).Round(time.Second)
	return fmt.Sprintf("#%d  %s  %s  %s left  rule %d  %s", p.ID, visible.Escape(p.Agent), visible.Escape(p.Tool), left, p.Rule,
		visible.Escape(p.Project))
}

// argLines wraps the highlighted call's arguments, which the agent controls, over about a third of the
// window and at least three lines, and says how to read the rest in the detail view. Escaping and
// wrapping megabytes on every frame would stall the UI, so it wraps only what it can show and a line
// more, allowing up to four bytes a cell. When that leaves some of the arguments unread, the count of
// lines left is an estimate at one byte a cell. lines.add clips each line again.
func (m Model) argLines(args string) []string {
	const indent = "    "
	width, most := max(m.width-len(indent), 1), max(m.height/3, 3)
	text, used := argText(args, (most+1)*width*4)
	out := strings.Split(ansi.Hardwrap(text, width, true), "\n")
	hidden := max(len(out)-most, 0)
	out = out[:len(out)-hidden]
	switch {
	case used < len(args):
		hidden += (len(args) - used + width - 1) / width
		out = append(out, fmt.Sprintf("+about %d more lines, enter to read all", hidden))
	case hidden > 0:
		out = append(out, fmt.Sprintf("+%d more lines, enter to read all", hidden))
	}
	for i := range out {
		out[i] = indent + out[i]
	}
	return out
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
