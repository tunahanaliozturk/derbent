// Package hook speaks the pre-tool hook protocols of the agent CLIs: it reads the call a CLI is about
// to make from the hook's standard input, and writes the gate's answer in the form that CLI expects.
package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tunahanaliozturk/derbent/internal/gate"
)

// ErrInput marks hook input a protocol cannot use.
var ErrInput = errors.New("unusable hook input")

// Call is a tool call a CLI reported to its pre-tool hook.
type Call struct {
	Session string          // the CLI's session, which scopes "approve for this session"
	Dir     string          // the working directory, which picks the project
	Tool    string          // the tool's name as the CLI reports it
	Server  string          // the MCP server entry of the tool, for CLIs that name it (Claude Code)
	Args    json.RawMessage // the tool's input
}

// Protocol is one CLI's hook protocol.
type Protocol struct {
	parse  func([]byte) (Call, error)
	answer func(gate.HookAnswer) []byte
	prefix func(server string) string // how the CLI names the tools of an MCP server entry
}

var protocols = map[string]Protocol{
	"claude":      {parse: parseClaude, answer: answerClaude, prefix: mcpPrefix},
	"codex":       {parse: parseClaude, answer: answerClaude, prefix: mcpPrefix}, // Codex mirrors Claude Code's contract
	"copilot":     {parse: parseCopilot, answer: answerCopilot, prefix: func(s string) string { return s + "-" }},
	"antigravity": {parse: parseAntigravity, answer: answerAntigravity, prefix: func(s string) string { return "mcp_" + s + "_" }},
}

func mcpPrefix(server string) string { return "mcp__" + server + "__" }

// Lookup returns the protocol of the named CLI.
func Lookup(cli string) (Protocol, bool) {
	p, ok := protocols[cli]
	return p, ok
}

// Names lists the CLIs with a protocol, sorted.
func Names() []string {
	names := make([]string, 0, len(protocols))
	for n := range protocols {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Parse reads a call from the hook's standard input. A leading UTF-8 byte order mark is skipped: a .NET
// program writing through Process.StandardInput sends one when the console input encoding is UTF-8.
func (p Protocol) Parse(in []byte) (Call, error) {
	c, err := p.parse(bytes.TrimPrefix(in, []byte("\xef\xbb\xbf")))
	if err != nil {
		return Call{}, fmt.Errorf("%w: %w", ErrInput, err)
	}
	switch {
	case c.Tool == "":
		return Call{}, fmt.Errorf("%w: no tool name", ErrInput)
	case c.Session == "":
		return Call{}, fmt.Errorf("%w: no session id", ErrInput)
	}
	if len(bytes.TrimSpace(c.Args)) == 0 {
		c.Args = json.RawMessage(`{}`)
	}
	return c, nil
}

// Answer is what the hook writes to standard output: nothing for no decision.
func (p Protocol) Answer(a gate.HookAnswer) []byte {
	if a.Verdict == gate.NoDecision {
		return nil
	}
	return p.answer(a)
}

// Own reports whether c calls one of Derbent's own MCP tools, which the MCP gate already decides and
// records: c.Tool starts with this CLI's prefix for the MCP server entry called server, served accepts
// the rest of the name, and the server the CLI named, if it names one, is server. For CLIs that name no
// server one gap remains: a foreign entry whose name and tool join, under the CLI's naming, into this
// prefix followed by a name served accepts still looks like Derbent's own. That can collide with a
// server in Derbent's config (an entry called derbent__github in Codex) or with a memory tool (an entry
// called derbent_memory with a tool called write, under Antigravity's naming).
func (p Protocol) Own(server string, c Call, served func(tool string) bool) bool {
	if c.Server != "" && c.Server != server {
		return false
	}
	rest, ok := strings.CutPrefix(c.Tool, p.prefix(server))
	return ok && served(rest)
}

func decision(a gate.HookAnswer) string {
	if a.Verdict == gate.Allowed {
		return "allow"
	}
	return "deny"
}

func reason(a gate.HookAnswer) string {
	if a.Verdict == gate.Denied {
		return a.Reason
	}
	return ""
}

func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // the values are structs of strings, which always encode
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// Claude Code, and Codex, which mirrors it. Claude Code 2.1.274 and later name the server of an MCP
// tool in mcp_server.

type claudeInput struct {
	SessionID string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	MCPServer struct {
		Name string `json:"name"`
	} `json:"mcp_server"`
}

func parseClaude(in []byte) (Call, error) {
	var v claudeInput
	if err := json.Unmarshal(in, &v); err != nil {
		return Call{}, err
	}
	return Call{Session: v.SessionID, Dir: v.Cwd, Tool: v.ToolName, Server: v.MCPServer.Name, Args: v.ToolInput}, nil
}

type claudeOutput struct {
	HookSpecificOutput claudeDecision `json:"hookSpecificOutput"`
}

type claudeDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func answerClaude(a gate.HookAnswer) []byte {
	return marshal(claudeOutput{claudeDecision{"PreToolUse", decision(a), reason(a)}})
}

// GitHub Copilot CLI: camelCase input with toolArgs as an object or a string of JSON, or the
// PascalCase input it sends to hooks registered as PreToolUse.

type copilotInput struct {
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	ToolName  string          `json:"toolName"`
	ToolArgs  json.RawMessage `json:"toolArgs"`
}

func parseCopilot(in []byte) (Call, error) {
	var v copilotInput
	if err := json.Unmarshal(in, &v); err != nil {
		return Call{}, err
	}
	if v.ToolName == "" {
		return parseClaude(in)
	}
	args := v.ToolArgs
	if bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
		args = nil // no arguments, which Parse turns into {}
	}
	var s string
	if json.Unmarshal(args, &s) == nil {
		if !json.Valid([]byte(s)) {
			return Call{}, errors.New("toolArgs is a string that does not hold JSON")
		}
		args = json.RawMessage(s)
	}
	return Call{Session: v.SessionID, Dir: v.Cwd, Tool: v.ToolName, Args: args}, nil
}

type copilotOutput struct {
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func answerCopilot(a gate.HookAnswer) []byte {
	return marshal(copilotOutput{decision(a), reason(a)})
}

// Antigravity CLI.

type antigravityInput struct {
	ToolCall struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
	ConversationID string   `json:"conversationId"`
	WorkspacePaths []string `json:"workspacePaths"`
}

func parseAntigravity(in []byte) (Call, error) {
	var v antigravityInput
	if err := json.Unmarshal(in, &v); err != nil {
		return Call{}, err
	}
	c := Call{Session: v.ConversationID, Tool: v.ToolCall.Name, Args: v.ToolCall.Args}
	if len(v.WorkspacePaths) > 0 {
		c.Dir = v.WorkspacePaths[0]
	}
	return c, nil
}

type antigravityOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func answerAntigravity(a gate.HookAnswer) []byte {
	return marshal(antigravityOutput{decision(a), reason(a)})
}
