package visible_test

import (
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/visible"
)

func TestEscape(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain text stays", `{"title":"Fix login"}`, `{"title":"Fix login"}`},
		{"letters and emoji stay", "é 漢字 😀 \U0000fffd", "é 漢字 😀 \U0000fffd"},
		{"newline and tab", "a\nb\tc", `a\nb\tc`},
		{"escape sequence", "\x1b[2J", `\u001b[2J`},
		{"bell and delete", "\a\x7f", `\u0007\u007f`},
		{"C1 control", "\u009b2J", `\u009b2J`},
		{"bidirectional override", "\U0000202egnp.exe\U00002066", "\\u202egnp.exe\\u2066"},
		{"zero-width space and joiner", "a\U0000200bb\U0000200dc", "a\\u200bb\\u200dc"},
		{"soft hyphen, word joiner, byte order mark", "\U000000ad\U00002060\U0000feff", "\\u00ad\\u2060\\ufeff"},
		{"line and paragraph separators", "\U00002028\U00002029", "\\u2028\\u2029"},
		{"variation selectors", "\U00002764\U0000fe0f\U000e0100", "\U00002764\\ufe0f\\U000e0100"},
		{"tag characters", "\U000e0000\U000e0041\U000e007f", "\\U000e0000\\U000e0041\\U000e007f"},
		{"bytes that are not UTF-8", "a\xffb\xc3", `a\xffb\xc3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visible.Escape(tc.in); got != tc.want {
				t.Fatalf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestUnsafe(t *testing.T) {
	unsafe := []rune{
		0, '\n', 0x1b, 0x7f, 0x9b, 0x202e, 0x2066, 0x200b, 0x200d, 0xad, 0x2060, 0xfeff, 0x2028, 0x2029,
		0xfe0f, 0xe0000, 0xe0041, 0xe007f, 0xe0100, 0xd800, 0x110000,
	}
	for _, r := range unsafe {
		if !visible.Unsafe(r) {
			t.Errorf("Unsafe(%U) = false", r)
		}
	}
	for _, r := range []rune{'a', ' ', 'é', '漢', 0x1f600, 0xfffd, 0x2764} {
		if visible.Unsafe(r) {
			t.Errorf("Unsafe(%U) = true", r)
		}
	}
}
