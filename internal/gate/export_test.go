package gate

import "testing"

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
