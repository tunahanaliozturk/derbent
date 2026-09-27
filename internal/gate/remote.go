package gate

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ServableName reports whether name can be offered to agents: 1 to 64 letters, digits, underscores and
// dashes, which every supported CLI accepts as a tool name.
func ServableName(name string) bool {
	return toolName.MatchString(name)
}

// LeftOut says why a downstream tool cannot be offered to agents under the gate name name, or returns ""
// when it can. SyncTools and `derbent config check` both use it, so they never disagree.
func LeftOut(name string, t *mcp.Tool) string {
	switch {
	case !ServableName(name):
		return "not a tool name of at most 64 letters, digits, underscores or dashes"
	case !objectSchema(t.InputSchema):
		return "its input schema is not an object" // the SDK serves only tools with object schemas
	}
	return ""
}

// withheldTool is a downstream tool kept from the agent because its definition changed since it was
// pinned, or because its pin could not be checked (unchecked).
type withheldTool struct {
	server    string
	tool      *mcp.Tool
	unchecked bool
}

// SyncTools makes one downstream server's tools available to the agent as <server>__<tool>. It leaves
// out tools whose name cannot be served and tools whose input schema is not an object (the SDK refuses
// those), withholds tools whose definition changed since they were pinned (ADR 0013), leaves out tools
// the rules hide from this agent, and removes the server's tools that are gone. The SDK tells connected
// agents that the list changed. Call it after Server.
func (g *Gate) SyncTools(ctx context.Context, server string, tools []*mcp.Tool) {
	servable := make([]*mcp.Tool, 0, len(tools))
	for _, t := range tools {
		name := server + "__" + t.Name
		if why := LeftOut(name, t); why != "" {
			slog.Warn("derbent: tool left out: "+why, "tool", name)
			continue
		}
		servable = append(servable, t)
	}
	// Pins are checked before the lock is taken: the check writes to the database, and a call that only
	// needs to know which tools are served should not wait for it.
	changed, checkErr := g.checkPins(ctx, server, servable)
	if checkErr != nil {
		slog.Warn("derbent: tools withheld because their pins could not be checked", "server", server, "err", checkErr)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owners == nil {
		g.owners = map[string]string{}
	}
	if g.withheld == nil {
		g.withheld = map[string]withheldTool{}
	}
	for name, w := range g.withheld { // this list replaces whatever was withheld from the server before
		if w.server == server {
			delete(g.withheld, name)
		}
	}
	keep := map[string]bool{}
	for _, t := range servable {
		name := server + "__" + t.Name
		switch {
		case !knobs.skipHiding && g.Rules.Hidden(g.Agent, name):
			// Refused by the rule that hides it, changed or not, exactly as before pins, so a refusal never
			// tells the agent the tool exists. Its pin was checked above all the same.
		case checkErr != nil:
			g.withheld[name] = withheldTool{server: server, tool: t, unchecked: true}
		case changed[t.Name]:
			g.withheld[name] = withheldTool{server: server, tool: t}
			slog.Warn("derbent: tool withheld: it changed since it was pinned; review it with derbent pins", "tool", name)
		default:
			g.serve(server, name, t)
			keep[name] = true
		}
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

// checkPins pins the server's tools on first sight and reports which changed since (ADR 0013). A gate
// without pins, or a server with pin = false, pins nothing and reports nothing changed.
func (g *Gate) checkPins(ctx context.Context, server string, tools []*mcp.Tool) (map[string]bool, error) {
	if g.Pins == nil || g.Unpinned[server] {
		return nil, nil
	}
	return g.Pins.Check(ctx, server, tools)
}

// serve lists t to the agent as name. The rules never hide a tool that reaches it: SyncTools leaves
// those out, and withholds none of them. g.mu must be held.
func (g *Gate) serve(server, name string, t *mcp.Tool) {
	g.owners[name] = server
	g.server.AddTool(&mcp.Tool{
		Name: name, Title: t.Title, Description: t.Description,
		InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, Annotations: t.Annotations,
	}, g.forward(server, t.Name))
}

// held returns the withheld tool of that gate name, if there is one.
func (g *Gate) held(name string) (withheldTool, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w, ok := g.withheld[name]
	return w, ok
}

// pinRecheck is how often WatchPins looks at the withheld tools.
const pinRecheck = 2 * time.Second

// WatchPins serves again, until ctx ends, each withheld tool whose new definition the user has accepted
// with derbent pins accept, or whose pin could at last be checked; the SDK tells the agent through
// list_changed. It looks every two seconds and does nothing while no tool is withheld. A server that
// goes back to the pinned definition says so with list_changed, which SyncTools handles.
func (g *Gate) WatchPins(ctx context.Context) {
	every := pinRecheck
	if knobs.pinRecheck > 0 {
		every = knobs.pinRecheck
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			g.recheckPins(ctx)
		}
	}
}

// recheckPins looks at the withheld tools again and serves each one that now matches its pin. The tools
// whose pins could not be checked are checked again, one transaction per server. A tool withheld because
// it changed is only compared with its pin, which writes nothing while a change is recorded (see
// pin.Store.Recheck): waiting gates take no write lock, and never replace the change the user reviews.
func (g *Gate) recheckPins(ctx context.Context) {
	g.mu.Lock()
	unchecked := map[string][]*mcp.Tool{}
	var differing []withheldTool
	for _, w := range g.withheld {
		if w.unchecked {
			unchecked[w.server] = append(unchecked[w.server], w.tool)
		} else {
			differing = append(differing, w)
		}
	}
	g.mu.Unlock()
	for server, tools := range unchecked {
		changed, err := g.checkPins(ctx, server, tools)
		if err != nil {
			continue // still withheld; SyncTools said why when it withheld them
		}
		for _, t := range tools {
			g.release(server, t, changed[t.Name])
		}
	}
	for _, w := range differing {
		if changed, err := g.Pins.Recheck(ctx, w.server, w.tool); err == nil {
			g.release(w.server, w.tool, changed)
		}
	}
}

// release serves a withheld tool that now matches its pin and keeps withholding one that differs,
// unless a newer list from its server took its place since recheckPins read it.
func (g *Gate) release(server string, t *mcp.Tool, changed bool) {
	name := server + "__" + t.Name
	g.mu.Lock()
	defer g.mu.Unlock()
	w, ok := g.withheld[name]
	switch {
	case !ok || w.tool != t:
	case changed:
		if w.unchecked { // SyncTools could not say so when it withheld the tool
			slog.Warn("derbent: tool withheld: it changed since it was pinned; review it with derbent pins", "tool", name)
		}
		w.unchecked = false
		g.withheld[name] = w
	default:
		delete(g.withheld, name)
		g.serve(server, name, t)
	}
}

// forward returns the handler for one downstream tool. A server that cannot take the call gives the
// agent a tool error it can read, not a protocol error.
func (g *Gate) forward(server, tool string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if g.Forward == nil {
			return toolError("derbent: no downstream servers are configured"), nil
		}
		res, err := g.Forward(ctx, server, tool, req.Params.Arguments)
		if err != nil {
			return toolError("derbent: " + err.Error()), nil
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
