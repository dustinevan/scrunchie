package termtext

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestSafePreservesUnicodeAndJoinedEmoji(t *testing.T) {
	text := "café Ελληνικά 日本語 👩‍💻 👨‍👩‍👦"
	if got := Safe(text); got != text {
		t.Errorf("got %q, want %q", got, text)
	}
}

func TestSafeEscapesTerminalAndDirectionalControls(t *testing.T) {
	text := "\x1b]52;c;ZXhmaWx0cmF0ZQ==\a\x1b[2J\r\t\n\x7f\u009b\u061c\u200e\u202e\u2067\u206f\u2028\u2029"
	got := Safe(text)
	for _, r := range got {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u206f' || r == '\u2028' || r == '\u2029' {
			t.Errorf("unsafe control U+%04X survived", r)
		}
	}
	for _, escape := range []string{`\x1b`, `\x07`, `\r`, `\t`, `\n`, `\x7f`, `\u009b`, `\u202e`} {
		if !strings.Contains(got, escape) {
			t.Errorf("missing visible escape %q", escape)
		}
	}
}

func TestSafeEscapesEveryC0AndC1Control(t *testing.T) {
	for r := rune(0); r <= 0x9f; r++ {
		if r > 0x1f && r < 0x7f {
			continue
		}
		if got := Safe(string(r)); strings.ContainsRune(got, r) || got == "" {
			t.Errorf("control U+%04X not rendered visibly: %q", r, got)
		}
	}
}

func TestSafeMultilineOnlyPreservesLineFeeds(t *testing.T) {
	if got := SafeMultiline("a\r\nb\tc\v"); got != "a\\r\nb\\tc\\x0b" {
		t.Errorf("got %q", got)
	}
}

func TestSafeRetainsInvalidByteIdentity(t *testing.T) {
	got := Safe(string([]byte{'a', 0xff, 0xc0, 'b'}))
	if got != `a\xff\xc0b` || !utf8.ValidString(got) {
		t.Errorf("got %q", got)
	}
}
