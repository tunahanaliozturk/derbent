package gate

import (
	"context"
	"log/slog"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ServableName reports whether name can be offered to agents: 1 to 64 letters, digits, underscores and
// dashes, which every supported CLI accepts as a tool name.
func ServableName(name string) bool {
	return toolName.MatchString(name)
}

// LeftOut says why a downstream tool cannot be offered to agents under the gate name name, or returns ""
// when it can. SyncTools and `portcullis config check` both use it, so they never disagree.
func LeftOut(name string, t *mcp.Tool) string {
	switch {
	case !ServableName(name):
		return "not a tool name of at most 64 letters, digits, underscores or dashes"
	case !objectSchema(t.InputSchema):
		return "its input schema is not an object" // the SDK serves only tools with object schemas
	}
	return ""
}

// SyncTools makes one downstream server's tools available to the agent as <server>__<tool>. It leaves
// out tools the rules hide from this agent, tools whose name cannot be served, and tools whose input
// schema is not an object (the SDK refuses those), and it removes the server's tools that are gone.
// The SDK tells connected agents that the list changed. Call it after Server.
func (g *Gate) SyncTools(server string, tools []*mcp.Tool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owners == nil {
		g.owners = map[string]string{}
	}
	keep := map[string]bool{}
	for _, t := range tools {
		name := server + "__" + t.Name
		if why := LeftOut(name, t); why != "" {
			slog.Warn("portcullis: tool left out: "+why, "tool", name)
			continue
		}
		if !knobs.skipHiding && g.Rules.Hidden(g.Agent, name) {
			continue
		}
		keep[name] = true
		g.owners[name] = server
		g.server.AddTool(&mcp.Tool{
			Name: name, Title: t.Title, Description: t.Description,
			InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, Annotations: t.Annotations,
		}, g.forward(server, t.Name))
	}
	var gone []string
	for name, owner := range g.owners {
		if owner == server && !keep[name] {
			gone = append(gone, name)
			delete(g.owners, name)
		}
	}
	if len(gone) > 0 {
		g.server.RemoveTools(gone...)
	}
}

// forward returns the handler for one downstream tool. A server that cannot take the call gives the
// agent a tool error it can read, not a protocol error.
func (g *Gate) forward(server, tool string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if g.Forward == nil {
			return toolError("portcullis: no downstream servers are configured"), nil
		}
		res, err := g.Forward(ctx, server, tool, req.Params.Arguments)
		if err != nil {
			return toolError("portcullis: " + err.Error()), nil
		}
		return res, nil
	}
}

func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func objectSchema(schema any) bool {
	m, ok := schema.(map[string]any)
	return ok && m["type"] == "object"
}
