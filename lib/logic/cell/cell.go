// Package cell renders result values without changing their PostgreSQL data.
package cell

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// Format selects how the original text or decoded bytea is interpreted.
type Format string

const (
	Auto   Format = "auto"
	Text   Format = "text"
	JSON   Format = "json"
	Hex    Format = "hex"
	Base64 Format = "base64"
	UUID   Format = "uuid"
)

const (
	MaxJSONBytes       = 16 << 20
	MaxJSONDepth       = 256
	MaxJSONOutputBytes = 32 << 20
)

// Formats returns the display choices in their keyboard cycling order.
func Formats() []Format {
	return []Format{Auto, Text, JSON, Hex, Base64, UUID}
}

// Next returns the next choice, wrapping to Auto for UUID or an unknown choice.
func Next(format Format) Format {
	switch format {
	case "", Auto:
		return Text
	case Text:
		return JSON
	case JSON:
		return Hex
	case Hex:
		return Base64
	case Base64:
		return UUID
	default:
		return Auto
	}
}

// Display returns one terminal-safe line. SQL NULL, empty text, and empty bytea
// have distinct labels. An invalid interpretation is an error label, not data.
func Display(value result.Cell, column result.Column, format Format) string {
	if value.Null {
		return "NULL"
	}
	isBytes := column.OID == 17 || strings.EqualFold(column.Type, "bytea")
	length := len(value.Text)
	if isBytes {
		length = len(value.Bytes)
	}
	if length > 512 {
		return preview(value, isBytes, format)
	}
	formatted, err := Raw(value, column, format)
	if err != nil {
		return "⟨" + termtext.Safe(err.Error()) + "⟩"
	}
	if isBytes && len(value.Bytes) == 0 {
		return "⟨empty bytea⟩"
	}
	if !isBytes && value.Text == "" {
		return `""`
	}
	if format == JSON {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(formatted)); err != nil {
			return "⟨formatting JSON: " + termtext.Safe(err.Error()) + "⟩"
		}
		formatted = compact.String()
	}
	return termtext.Safe(formatted)
}

// preview bounds display allocations for large cells. Copy and inspection
// still use the original value; the suffix makes the shortened display clear.
func preview(value result.Cell, isBytes bool, format Format) string {
	var data []byte
	if isBytes {
		data = value.Bytes
	} else {
		data = []byte(value.Text)
	}
	if format == "" || format == Auto {
		format = Text
		if isBytes {
			if !utf8.Valid(data) {
				format = Hex
			} else {
				for _, r := range string(data) {
					if r != '\n' && r != '\r' && r != '\t' && !unicode.IsPrint(r) {
						format = Hex
						break
					}
				}
			}
		}
	}
	var text string
	switch format {
	case Text, JSON:
		if !utf8.Valid(data) {
			return "⟨formatting text: invalid UTF-8; use hex or base64⟩"
		}
		if format == JSON && !json.Valid(data) {
			return "⟨formatting JSON: invalid JSON⟩"
		}
		end := 512
		for end > 0 && !utf8.RuneStart(data[end]) {
			end--
		}
		text = string(data[:end])
	case Hex:
		text = "0x" + hex.EncodeToString(data[:256])
	case Base64:
		text = base64.StdEncoding.EncodeToString(data[:384])
	case UUID:
		return "⟨formatting UUID: expected 16 bytes or a UUID with 32 hexadecimal digits⟩"
	default:
		return "⟨formatting value: unknown format⟩"
	}
	return termtext.Safe(text) + fmt.Sprintf("… (%d bytes)", len(data))
}

// Raw returns the requested representation without terminal escaping. It is
// intended for explicit copy/export and must be escaped before terminal output.
// SQL NULL returns "NULL"; exporters must use Cell.Null to retain its type.
// JSON is indented without decoding numbers or reordering object members.
func Raw(value result.Cell, column result.Column, format Format) (string, error) {
	if value.Null {
		return "NULL", nil
	}
	isBytes := column.OID == 17 || strings.EqualFold(column.Type, "bytea")
	data := []byte(value.Text)
	if isBytes {
		data = value.Bytes
	}
	switch format {
	case "", Auto:
		if !isBytes {
			if !utf8.Valid(data) {
				return "", fmt.Errorf("formatting text: invalid UTF-8; use hex or base64")
			}
			return value.Text, nil
		}
		if utf8.Valid(data) && json.Valid(data) {
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				return "", fmt.Errorf("formatting JSON: %w", err)
			}
			return compact.String(), nil
		}
		printable := utf8.Valid(data)
		if printable {
			for _, r := range string(data) {
				if r != '\n' && r != '\r' && r != '\t' && !unicode.IsPrint(r) {
					printable = false
					break
				}
			}
		}
		if printable {
			return string(data), nil
		}
		return "0x" + hex.EncodeToString(data), nil
	case Text:
		if !utf8.Valid(data) {
			return "", fmt.Errorf("formatting text: invalid UTF-8; use hex or base64")
		}
		return string(data), nil
	case JSON:
		if len(data) > MaxJSONBytes {
			return "", fmt.Errorf("formatting JSON: document exceeds %d bytes", MaxJSONBytes)
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("formatting JSON: invalid UTF-8")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return "", fmt.Errorf("formatting JSON: %w", err)
		}
		compactBytes := compact.Bytes()
		outputBytes := len(compactBytes)
		depth := 0
		inString := false
		for i := 0; i < len(compactBytes); i++ {
			c := compactBytes[i]
			if inString {
				if c == '\\' {
					i++
				} else if c == '"' {
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '{', '[':
				depth++
				if depth > MaxJSONDepth {
					return "", fmt.Errorf("formatting JSON: nesting exceeds %d levels", MaxJSONDepth)
				}
				if compactBytes[i+1] != '}' && compactBytes[i+1] != ']' {
					outputBytes += 1 + 2*depth
				}
			case '}', ']':
				depth--
				if compactBytes[i-1] != '{' && compactBytes[i-1] != '[' {
					outputBytes += 1 + 2*depth
				}
			case ',':
				outputBytes += 1 + 2*depth
			case ':':
				outputBytes++
			}
			if outputBytes > MaxJSONOutputBytes {
				return "", fmt.Errorf("formatting JSON: indented output exceeds %d bytes", MaxJSONOutputBytes)
			}
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, compactBytes, "", "  "); err != nil {
			return "", fmt.Errorf("formatting JSON: %w", err)
		}
		return pretty.String(), nil
	case Hex:
		return "0x" + hex.EncodeToString(data), nil
	case Base64:
		return base64.StdEncoding.EncodeToString(data), nil
	case UUID:
		uuidBytes := data
		if !isBytes || len(uuidBytes) != 16 {
			uuidText := string(data)
			if len(uuidText) == 36 {
				if uuidText[8] != '-' || uuidText[13] != '-' || uuidText[18] != '-' || uuidText[23] != '-' {
					return "", fmt.Errorf("formatting UUID: expected 16 bytes or a UUID with 32 hexadecimal digits")
				}
				uuidText = uuidText[:8] + uuidText[9:13] + uuidText[14:18] + uuidText[19:23] + uuidText[24:]
			}
			if len(uuidText) != 32 {
				return "", fmt.Errorf("formatting UUID: expected 16 bytes or a UUID with 32 hexadecimal digits")
			}
			decoded, err := hex.DecodeString(uuidText)
			if err != nil {
				return "", fmt.Errorf("formatting UUID: %w", err)
			}
			uuidBytes = decoded
		}
		digits := hex.EncodeToString(uuidBytes)
		return digits[:8] + "-" + digits[8:12] + "-" + digits[12:16] + "-" + digits[16:20] + "-" + digits[20:], nil
	default:
		return "", fmt.Errorf("formatting value: unknown format %q", format)
	}
}
