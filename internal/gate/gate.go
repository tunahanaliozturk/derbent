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
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// Gate serves one agent session.
type Gate struct {
	Agent    string
	Project  string
	Session  string
	Version  string
	Rules    rule.Set
	Memory   *memory.Store
	Receipts *receipt.Log
	// Forward sends a call to a downstream server. It may be nil when no servers are configured.
	Forward func(ctx context.Context, server, tool string, args json.RawMessage) (*mcp.CallToolResult, error)
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

	server  *mcp.Server
	started time.Time
	local   map[string]bool // the memory tools this gate registered, written only by Server
	mu      sync.Mutex
	owners  map[string]string // gate tool name to the downstream server it belongs to
}

// knobs switch safety checks off, so that tests can prove the tests of those checks can fail, and let
// a test see when a request starts waiting. Only tests set them, through export_test.go.
var knobs struct {
	skipRules     bool
	skipHiding    bool
	skipRedaction bool
	waiting       func() // called as a tool listing or call starts waiting for the downstream servers
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
// is read, a name it does not serve; settle decides the rest.
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
	if g.serves(name) {
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
	by    string // what decided: rule:<n>, gate, user:<id>, grant:<id>, timeout:<id> or withdrawn:<id>
	text  string // what the agent is told when the call is refused
	err   error  // set when the agent gave up while the call waited
}

// settle decides a call before it runs: the rules first, and for a rule that says ask, a grant from an
// earlier "approve for this session" or the user's answer. MCP calls and pre-tool hook calls both come
// through here, so the two paths cannot decide differently. redacted is the arguments as the approval
// queue may show them.
func (g *Gate) settle(ctx context.Context, name string, args map[string]any, isObject bool, redacted string) settled {
	if !isObject {
		// Arguments are an object. Rules read named string arguments, so anything else would slip past
		// an args condition that a deny depends on.
		return settled{by: "gate", text: name + " was refused: its arguments are not a JSON object"}
	}
	d := g.Rules.Decide(g.Agent, name, args)
	if knobs.skipRules {
		d = rule.Decision{Action: rule.Allow}
	}
	byRule := "rule:" + strconv.Itoa(d.Rule)
	switch d.Action { // rule.Compile admits these three and no other
	case rule.Allow:
		return settled{allow: true, by: byRule}
	case rule.Ask:
		return g.ask(ctx, name, redacted, d, byRule)
	case rule.Deny:
	}
	return settled{by: byRule, text: fmt.Sprintf("%s is not allowed for this agent (rule %d)", name, d.Rule)}
}

// ask settles a call a rule sends to the user. A grant from an earlier "approve for this session" lets
// it through at once, when the same rule, under the same rules above it, asked for that grant: grants
// are keyed on a fingerprint of the rule and every rule above it, so one rule's grant never covers a
// call that another rule holds, nor one the same rule catches after a rule above it changed (ADR 0011).
// Otherwise the call waits in the approval queue until the user decides, the timeout passes, the agent
// gives up, or the gate is told to stop. A call the rule matched on a value it could not read gets no
// rule key, so no grant covers it and approving it writes none: every such call is shown to the user.
func (g *Gate) ask(ctx context.Context, name, redacted string, d rule.Decision, byRule string) settled {
	if g.Approvals == nil {
		return settled{by: byRule, text: name + " needs the user's approval, and this gate cannot ask for it"}
	}
	ruleKey := g.Rules.Key(d.Rule)
	if d.Unread {
		ruleKey = "" // an empty key never matches a grant, and an approval with none grants nothing
	}
	id, granted, err := g.Approvals.Granted(ctx, g.Agent, g.Session, name, ruleKey)
	if err != nil {
		return settled{by: byRule, text: name + " needs the user's approval, which could not be checked: " + err.Error()}
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
		Project: g.Project, Agent: g.Agent, Session: g.Session, Tool: name, Args: redacted, Rule: d.Rule, RuleKey: ruleKey,
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
		return settled{by: byRule, text: name + " needs the user's approval, which could not be asked for: " + err.Error()}
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
