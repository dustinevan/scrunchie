package cell

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestDisplayDistinguishesEmptyValues(t *testing.T) {
	column := result.Column{OID: 17}
	values := []string{
		Display(result.Cell{Null: true}, column, Auto),
		Display(result.Cell{}, result.Column{OID: 25}, Auto),
		Display(result.Cell{}, column, Auto),
		Display(result.Cell{Bytes: []byte{0}}, column, Auto),
	}
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[i] == values[j] {
				t.Errorf("values %d and %d both display as %q", i, j, values[i])
			}
		}
	}
}

func TestRawFormatsPreserveOriginalBytes(t *testing.T) {
	data := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	original := bytes.Clone(data)
	value := result.Cell{Text: `\x00112233445566778899aabbccddeeff`, Bytes: data}
	cases := []struct {
		format Format
		want   string
	}{
		{Auto, "0x" + hex.EncodeToString(data)},
		{Hex, "0x" + hex.EncodeToString(data)},
		{Base64, base64.StdEncoding.EncodeToString(data)},
		{UUID, "00112233-4455-6677-8899-aabbccddeeff"},
	}
	for _, test := range cases {
		got, err := Raw(value, result.Column{OID: 17}, test.format)
		if err != nil || got != test.want {
			t.Errorf("%s: got %q, %v; want %q", test.format, got, err, test.want)
		}
	}
	if !bytes.Equal(value.Bytes, original) || value.Text != `\x00112233445566778899aabbccddeeff` {
		t.Fatal("formatting changed the PostgreSQL value")
	}
}

func TestRawAutoDetectsJSONTextAndBinary(t *testing.T) {
	cases := []struct{ input, want string }{
		{` { "n": 9007199254740993, "n": 1e400 } `, `{"n":9007199254740993,"n":1e400}`},
		{"Καλημέρα\nworld", "Καλημέρα\nworld"},
		{"abcdefghijklmnop", "abcdefghijklmnop"},
		{string([]byte{0xff, 0x1b}), "0xff1b"},
	}
	for _, test := range cases {
		got, err := Raw(result.Cell{Bytes: []byte(test.input)}, result.Column{OID: 17}, Auto)
		if err != nil || got != test.want {
			t.Errorf("got %q, %v; want %q", got, err, test.want)
		}
	}
}

func TestRawRejectsInvalidInterpretations(t *testing.T) {
	cases := []struct {
		value  result.Cell
		column result.Column
		format Format
	}{
		{result.Cell{Bytes: []byte{0xff}}, result.Column{OID: 17}, Text},
		{result.Cell{Bytes: []byte{0xff}}, result.Column{OID: 17}, JSON},
		{result.Cell{Text: "{"}, result.Column{}, JSON},
		{result.Cell{Bytes: []byte{1, 2, 3}}, result.Column{OID: 17}, UUID},
		{result.Cell{Text: "abcdefghijklmnop"}, result.Column{}, UUID},
		{result.Cell{Text: strings.Repeat("z", 32)}, result.Column{}, UUID},
		{result.Cell{Text: "00112233_4455-6677-8899-aabbccddeeff"}, result.Column{}, UUID},
	}
	for _, test := range cases {
		if _, err := Raw(test.value, test.column, test.format); err == nil {
			t.Errorf("%s accepted an invalid interpretation", test.format)
		}
		if !strings.Contains(Display(test.value, test.column, test.format), "formatting") {
			t.Errorf("%s did not display its error", test.format)
		}
	}
}

func TestDisplayBoundsLargePreviews(t *testing.T) {
	plain := strings.Repeat("é", 100_000)
	jsonText := `{"text":"` + plain + `"}`
	for _, format := range []Format{Auto, Text, JSON, Hex, Base64} {
		value := result.Cell{Bytes: []byte(jsonText)}
		got := Display(value, result.Column{OID: 17}, format)
		if len(got) > 4096 || !utf8.ValidString(got) || !strings.Contains(got, "bytes)") {
			t.Errorf("%s preview is not bounded, valid UTF-8, and marked: %d bytes", format, len(got))
		}
		if string(value.Bytes) != jsonText {
			t.Errorf("%s preview changed the value", format)
		}
	}
	got, err := Raw(result.Cell{Text: plain}, result.Column{}, Text)
	if err != nil || got != plain {
		t.Fatal("explicit raw text was shortened")
	}
	attack := result.Cell{Text: strings.Repeat("\x1b", 1024)}
	if got := Display(attack, result.Column{}, Text); len(got) > 4096 || strings.ContainsRune(got, '\x1b') {
		t.Fatal("large control-character preview was not safely bounded")
	}
}

func TestRawJSONRetainsNumbersAndRejectsExcessiveExpansion(t *testing.T) {
	input := `{"n":9007199254740993,"n":1e400}`
	got, err := Raw(result.Cell{Text: input}, result.Column{}, JSON)
	if err != nil || !strings.Contains(got, "9007199254740993") || !strings.Contains(got, "1e400") || strings.Count(got, `"n"`) != 2 {
		t.Fatalf("JSON changed values: %q, %v", got, err)
	}
	inputs := []string{
		strings.Repeat(" ", MaxJSONBytes+1),
		strings.Repeat("[", MaxJSONDepth+1) + "0" + strings.Repeat("]", MaxJSONDepth+1),
		strings.Repeat("[", MaxJSONDepth) + strings.Repeat("0,", 100_000) + "0" + strings.Repeat("]", MaxJSONDepth),
	}
	for _, input := range inputs {
		if _, err := Raw(result.Cell{Text: input}, result.Column{}, JSON); err == nil {
			t.Errorf("accepted excessive JSON input of %d bytes", len(input))
		}
	}
}
