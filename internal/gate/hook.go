package gate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/receipt"
	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// Verdict is the gate's answer to an agent CLI's pre-tool hook.
type Verdict int

const (
	// NoDecision means a rule allowed the call: the hook says nothing, so the CLI's own permission
	// settings still apply on top.
	NoDecision Verdict = iota
	// Allowed means the user approved the call, now or earlier in the same session, and the user's rules
	// alone would not have allowed it. A call only the project asked about answers NoDecision once
	// approved, as the user's rules alone would, so a project file never skips the CLI's own settings.
	Allowed
	// Denied means a rule, the gate or the user refused the call, or no answer came in time.
	Denied
)

// HookAnswer is what a pre-tool hook tells its CLI.
type HookAnswer struct {
	Verdict Verdict
	Reason  string // for Denied: shown to the model
}

// Hook decides a call to one of a CLI's own tools, reported by the CLI's pre-tool hook, under the same
// rules, approvals and redaction as MCP calls, and appends its receipt. The hook runs before the tool
// does, so the outcome is "gated" for a call that may go ahead and "refused" for one that may not. A
// call whose receipt cannot be written is refused, and the error says why.
func (g *Gate) Hook(ctx context.Context, tool string, args json.RawMessage) (HookAnswer, error) {
	start := time.Now()
	decoded, argsJSON, isObject := DecodeArgs(args)
	rec := receipt.Receipt{
		Project: g.Project, Agent: g.Agent, Session: g.Session, Tool: tool,
		Args: g.redact(argsJSON), ArgsSHA256: sha256Hex(args),
	}
	s := g.settle(ctx, tool, decoded, isObject, rec.Args)
	rec.DecidedBy = s.by
	ans := HookAnswer{Verdict: Denied, Reason: "derbent: " + s.text}
	rec.Decision, rec.Outcome = string(rule.Deny), "refused"
	if s.allow {
		ans = HookAnswer{Verdict: NoDecision}
		if s.user {
			ans.Verdict = Allowed
		}
		rec.Decision, rec.Outcome = string(rule.Allow), "gated"
	}
	rec.ResultSize, rec.ResultSHA256 = resultDigest(nil, nil)
	rec.Duration = time.Since(start)
	if _, err := g.Receipts.Append(context.WithoutCancel(ctx), rec); err != nil {
		return HookAnswer{Verdict: Denied, Reason: "derbent: the decision could not be recorded, so the call is refused: " + err.Error()}, err
	}
	return ans, nil
}
