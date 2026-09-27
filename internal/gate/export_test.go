package gate

import (
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SwitchOffRules lets every call through for the rest of the test.
func SwitchOffRules(t testing.TB) {
	knobs.skipRules = true
	t.Cleanup(func() { knobs.skipRules = false })
}

// SwitchOffHiding lists every tool for the rest of the test. Call it before Server.
func SwitchOffHiding(t testing.TB) {
	knobs.skipHiding = true
	t.Cleanup(func() { knobs.skipHiding = false })
}

// SwitchOffRedaction stores arguments unmasked for the rest of the test.
func SwitchOffRedaction(t testing.TB) {
	knobs.skipRedaction = true
	t.Cleanup(func() { knobs.skipRedaction = false })
}

// WhenWaitingForTools calls f each time a tool listing or call starts waiting for the downstream
// servers, for the rest of the test. Call it before Server.
func WhenWaitingForTools(t testing.TB, f func()) {
	knobs.waiting = f
	t.Cleanup(func() { knobs.waiting = nil })
}

// SetPinRecheck makes WatchPins look at the withheld tools every d, for the rest of the test. Call it
// before WatchPins starts.
func SetPinRecheck(t testing.TB, d time.Duration) {
	knobs.pinRecheck = d
	t.Cleanup(func() { knobs.pinRecheck = 0 })
}

// GateCalls is the middleware Server installs, so a test can hand it a request the SDK does not build.
func (g *Gate) GateCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return g.gateCalls(next)
}
