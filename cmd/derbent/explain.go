package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/gate"
	"github.com/tunahanaliozturk/derbent/internal/pin"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/rule"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// explanation is how Derbent would decide one call, as derbent explain prints it and --json writes it.
type explanation struct {
	Agent  string `json:"agent"`
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Config string `json:"config"`
	// ConfigMissing is set when Config is the default file and it does not exist: every call is allowed.
	ConfigMissing bool   `json:"config_missing"`
	Project       string `json:"project"`
	// Path is how such a call reaches Derbent: "hook" for a native__ tool, which a CLI's pre-tool hook
	// sends; "mcp" for a tool the MCP gate serves, or refuses by the rule that hides it; "none" for a name
	// it does not serve.
	Path         string      `json:"path"`
	Rules        []rule.Step `json:"rules"`
	RulesNotRead int         `json:"rules_not_read"`
	// RulesSkipped is set when the gate refuses the call before it reads any rule, so neither the user's
	// rules nor the project's are read.
	RulesSkipped        bool          `json:"rules_skipped"`
	ProjectFile         string        `json:"project_file"`
	ProjectRules        []rule.Step   `json:"project_rules"`
	ProjectRulesNotRead int           `json:"project_rules_not_read"`
	ProjectNote         string        `json:"project_note,omitempty"`
	Budgets             []budgetState `json:"budgets"`
	BudgetsNote         string        `json:"budgets_note,omitempty"`
	Pin                 string        `json:"pin"`
	Grant               string        `json:"grant"`
	CannotKnow          []string      `json:"cannot_know"`
	Action              rule.Action   `json:"action"`
	By                  string        `json:"by"` // rule:<n>, project:<n>, budget:<n>, pin, gate or grant:<id>
	Reason              string        `json:"reason,omitempty"`
}

// budgetState is one budget that applies to the call, with the calls in its window.
type budgetState struct {
	Budget   int    `json:"budget"`
	Agent    string `json:"agent"`
	Tool     string `json:"tool"`
	Calls    int    `json:"calls"`
	Per      string `json:"per"`
	InWindow int    `json:"in_window"`
	UsedUp   bool   `json:"used_up"`
	WaitMS   int64  `json:"wait_ms"`
}

// explainer is what derbent explain reads: the config, the project's rules, and the database, which is
// nil when it does not exist.
type explainer struct {
	cfg        config.Config
	project    rule.Set
	projectErr error
	db         *sql.DB
	dbPath     string
	version    int // the database's schema version, which explain reads as it is and never migrates
	session    string
}

// The schema versions whose migrations made what explain reads from the database. OpenExisting takes an
// older database as it is; the next gate to start migrates it.
const (
	grantsVersion = 3 // 0003_rule_grants.sql: grants keyed on the rule that asked
	pinsVersion   = 5 // 0005_pins.sql: the pins table
)

// runExplain shows how Derbent would decide one call, rule by rule, and changes nothing: it starts no
// servers, and reads the database read-only, only when it exists.
func runExplain(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("explain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	agent := flags.String("agent", "", "the agent label the call comes from")
	tool := flags.String("tool", "", "the tool as the gate names it, such as native__Bash, memory_write or github__create_issue")
	argsJSON := flags.String("args", "", "the call's arguments as a JSON object (default: none)")
	projectDir := flags.String("project", "", "project directory (default: the working directory)")
	configPath := flags.String("config", "", "config file (default: config.toml in the user config directory)")
	dbFlag := flags.String("db", "", "database to read budgets, pins and grants from, read-only (default: derbent.db in the user state directory)")
	session := flags.String("session", "", "a session to check for a grant: the CLI's session id, or the MCP gate's as derbent receipts shows it")
	asJSON := flags.Bool("json", false, "print one JSON object instead of text")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch {
	case flags.NArg() > 0:
		return fmt.Errorf("explain: unexpected argument %q", flags.Arg(0))
	case !agentName.MatchString(*agent):
		return fmt.Errorf("explain: --agent must be 1 to 32 lower-case letters, digits, dashes or underscores, got %q", *agent)
	case *tool == "":
		return errors.New("explain: give the tool with --tool, as the gate names it, such as native__Bash")
	case *argsJSON != "" && !json.Valid([]byte(*argsJSON)):
		return fmt.Errorf(`explain: --args is not JSON: %s (as it arrived; Windows PowerShell 5.1 drops the double quotes inside an argument unless each is written as \")`,
			visible.Escape(*argsJSON))
	}
	cfg, cfgPath, missing, err := loadConfig(*configPath, config.LoadForHook)
	if err != nil {
		return err
	}
	dir := *projectDir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return fmt.Errorf("find working directory: %w", err)
		}
	}
	key, err := config.ProjectKey(dir)
	if err != nil {
		return err
	}
	root, err := config.CheckoutRoot(dir)
	if err != nil {
		return err
	}
	x := explainer{cfg: cfg, session: *session}
	x.project, x.projectErr = config.NewProjectRules(root).Load()
	if x.dbPath, err = databasePath(*dbFlag); err != nil {
		return err
	}
	db, err := store.OpenExisting(ctx, x.dbPath)
	switch {
	case errors.Is(err, fs.ErrNotExist): // budgets, pins and grants are reported as not checked
	case err != nil:
		return err
	default:
		defer db.Close()
		x.db = db
		if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&x.version); err != nil {
			return fmt.Errorf("read the schema version of %s: %w", x.dbPath, err)
		}
	}
	e, err := x.explain(ctx, *agent, *tool, *argsJSON)
	if err != nil {
		return err
	}
	e.Config, e.ConfigMissing, e.Project, e.ProjectFile = cfgPath, missing, key, filepath.Join(root, config.ProjectRulesFile)
	if *asJSON {
		line, marshalErr := json.Marshal(e.withEmptyLists())
		if marshalErr != nil {
			return marshalErr
		}
		_, err = fmt.Fprintln(stdout, escapeRaw(string(line)))
		return err
	}
	return writeExplanation(stdout, e)
}

// explain works out how a call from agent to tool with argsJSON would be decided, in the order the gate
// decides it (gate.call and gate.settle): a name the MCP gate does not serve and a withheld tool, then
// arguments that are not an object and a project rules file that cannot be read, all before any rule; then
// the rules, a used-up budget, and for a call that asks, a session grant.
func (x explainer) explain(ctx context.Context, agent, tool, argsJSON string) (explanation, error) {
	e := explanation{Agent: agent, Tool: tool, Grant: "not needed: the call does not ask"}
	args, compact, isObject := gate.DecodeArgs(json.RawMessage(argsJSON))
	e.Args = compact
	server, name, configured := downstreamTool(x.cfg, tool)
	// The gate serves a server's tool only under a name agents accept, and leaves out the rest (gate.LeftOut).
	// The hook's servedBy does not ask this: a call under Derbent's own entry is the MCP gate's to decide
	// and record, even one it then refuses.
	servable := configured && gate.ServableName(tool)
	hidden := x.cfg.Rules.Hidden(agent, tool)
	switch {
	case strings.HasPrefix(tool, "native__"):
		e.Path = "hook"
	case gate.OwnTool(tool) || servable || hidden:
		e.Path = "mcp"
	default:
		e.Path = "none"
	}
	over, err := x.budgets(ctx, &e, agent, tool)
	if err != nil {
		return explanation{}, err
	}
	withheld := false
	switch {
	case servable:
		if withheld, err = x.pinState(ctx, &e, server, name, hidden); err != nil {
			return explanation{}, err
		}
		e.CannotKnow = append(e.CannotKnow, fmt.Sprintf("whether server %s offers a tool called %s, and which definition it sends now: "+
			"explain starts no servers, and the gate refuses a tool its server does not offer before any rule is read", server, name))
	case configured:
		e.Pin = "never pinned: the gate leaves out a tool whose name agents cannot be offered"
	default:
		e.Pin = "not a downstream tool: only downstream tools are pinned"
	}

	switch {
	case e.Path == "none" && configured:
		e.verdict(rule.Deny, "gate", tool+" is not a name the gate can serve: it serves a tool under a name of 1 to 64 letters, "+
			"digits, underscores or dashes, and leaves the rest out, so a call to it is refused before any rule is read")
	case e.Path == "none":
		e.verdict(rule.Deny, "gate", tool+" is not a tool the gate serves, so a call to it is refused before any rule is read; "+
			"the hook names a CLI's own tools native__<tool>")
	case withheld:
		e.verdict(rule.Deny, "pin", tool+" changed since it was pinned, so the gate withholds it and refuses its calls until derbent pins accept")
	case !isObject:
		e.verdict(rule.Deny, "gate", "its arguments are not a JSON object, so it is refused before any rule is read")
	case x.projectErr != nil:
		e.verdict(rule.Deny, "gate", x.projectErr.Error())
	}
	skipped := e.By != ""
	x.readRules(&e, agent, tool, args, skipped)
	if skipped {
		e.Grant = "not reached: the gate refuses the call before it reads any rule"
		return e, nil
	}

	j := gate.Judge(x.cfg.Rules, x.project, agent, tool, args)
	switch {
	case j.Action == rule.Deny:
		e.verdict(rule.Deny, j.By, j.Denial(tool))
	case over != nil:
		if j.Action == rule.Ask {
			e.Grant = fmt.Sprintf("not reached: budget %d refuses the call first", over.N)
		}
		e.verdict(rule.Deny, "budget:"+strconv.Itoa(over.N), over.Message(agent))
	case j.Action == rule.Ask:
		return e, x.grant(ctx, &e, agent, tool, j)
	default:
		e.verdict(rule.Allow, j.By, "")
	}
	return e, nil
}

// readRules records how the user's rules and the project's meet the call, each read as Decide reads it,
// or, when skipped, that the gate refuses the call before it reads any rule.
func (x explainer) readRules(e *explanation, agent, tool string, args map[string]any, skipped bool) {
	if skipped {
		e.RulesSkipped, e.RulesNotRead = true, x.cfg.Rules.Len()
	} else {
		e.Rules = x.cfg.Rules.Explain(agent, tool, args).Steps
		e.RulesNotRead = x.cfg.Rules.Len() - len(e.Rules)
	}
	switch {
	case x.projectErr != nil:
		e.ProjectNote = x.projectErr.Error()
	case x.project.Len() == 0:
		e.ProjectNote = "no project rules: the project adds nothing"
	case skipped:
		e.ProjectRulesNotRead = x.project.Len()
	default:
		px := x.project.Explain(agent, tool, args)
		e.ProjectRules, e.ProjectRulesNotRead = px.Steps, x.project.Len()-len(px.Steps)
		if px.Decision.Rule == 0 {
			e.ProjectNote = "no project rule matches: the project adds nothing"
		}
	}
}

// verdict records what decides the call and why. For a rule's deny and a budget, reason is what the gate
// tells the agent.
func (e *explanation) verdict(action rule.Action, by, reason string) {
	e.Action, e.By, e.Reason = action, by, reason
}

// budgets records every budget that applies to the call with its count in the window, as the gate counts
// it, and returns the one that would refuse the call, or nil.
func (x explainer) budgets(ctx context.Context, e *explanation, agent, tool string) (*rule.Reached, error) {
	window := x.cfg.Budgets.Window(agent, tool)
	switch {
	case window == 0:
		e.BudgetsNote = "no budget applies"
		return nil, nil
	case x.db == nil:
		e.BudgetsNote = "not checked: no database at " + x.dbPath
		return nil, nil
	}
	now := time.Now()
	passed, err := gate.Passed(ctx, receipt.NewLog(x.db), agent, now.Add(-window))
	if err != nil {
		return nil, err
	}
	for _, u := range x.cfg.Budgets.Uses(agent, tool, passed, now) {
		e.Budgets = append(e.Budgets, budgetState{
			Budget: u.N, Agent: u.Spec.Agent, Tool: u.Spec.Tool, Calls: u.Spec.Calls, Per: u.Spec.Per,
			InWindow: u.Count, UsedUp: u.Full, WaitMS: u.Wait.Milliseconds(),
		})
	}
	if r, reached := x.cfg.Budgets.Reached(agent, tool, passed, now); reached {
		return &r, nil
	}
	return nil, nil
}

// pinState records what the database holds about the pin of a downstream tool the gate can serve, and
// reports whether the gate would withhold the tool: it changed since it was pinned and no rule hides it,
// since a rule that hides a tool refuses it whether it changed or not (see gate.SyncTools).
func (x explainer) pinState(ctx context.Context, e *explanation, server, name string, hidden bool) (bool, error) {
	if !slices.ContainsFunc(x.cfg.Servers, func(s config.Server) bool { return s.Name == server && s.Pin }) {
		e.Pin = "not pinned: the server's table says pin = false"
		return false, nil
	}
	e.CannotKnow = append(e.CannotKnow, "whether a running gate could check this tool's pin: a gate that could not withholds the tool, "+
		"refuses its calls, and says why only on its stderr")
	switch {
	case x.db == nil:
		e.Pin = "not checked: no database at " + x.dbPath
		return false, nil
	case x.version < pinsVersion:
		e.Pin = x.olderThan(pinsVersion, "pins")
		return false, nil
	}
	p, err := pin.NewStore(x.db).Get(ctx, server, name)
	switch {
	case errors.Is(err, pin.ErrNoPin):
		e.Pin = "no pin yet: the first gate to list it pins it and serves it"
		return false, nil
	case err != nil:
		return false, err
	case p.State() == pin.Changed && hidden:
		e.Pin = "changed since it was pinned, and a rule hides it, which refuses it whether it changed or not"
		return false, nil
	case p.State() == pin.Changed:
		e.Pin = fmt.Sprintf("changed since it was pinned: derbent pins show %s__%s shows the change", server, name)
		return true, nil
	}
	e.Pin = "pinned, sha256 " + p.SHA256
	return false, nil
}

// olderThan says that what arrived in schema version v is not checked in a database older than that.
func (x explainer) olderThan(v int, what string) string {
	return fmt.Sprintf("not checked: the database's schema is version %d, and %s arrived in version %d; "+
		"the next gate to start migrates it", x.version, what, v)
}

// grant works out whether a session grant covers a call the rules send to the user, as the gate's ask looks
// one up, and records the verdict: the grant, or the wait for the user.
func (x explainer) grant(ctx context.Context, e *explanation, agent, tool string, j gate.Judgement) error {
	switch {
	case j.Key == "":
		e.Grant = "none can cover it: its rule matched on an argument it could not read, so it asks every time"
	case x.session == "":
		e.Grant = "not checked: give --session, the CLI's session id or the MCP gate's as derbent receipts shows it"
	case x.db == nil:
		e.Grant = "not checked: no database at " + x.dbPath
	case x.version < grantsVersion:
		e.Grant = x.olderThan(grantsVersion, "grants keyed on the rule")
	default:
		id, granted, err := approval.NewQueue(x.db).Granted(ctx, agent, x.session, tool, j.Key)
		if err != nil {
			return err
		}
		if granted {
			e.Grant = fmt.Sprintf("approval #%d granted it for session %s", id, x.session)
			e.verdict(rule.Allow, "grant:"+strconv.FormatInt(id, 10), "")
			return nil
		}
		e.Grant = "none in session " + x.session + " covers it"
	}
	e.verdict(rule.Ask, j.By, fmt.Sprintf("the call waits for the user for up to %s, and is denied if no answer comes", x.cfg.ApprovalTimeout))
	return nil
}

// pathText says how a call reaches Derbent, by explanation.Path.
var pathText = map[string]string{
	"hook": "a CLI's own tool, which its pre-tool hook sends to derbent gate",
	"mcp":  "a tool of the MCP gate, which serves it or refuses it by the rule that hides it",
	"none": "not a tool the gate serves",
}

// writeExplanation prints e, a line for each rule read and one for each other check, every line escaped:
// arguments, names and the project can come from agents, and patterns from a file.
func writeExplanation(w io.Writer, e explanation) error {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	add("call:     %s calls %s with %s", e.Agent, e.Tool, e.Args)
	add("project:  %s", e.Project)
	add("path:     %s", pathText[e.Path])
	missing := ""
	if e.ConfigMissing {
		missing = " (not found: every call is allowed)"
	}
	why := "the first match decides"
	if e.RulesSkipped {
		why = "the gate refuses the call before it reads any rule"
	}
	add("your rules, from %s%s:", e.Config, missing)
	for _, s := range e.Rules {
		add("  %s", stepLine("rule", s))
	}
	if e.RulesNotRead > 0 {
		add("  %s", notRead("rule", len(e.Rules)+1, e.RulesNotRead, why))
	}
	add("project rules, from %s:", e.ProjectFile)
	for _, s := range e.ProjectRules {
		add("  %s", stepLine("project rule", s))
	}
	if e.ProjectRulesNotRead > 0 {
		add("  %s", notRead("project rule", len(e.ProjectRules)+1, e.ProjectRulesNotRead, why))
	}
	if e.ProjectNote != "" {
		add("  %s", e.ProjectNote)
	}
	add("budgets:")
	for _, b := range e.Budgets {
		add("  %s", budgetLine(b))
	}
	if e.BudgetsNote != "" {
		add("  %s", e.BudgetsNote)
	}
	add("pin:      %s", e.Pin)
	add("grant:    %s", e.Grant)
	for _, c := range e.CannotKnow {
		add("unknown:  %s", c)
	}
	verdict := fmt.Sprintf("verdict:  %s (%s)", e.Action, e.By)
	if e.Reason != "" {
		verdict += ": " + e.Reason
	}
	out = append(out, verdict)
	for _, l := range out {
		if _, err := fmt.Fprintln(w, visible.Escape(l)); err != nil {
			return err
		}
	}
	return nil
}

// notRead says that n rules from first on are not read, and why.
func notRead(label string, first, n int, why string) string {
	if n == 1 {
		return fmt.Sprintf("%s %d is not read: %s", label, first, why)
	}
	return fmt.Sprintf("%ss %d to %d are not read: %s", label, first, first+n-1, why)
}

// withEmptyLists returns e with every list it has nothing in, a step's args included, made empty rather
// than nil, so --json writes [] and never null.
func (e explanation) withEmptyLists() explanation {
	e.Rules, e.ProjectRules = stepsWithArgs(e.Rules), stepsWithArgs(e.ProjectRules)
	e.Budgets, e.CannotKnow = orEmpty(e.Budgets), orEmpty(e.CannotKnow)
	return e
}

func stepsWithArgs(steps []rule.Step) []rule.Step {
	out := make([]rule.Step, len(steps))
	for i, s := range steps {
		s.Args = orEmpty(s.Args)
		out[i] = s
	}
	return out
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// stepLine says how one rule met the call, such as `rule 3 (ask): tool native__Bash matches, command
// "git push origin" matches git push*: it matches`.
func stepLine(label string, s rule.Step) string {
	var parts []string
	if s.Agent != "" {
		parts = append(parts, "agent "+s.Agent+" "+matchWord(s.AgentMatches))
	}
	if s.Tool != "" {
		parts = append(parts, "tool "+s.Tool+" "+matchWord(s.ToolMatches))
	}
	for _, a := range s.Args {
		parts = append(parts, argLine(a))
	}
	if len(parts) == 0 {
		parts = append(parts, "no conditions")
	}
	result := "no match"
	if s.Matches {
		result = "it matches"
	}
	return fmt.Sprintf("%s %d (%s): %s: %s", label, s.Rule, s.Action, strings.Join(parts, ", "), result)
}

func matchWord(ok bool) string {
	if ok {
		return "matches"
	}
	return "does not match"
}

// argLine says how one args condition met the call's argument.
func argLine(a rule.Arg) string {
	switch a.Read {
	case rule.ArgMatches:
		return fmt.Sprintf("%s %q matches %s", a.Name, a.Value, a.Pattern)
	case rule.ArgDiffers:
		return fmt.Sprintf("%s %q does not match %s", a.Name, a.Value, a.Pattern)
	case rule.ArgMissing:
		return fmt.Sprintf("%s is missing, so %s does not match", a.Name, a.Pattern)
	case rule.ArgUnreadable:
		return fmt.Sprintf("%s is not a string, which %s cannot read: that matches a deny or an ask, never an allow", a.Name, a.Pattern)
	}
	return ""
}

// budgetLine says how one budget stands.
func budgetLine(b budgetState) string {
	s := fmt.Sprintf("budget %d: %d %s to %s per %s for %s: %d in the window", b.Budget, b.Calls, plural(b.Calls, "call", "calls"),
		cmp.Or(b.Tool, "*"), b.Per, cmp.Or(b.Agent, "*"), b.InWindow)
	if b.UsedUp {
		wait := max(time.Duration(b.WaitMS)*time.Millisecond, time.Second).Round(time.Second)
		s += fmt.Sprintf(", used up; the next call is possible in about %s", wait)
	}
	return s
}
