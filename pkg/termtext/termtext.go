// Package termtext escapes untrusted values before they reach a terminal.
package termtext

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Safe renders control characters and invalid UTF-8 visibly on a single line.
// It preserves ordinary Unicode, including joined emoji.
func Safe(text string) string {
	return strings.ReplaceAll(SafeMultiline(text), "\n", `\n`)
}

// SafeMultiline preserves newlines and renders all other C0/C1 controls, DEL,
// bidirectional controls, Unicode line separators, and invalid UTF-8 visibly.
func SafeMultiline(text string) string {
	var safe strings.Builder
	safe.Grow(len(text))
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&safe, `\x%02x`, text[0])
			text = text[1:]
			continue
		}
		text = text[size:]
		switch r {
		case '\n':
			safe.WriteByte('\n')
		case '\r':
			safe.WriteString(`\r`)
		case '\t':
			safe.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&safe, `\x%02x`, r)
			} else if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r >= 0x206a && r <= 0x206f || r == '\u2028' || r == '\u2029' {
				fmt.Fprintf(&safe, `\u%04x`, r)
			} else {
				safe.WriteRune(r)
			}
		}
	}
	return safe.String()
}
