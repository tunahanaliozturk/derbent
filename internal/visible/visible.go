// Package visible makes text that came from an agent, a tool or the database safe to show on a
// terminal. The UI and the commands that print stored text all use it, so they escape the same runes.
package visible

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Unsafe reports whether r must not reach a terminal as it is: a control character, which can move
// the cursor, set the clipboard or ring the bell; a format character, which is invisible (zero-width
// spaces and joiners, the soft hyphen, the word joiner, the byte order mark, tag characters) or
// reorders text (bidirectional controls); a line or paragraph separator; a variation selector; a
// letter or mark that draws as nothing or as a blank (blanks); or a value that is not a rune at all.
func Unsafe(r rune) bool {
	return !utf8.ValidRune(r) || unicode.IsControl(r) ||
		unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Variation_Selector) ||
		(r >= 0xE0000 && r <= 0xE007F) || // every tag character, assigned or not
		strings.ContainsRune(blanks, r)
}

// blanks are runes outside the categories above that a terminal draws as nothing or as a space: the
// four Hangul fillers, the combining grapheme joiner and the braille blank.
const blanks = "\u115f\u1160\u3164\uffa0\u034f\u2800"

// Escape returns s with every unsafe rune written as an escape: \n and \t for a newline and a tab, so
// one value stays one line, \uXXXX, or \UXXXXXXXX above U+FFFF, for the others, and \xNN for a byte
// that is not UTF-8.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case !Unsafe(r):
			b.WriteString(s[i : i+size])
		case r > 0xFFFF:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += size
	}
	return b.String()
}
