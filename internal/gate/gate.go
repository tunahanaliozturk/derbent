// Package gate is the MCP server one agent session talks to. It serves the memory tools, decides every
// call with the rules, and writes a receipt for each call whatever its outcome.
package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/pin"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// Gate serves one agent session.
type Gate struct {
	Agent   string
	Project string
	Session string
	Version string
	Rules   rule.Set
	// Budgets limit how many calls the agent may have let through to some tools within a window
	// (ADR 0012). The zero value has none.
	Budgets rule.Budgets
	// ProjectRules, when set, reads the project's .derbent.toml, whose rules can only make a call
	// stricter (ADR 0014). Nil means the user's rules alone.
	ProjectRules *config.ProjectRules
	Memory       *memory.Store
	Receipts     *receipt.Log
	// Forward sends a call to a downstream server. It may be nil when no servers are configured.
	Forward func(ctx context.Context, server, tool string, args json.RawMessage) (*mcp.CallToolResult, error)
	// Pins, when set, pins each downstream tool on first sight and withholds one whose definition has
	// changed since (ADR 0013). Nil serves tools unpinned.
	Pins *pin.Store
	// Unpinned names the servers whose tools are served without pinning (pin = false).
	Unpinned map[string]bool
	// Redact masks secrets in arguments before they are stored in a receipt or in the approvals table.
	// Nil stores them as they are.
	Redact func(string) string
	// Approvals holds the calls a rule sends to the user. Nil refuses them.
	Approvals *approval.Queue
	// ApprovalTimeout is how long a call waits for the user before it is denied.
	ApprovalTimeout time.Duration
	// Stop ends when the gate is told to stop, and withdraws the calls still waiting for the user so
	// that none is approved while the gate shuts down. Nil never ends.
	Stop context.Context
	// ToolsReady, when set, holds the agent's tool listings and calls until it is closed or ToolsWait
	// has passed since Server was called: a downstream server that comes up quickly is in the agent's
	// first list, and one that is slow holds nothing for long. The agent's initialize is never held.
	ToolsReady <-chan struct{}
	ToolsWait  time.Duration

	server   *mcp.Server
	started  time.Time
	local    map[string]bool // the memory tools this gate registered, written only by Server
	mu       sync.Mutex
	owners   map[string]string       // gate tool name to the downstream server it belongs to
	withheld map[string]withheldTool // gate tool name to a tool kept from the agent, under mu
}

// knobs switch safety checks off, so that tests can prove the tests of those checks can fail, and let
// a test see when a request starts waiting. Only tests set them, through export_test.go.
var knobs struct {
	skipRules     bool
	skipHiding    bool
	skipRedaction bool
	waiting       func()        // called as a tool listing or call starts waiting for the downstream servers
	pinRecheck    time.Duration // how often WatchPins looks, when set
}

const instructions = "Derbent gates this session's tools. memory_write, memory_search and memory_read " +
	"share notes with the other agents working on this project."

// Server builds the MCP server for this session.
func (g *Gate) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "derbent", Version: g.Version},
		&mcp.ServerOptions{Instructions: instructions})
	g.server, g.started = s, time.Now()
	g.addMemoryTools(s)
	s.AddReceivingMiddleware(g.gateCalls)
	return s
}

// gateCalls applies the rules to every tools/call request and records a receipt for it. Tool listings
// and calls wait while the downstream servers make their first attempt, so neither finds a tool
// missing that was about to arrive. Every other method passes through untouched.
func (g *Gate) gateCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "tools/list" || method == "tools/call" {
			g.waitForTools(ctx)
		}
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		call, ok := req.(*mcp.CallToolRequest)
		if !ok {
			// The SDK builds a *mcp.CallToolRequest for every tools/call. Should that change, a call the
			// gate cannot read is refused rather than passed on with no rule applied.
			return nil, fmt.Errorf("derbent: refused a tools/call request of type %T, which this gate cannot read", req)
		}
		return g.call(ctx, method, call, next)
	}
}

// waitForTools returns when ToolsReady is closed, ToolsWait has passed since the session began, or ctx
// ends, whichever comes first.
func (g *Gate) waitForTools(ctx context.Context) {
	if g.ToolsReady == nil {
		return
	}
	wait := time.NewTimer(g.ToolsWait - time.Since(g.started))
	defer wait.Stop()
	if knobs.waiting != nil {
		knobs.waiting()
	}
	select {
	case <-g.ToolsReady:
	case <-wait.C:
	case <-ctx.Done():
	}
}

// call decides one tools/call request and records its receipt. The gate itself refuses, before any rule
// is read, a name it does not serve and a tool its pin withholds; settle decides the rest.
func (g *Gate) call(ctx context.Context, method string, req *mcp.CallToolRequest, next mcp.MethodHandler) (mcp.Result, error) {
	start := time.Now()
	name := req.Params.Name
	args, argsJSON, isObject := decodeArgs(req.Params.Arguments)
	rec := receipt.Receipt{
		Project: g.Project, Agent: g.Agent, Session: g.Session, Tool: name,
		Args: g.redact(argsJSON), ArgsSHA256: sha256Hex(req.Params.Arguments),
		Decision: string(rule.Deny), Outcome: "refused",
	}
	// Asking the user about a name the gate does not serve would only fill their queue with a call that
	// fails anyway. Hook calls name the CLI's own tools, which the gate never serves, so this check is
	// here and not in settle.
	s := settled{by: "gate", text: name + " is not a tool this gate serves"}
	w, held := g.held(name)
	switch {
	case held && w.unchecked:
		s = settled{by: "pin", text: name + " is withheld because its pin could not be checked; the gate's stderr says why"}
	case held:
		// The agent holds a list from before the tool changed; the changed tool is refused before any
		// rule, like a name the gate does not serve (ADR 0013).
		s = settled{by: "pin", text: name + " changed since it was pinned; the user can review it with derbent pins"}
	case g.serves(name):
		s = g.settle(ctx, name, args, isObject, rec.Args)
	}
	rec.DecidedBy = s.by
	var (
		res mcp.Result
		err error
	)
	switch {
	case s.allow:
		rec.Decision = string(rule.Allow)
		res, err = next(ctx, method, req)
		rec.Outcome = outcome(res, err)
	case s.err != nil: // the agent gave up while the call waited
		err = s.err
	default:
		res = toolError("derbent: " + s.text)
	}
	rec.ResultSize, rec.ResultSHA256 = resultDigest(res, err)
	rec.Duration = time.Since(start)
	// The receipt is written even when the agent has given up on the call.
	if _, appendErr := g.Receipts.Append(context.WithoutCancel(ctx), rec); appendErr != nil {
		return nil, unrecorded(name, rec.Outcome, appendErr)
	}
	return res, err
}

// settled is how the gate answered a call before it runs.
type settled struct {
	allow bool
	user  bool   // the call is allowed because the user approved it, now or earlier in the session
	by    string // what decided: rule:<n>, project:<n>, budget:<n>, pin, gate, user:<id>, grant:<id>, timeout:<id> or withdrawn:<id>
	text  string // what the agent is told when the call is refused
	err   error  // set when the agent gave up while the call waited
}

// strictness orders the actions, so project rules can only make a call stricter (ADR 0014).
var strictness = map[rule.Action]int{rule.Allow: 0, rule.Ask: 1, rule.Deny: 2}

// verdict is what the rules decided for a call: the user's rules, made stricter by the project's.
type verdict struct {
	action  rule.Action
	by      string // rule:<n> or project:<n>
	rule    int
	project bool   // the project's rules set the action
	key     string // what a session grant for the call is keyed on; "" when no grant may cover it
}

// judge decides a call with the user's rules and then the project's (ADR 0014). The stricter action
// wins, deny over ask over allow, and on a tie the user's rule is named. A call the project's rules ask
// about is keyed on both lists, the user's rules up to the one that decided and the project's up to the
// one that asked, so an edit at or above either stops a grant from covering it (ADR 0011). A call a rule
// matched on an argument it could not read gets no key, so no grant covers it. A project with no rules
// file, or no rule matching, adds nothing: Decide on its rules gives Rule 0 then.
func (g *Gate) judge(name string, args map[string]any) (verdict, error) {
	if knobs.skipRules {
		return verdict{action: rule.Allow, by: "rule:0"}, nil
	}
	d := g.Rules.Decide(g.Agent, name, args)
	v := verdict{action: d.Action, by: "rule:" + strconv.Itoa(d.Rule), rule: d.Rule, key: g.Rules.Key(d.Rule)}
	unread := d.Unread
	if g.ProjectRules != nil {
		set, err := g.ProjectRules.Load()
		if err != nil {
			return verdict{}, err
		}
		if p := set.Decide(g.Agent, name, args); p.Rule > 0 {
			if strictness[p.Action] > strictness[v.action] {
				v.action, v.by, v.rule, v.project = p.Action, "project:"+strconv.Itoa(p.Rule), p.Rule, true
			}
			if p.Action == rule.Ask {
				v.key += "+" + set.Key(p.Rule)
				unread = unread || p.Unread
			}
		}
	}
	if unread {
		v.key = ""
	}
	return v, nil
}

// settle decides a call before it runs: the user's rules, made stricter by the project's, then a budget
// that is used up, and for a call that must ask, a grant from an earlier "approve for this session" or
// the user's answer. MCP calls and pre-tool hook calls both come through here, so the two paths cannot
// decide differently. redacted is the arguments as the approval queue may show them.
func (g *Gate) settle(ctx context.Context, name string, args map[string]any, isObject bool, redacted string) settled {
	if !isObject {
		// Arguments are an object. Rules read named string arguments, so anything else would slip past
		// an args condition that a deny depends on.
		return settled{by: "gate", text: name + " was refused: its arguments are not a JSON object"}
	}
	v, err := g.judge(name, args)
	if err != nil {
		return settled{by: "gate", text: name + " was refused: " + err.Error()}
	}
	if v.action == rule.Deny {
		if v.project {
			return settled{by: v.by, text: fmt.Sprintf("%s is not allowed in this project (rule %d of %s)", name, v.rule, config.ProjectRulesFile)}
		}
		return settled{by: v.by, text: fmt.Sprintf("%s is not allowed for this agent (rule %d)", name, v.rule)}
	}
	// A deny stays a deny. Otherwise a used-up budget refuses the call without asking the user: once the
	// calls an agent stuck in a loop had let through use it up, its further calls stop here (ADR 0012).
	if s, over := g.overBudget(ctx, name); over {
		return s
	}
	if v.action == rule.Ask {
		return g.ask(ctx, name, redacted, v)
	}
	return settled{allow: true, by: v.by}
}

// overBudget refuses a call when a budget that applies to it is used up. The count reads the receipts,
// which every gate and hook on the machine appends to, so it is shared; it is not one transaction with
// the append that follows, so calls in flight at the same moment can pass a budget by their number. A
// count that cannot be read refuses the call.
func (g *Gate) overBudget(ctx context.Context, name string) (settled, bool) {
	window := g.Budgets.Window(g.Agent, name)
	if window == 0 {
		return settled{}, false
	}
	now := time.Now()
	allowed, err := g.Receipts.AllowedSince(ctx, g.Agent, now.Add(-window))
	if err != nil {
		return settled{by: "gate", text: name + " was refused because its budget could not be counted: " + err.Error()}, true
	}
	passed := make([]rule.Passed, len(allowed))
	for i, a := range allowed {
		passed[i] = rule.Passed{Tool: a.Tool, At: a.At}
	}
	r, reached := g.Budgets.Reached(g.Agent, name, passed, now)
	if !reached {
		return settled{}, false
	}
	return settled{by: "budget:" + strconv.Itoa(r.N), text: r.Message(g.Agent)}, true
}

// ask settles a call the rules send to the user. A grant from an earlier "approve for this session" lets
// it through at once when it was keyed the same way (see judge): grants are keyed on a fingerprint of the
// rule and every rule above it, so one rule's grant never covers a call that another rule holds, nor one
// the same rule catches after a rule above it changed (ADR 0011). Otherwise the call waits in the
// approval queue until the user decides, the timeout passes, the agent gives up, or the gate is told to
// stop. A call with no key is shown to the user every time, and approving it writes no grant.
func (g *Gate) ask(ctx context.Context, name, redacted string, v verdict) settled {
	if g.Approvals == nil {
		return settled{by: v.by, text: name + " needs the user's approval, and this gate cannot ask for it"}
	}
	id, granted, err := g.Approvals.Granted(ctx, g.Agent, g.Session, name, v.key)
	if err != nil {
		return settled{by: v.by, text: name + " needs the user's approval, which could not be checked: " + err.Error()}
	}
	if granted {
		return settled{allow: true, user: true, by: "grant:" + strconv.FormatInt(id, 10)}
	}
	// A gate told to stop asks nothing more: the call never waits, so the gate itself refuses it.
	stopping := settled{by: "gate", text: name + " was refused because the gate is stopping; it did not run"}
	if g.Stop != nil && g.Stop.Err() != nil {
		return stopping
	}
	// The call waits until the agent gives up or the gate is told to stop, whichever comes first.
	wait, cancel := context.WithCancel(ctx)
	defer cancel()
	if g.Stop != nil {
		stop := context.AfterFunc(g.Stop, cancel)
		defer stop()
	}
	out, err := g.Approvals.Ask(wait, approval.Request{
		Project: g.Project, Agent: g.Agent, Session: g.Session, Tool: name, Args: redacted,
		Rule: v.rule, ProjectRule: v.project, RuleKey: v.key,
	}, g.ApprovalTimeout)
	ref := strconv.FormatInt(out.ID, 10)
	withdrawn := "withdrawn:" + ref
	if out.ID == 0 {
		withdrawn = "gate" // the wait ended before the approval row was written, so nothing was withdrawn
	}
	switch {
	case err != nil && ctx.Err() != nil:
		return settled{by: withdrawn, text: name + " was withdrawn before the user decided", err: err}
	case err != nil && wait.Err() != nil && out.ID == 0:
		return stopping // the gate stopped before the call could wait, as if it had been stopping already
	case err != nil && wait.Err() != nil:
		return settled{by: withdrawn, text: name + " was withdrawn before the user decided, because the gate is stopping; it did not run"}
	case err != nil:
		return settled{by: v.by, text: name + " needs the user's approval, which could not be asked for: " + err.Error()}
	case out.Approved:
		return settled{allow: true, user: true, by: "user:" + ref}
	case out.By == approval.ByTimeout:
		return settled{by: "timeout:" + ref, text: fmt.Sprintf("%s needs the user's approval and none came within %s, so it was denied. "+
			"Try again and ask the user to approve it in the derbent UI while it waits.", name, g.ApprovalTimeout)}
	default:
		return settled{by: "user:" + ref, text: "the user denied " + name}
	}
}

// unrecorded is the error for a call whose receipt could not be written. It tells the agent whether
// the tool ran, because a tool that ran has left its effect behind and must not simply be retried, and
// it tells the user through stderr, which is the only other place a gate process can speak.
func unrecorded(tool, outcome string, err error) error {
	slog.Error("derbent: a call could not be recorded", "tool", tool, "outcome", outcome, "err", err)
	if outcome == "refused" {
		return fmt.Errorf("derbent: %s was refused and did not run, but the refusal could not be recorded: %w", tool, err)
	}
	return fmt.Errorf("derbent: %s ran (outcome %s) but could not be recorded; do not repeat it without checking its effect: %w",
		tool, outcome, err)
}

func (g *Gate) redact(args string) string {
	if g.Redact == nil || knobs.skipRedaction {
		return args
	}
	return g.Redact(args)
}

// decodeArgs returns the arguments as a map for the rules and as compact JSON for the receipt, and
// reports whether they are an object. Missing and null arguments are both no arguments to the rules;
// missing ones are recorded as {} and null ones as null.
func decodeArgs(raw json.RawMessage) (map[string]any, string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, "{}", true
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, string(raw), false
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, compact.String(), false
	}
	return args, compact.String(), true
}

func outcome(res mcp.Result, err error) string {
	if err != nil {
		return "error"
	}
	if r, ok := res.(*mcp.CallToolResult); ok && r != nil && r.IsError {
		return "error"
	}
	return "ok"
}

// resultDigest returns the size and SHA-256 of the bytes a receipt keeps for a call's outcome: the
// error text for a protocol error, and otherwise resultBytes.
func resultDigest(res mcp.Result, err error) (int64, string) {
	var b []byte
	switch {
	case err != nil:
		b = []byte(err.Error())
	case res != nil:
		b = resultBytes(res)
	}
	return int64(len(b)), sha256Hex(b)
}

// resultBytes is the form of a result that receipts hash, chosen so that anyone holding what the agent
// received can recompute it (ADR 0004): a JSON object with the result's content, structuredContent and
// isError fields, object keys sorted, numbers as written, and no HTML escaping. The SDK adds _meta and
// resultType after the gate has run, so those are not part of it.
func resultBytes(res mcp.Result) []byte {
	raw, err := json.Marshal(res)
	if err != nil {
		return []byte(err.Error())
	}
	var full map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err = dec.Decode(&full); err != nil {
		return raw
	}
	picked := make(map[string]any, 3)
	for _, k := range []string{"content", "structuredContent", "isError"} {
		if v, ok := full[k]; ok {
			picked[k] = v
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(picked); err != nil {
		return raw
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
