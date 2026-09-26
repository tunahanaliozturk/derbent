package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// clean makes text that came from an agent, a tool or the database safe to draw. Control characters,
// which can move the cursor, set the clipboard or ring the bell, and bidirectional overrides, which can
// make text read as something it is not, are written as \u escapes; newlines and tabs as \n and \t, so
// one value is one line.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// clip cuts s to width terminal cells, ending a cut line with an ellipsis. s must already be clean.
func clip(s string, width int) string {
	return ansi.Truncate(s, max(width, 1), "…")
}
