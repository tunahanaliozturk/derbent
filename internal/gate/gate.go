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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tunahanaliozturk/portcullis/internal/memory"
	"github.com/tunahanaliozturk/portcullis/internal/receipt"
	"github.com/tunahanaliozturk/portcullis/internal/rule"
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
}

// knobs switch safety checks off. Only tests set them, through export_test.go, to prove that the
// tests of those checks can fail.
var knobs struct {
	skipRules  bool
	skipHiding bool
}

const instructions = "Portcullis gates this session's tools. memory_write, memory_search and memory_read " +
	"share notes with the other agents working on this project."

// Server builds the MCP server for this session.
func (g *Gate) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "portcullis", Version: g.Version},
		&mcp.ServerOptions{Instructions: instructions})
	g.addMemoryTools(s)
	s.AddReceivingMiddleware(g.gateCalls)
	return s
}

// gateCalls applies the rules to every tools/call request and records a receipt for it. Every other
// method passes through untouched.
func (g *Gate) gateCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if method != "tools/call" || !ok {
			return next(ctx, method, req)
		}
		return g.call(ctx, method, call, next)
	}
}

func (g *Gate) call(ctx context.Context, method string, req *mcp.CallToolRequest, next mcp.MethodHandler) (mcp.Result, error) {
	start := time.Now()
	name := req.Params.Name
	args, argsJSON := decodeArgs(req.Params.Arguments)
	decision := g.Rules.Decide(g.Agent, name, args)
	if knobs.skipRules {
		decision = rule.Decision{Action: rule.Allow}
	}
	rec := receipt.Receipt{
		Project: g.Project, Agent: g.Agent, Session: g.Session, Tool: name,
		Args: argsJSON, ArgsSHA256: sha256Hex(req.Params.Arguments),
		Decision: string(decision.Action), DecidedBy: "rule:" + strconv.Itoa(decision.Rule),
	}
	var (
		res mcp.Result
		err error
	)
	switch decision.Action {
	case rule.Allow:
		res, err = next(ctx, method, req)
		rec.Outcome = outcome(res, err)
	case rule.Deny:
		res = refusal(name, decision.Rule)
		rec.Outcome = "refused"
	}
	rec.ResultSize, rec.ResultSHA256 = resultDigest(res, err)
	rec.Duration = time.Since(start)
	// The receipt is written even when the agent has given up on the call.
	if _, appendErr := g.Receipts.Append(context.WithoutCancel(ctx), rec); appendErr != nil {
		return nil, unrecorded(name, rec.Outcome, appendErr)
	}
	return res, err
}

// unrecorded is the error for a call whose receipt could not be written. It tells the agent whether
// the tool ran, because a tool that ran has left its effect behind and must not simply be retried, and
// it tells the user through stderr, which is the only other place a gate process can speak.
func unrecorded(tool, outcome string, err error) error {
	slog.Error("portcullis: a call could not be recorded", "tool", tool, "outcome", outcome, "err", err)
	if outcome == "refused" {
		return fmt.Errorf("portcullis: %s was refused and did not run, but the refusal could not be recorded: %w", tool, err)
	}
	return fmt.Errorf("portcullis: %s ran (outcome %s) but could not be recorded; do not repeat it without checking its effect: %w",
		tool, outcome, err)
}

// decodeArgs returns the arguments as a map for the rules, or nil when they are not a JSON object, and
// as compact JSON for the receipt. Missing arguments are recorded as {}.
func decodeArgs(raw json.RawMessage) (map[string]any, string) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, string(raw)
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, compact.String()
	}
	return args, compact.String()
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

func refusal(tool string, ruleIndex int) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("portcullis: %s is not allowed for this agent (rule %d)", tool, ruleIndex),
		}},
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
