package tui

import "github.com/charmbracelet/x/ansi"

// clip cuts s to width terminal cells, ending a cut line with an ellipsis. s must already be escaped
// with visible.Escape.
func clip(s string, width int) string {
	return ansi.Truncate(s, max(width, 1), "…")
}
