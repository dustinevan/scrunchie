package cell

import (
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dustinevan/scrunchie/pkg/result"
)

// Table is a rectangle of result values in display order for one explicit
// copy. Rows shorter than Columns are treated as SQL NULL in missing cells.
// Encoders use PostgreSQL's text for each value, with bytea as \x hex, and
// ignore grid display formats.
type Table struct {
	Columns []result.Column
	Rows    [][]result.Cell
}

// TSV writes a header line and tab-separated rows without a final newline.
// A field containing a tab, line break, or double quote, or a value that is
// literally \N, is double-quoted with doubled quotes; SQL NULL is bare \N.
func TSV(table Table) string {
	var output strings.Builder
	for i, column := range table.Columns {
		if i > 0 {
			output.WriteByte('\t')
		}
		output.WriteString(tsvField(column.Name))
	}
	for _, row := range table.Rows {
		output.WriteByte('\n')
		for i, column := range table.Columns {
			if i > 0 {
				output.WriteByte('\t')
			}
			if i >= len(row) || row[i].Null {
				output.WriteString(`\N`)
				continue
			}
			output.WriteString(tsvField(pgText(row[i], column)))
		}
	}
	return output.String()
}

func tsvField(text string) string {
	if text == `\N` || strings.ContainsAny(text, "\t\n\r\"") {
		return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	}
	return text
}

// Markdown writes a GitHub-flavored table. Backslashes and pipes are escaped,
// line breaks become <br>, numeric columns are right-aligned, and SQL NULL is
// written as NULL. There is no final newline.
func Markdown(table Table) string {
	var output strings.Builder
	output.WriteByte('|')
	for _, column := range table.Columns {
		output.WriteString(" " + markdownText(column.Name) + " |")
	}
	output.WriteString("\n|")
	for _, column := range table.Columns {
		kind := orderFor(column)
		if kind == byDecimal || kind == byFloat {
			output.WriteString(" ---: |")
		} else {
			output.WriteString(" --- |")
		}
	}
	for _, row := range table.Rows {
		output.WriteString("\n|")
		for i, column := range table.Columns {
			text := "NULL"
			if i < len(row) && !row[i].Null {
				text = markdownText(pgText(row[i], column))
			}
			output.WriteString(" " + text + " |")
		}
	}
	return output.String()
}

func markdownText(text string) string {
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, "|", `\|`)
	text = strings.ReplaceAll(text, "\r\n", "<br>")
	text = strings.ReplaceAll(text, "\n", "<br>")
	return strings.ReplaceAll(text, "\r", "<br>")
}

// JSONObjects writes an array with one object per row, keys in column order.
// Values are PostgreSQL text strings, like the JSON export, and SQL NULL is
// null. A repeated column name gets the first unused suffix: id, id_2, id_3.
func JSONObjects(table Table) string {
	names := make(map[string]bool, len(table.Columns))
	for _, column := range table.Columns {
		names[column.Name] = true
	}
	keys := make([]string, len(table.Columns))
	used := make(map[string]bool, len(table.Columns))
	for i, column := range table.Columns {
		key := column.Name
		if used[key] {
			for suffix := 2; ; suffix++ {
				candidate := column.Name + "_" + strconv.Itoa(suffix)
				if !used[candidate] && !names[candidate] {
					key = candidate
					break
				}
			}
		}
		used[key] = true
		keys[i] = jsonString(key)
	}
	if len(table.Rows) == 0 {
		return "[]"
	}
	var output strings.Builder
	output.WriteString("[")
	for r, row := range table.Rows {
		if r > 0 {
			output.WriteByte(',')
		}
		output.WriteString("\n  {")
		for i, column := range table.Columns {
			if i > 0 {
				output.WriteString(", ")
			}
			output.WriteString(keys[i] + ": ")
			if i >= len(row) || row[i].Null {
				output.WriteString("null")
			} else {
				output.WriteString(jsonString(pgText(row[i], column)))
			}
		}
		output.WriteByte('}')
	}
	output.WriteString("\n]")
	return output.String()
}

// jsonString quotes text for JSON without HTML escaping. Invalid UTF-8
// becomes U+FFFD, as encoding/json does.
func jsonString(text string) string {
	var output strings.Builder
	output.Grow(len(text) + 2)
	output.WriteByte('"')
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		switch {
		case r == '"':
			output.WriteString(`\"`)
		case r == '\\':
			output.WriteString(`\\`)
		case r == '\n':
			output.WriteString(`\n`)
		case r == '\r':
			output.WriteString(`\r`)
		case r == '\t':
			output.WriteString(`\t`)
		case r < 0x20:
			output.WriteString(`\u00`)
			output.WriteString(hex.EncodeToString([]byte{byte(r)}))
		case r == utf8.RuneError && size == 1:
			output.WriteString(`�`)
		default:
			output.WriteRune(r)
		}
	}
	output.WriteByte('"')
	return output.String()
}

// Inserts writes one INSERT statement per row. relation holds unquoted name
// parts such as schema and table; without one the table is table_name, a
// placeholder to edit. Identifiers are quoted only when PostgreSQL needs it.
func Inserts(table Table, relation []string, backslashEscapes bool) string {
	target := "table_name"
	if len(relation) > 0 {
		parts := make([]string, len(relation))
		for i, part := range relation {
			parts[i] = Identifier(part)
		}
		target = strings.Join(parts, ".")
	}
	names := make([]string, len(table.Columns))
	for i, column := range table.Columns {
		names[i] = Identifier(column.Name)
	}
	prefix := "INSERT INTO " + target + " (" + strings.Join(names, ", ") + ") VALUES ("
	var output strings.Builder
	for r, row := range table.Rows {
		if r > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(prefix)
		for i, column := range table.Columns {
			if i > 0 {
				output.WriteString(", ")
			}
			value := result.Cell{Null: true}
			if i < len(row) {
				value = row[i]
			}
			output.WriteString(Literal(value, column, backslashEscapes))
		}
		output.WriteString(");")
	}
	return output.String()
}

// InList writes values as a parenthesized, comma-separated SQL list.
func InList(values []result.Cell, column result.Column, backslashEscapes bool) string {
	literals := make([]string, len(values))
	for i, value := range values {
		literals[i] = Literal(value, column, backslashEscapes)
	}
	return "(" + strings.Join(literals, ", ") + ")"
}

// Literal writes a SQL constant for a result value. Integers, numeric, and
// finite floats are bare, except float -0, which a bare constant would make 0;
// booleans are TRUE or FALSE; bytea is E'\\x…'::bytea; everything else is a
// quoted string that PostgreSQL coerces to the target type. A string with a
// backslash is an E-string with doubled backslashes, so the SQL means the
// same under either standard_conforming_strings setting; backslashEscapes no
// longer changes the output.
func Literal(value result.Cell, column result.Column, backslashEscapes bool) string {
	if value.Null {
		return "NULL"
	}
	kind := orderFor(column)
	switch kind {
	case byBool:
		if parsed, ok := parseBool(value.Text); ok {
			if parsed {
				return "TRUE"
			}
			return "FALSE"
		}
	case byDecimal, byFloat:
		negativeZero := false
		if kind == byFloat {
			parsed, err := strconv.ParseFloat(value.Text, 64)
			negativeZero = err == nil && parsed == 0 && math.Signbit(parsed)
		}
		if numericLiteral(value.Text) && !negativeZero {
			return value.Text
		}
	case byBytes:
		return quote(`\x`+hex.EncodeToString(value.Bytes)) + "::bytea"
	}
	return quote(value.Text)
}

// quote writes an ordinary string constant, or an E-string when text has a
// backslash, whose meaning would otherwise depend on the session.
func quote(text string) string {
	text = strings.ReplaceAll(text, "'", "''")
	if strings.Contains(text, `\`) {
		return "E'" + strings.ReplaceAll(text, `\`, `\\`) + "'"
	}
	return "'" + text + "'"
}

// numericLiteral accepts [-]digits[.digits][e[+-]digits], rejecting NaN and
// Infinity, which are valid values but not bare SQL constants.
func numericLiteral(text string) bool {
	i := 0
	if i < len(text) && text[i] == '-' {
		i++
	}
	digits := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
		digits++
	}
	if i < len(text) && text[i] == '.' {
		i++
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		exponent := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if i == exponent {
			return false
		}
	}
	return i == len(text)
}

// Identifier returns name unchanged when PostgreSQL would read it back as the
// same identifier unquoted, and otherwise double-quotes it.
func Identifier(name string) string {
	plain := name != "" && !reservedWords[name]
	for i := 0; i < len(name) && plain; i++ {
		c := name[i]
		plain = c >= 'a' && c <= 'z' || c == '_' || i > 0 && (c >= '0' && c <= '9' || c == '$')
	}
	if plain {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// reservedWords are PostgreSQL's reserved and type/function-name keywords,
// which cannot be bare column or table names.
var reservedWords = map[string]bool{
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true, "array": true, "as": true, "asc": true,
	"asymmetric": true, "authorization": true, "binary": true, "both": true, "case": true, "cast": true, "check": true,
	"collate": true, "collation": true, "column": true, "concurrently": true, "constraint": true, "create": true,
	"cross": true, "current_catalog": true, "current_date": true, "current_role": true, "current_schema": true,
	"current_time": true, "current_timestamp": true, "current_user": true, "default": true, "deferrable": true,
	"desc": true, "distinct": true, "do": true, "else": true, "end": true, "except": true, "false": true, "fetch": true,
	"for": true, "foreign": true, "freeze": true, "from": true, "full": true, "grant": true, "group": true, "having": true,
	"ilike": true, "in": true, "initially": true, "inner": true, "intersect": true, "into": true, "is": true,
	"isnull": true, "join": true, "lateral": true, "leading": true, "left": true, "like": true, "limit": true,
	"localtime": true, "localtimestamp": true, "natural": true, "not": true, "notnull": true, "null": true,
	"offset": true, "on": true, "only": true, "or": true, "order": true, "outer": true, "overlaps": true,
	"placing": true, "primary": true, "references": true, "returning": true, "right": true, "select": true,
	"session_user": true, "similar": true, "some": true, "symmetric": true, "system_user": true, "table": true,
	"tablesample": true, "then": true, "to": true, "trailing": true, "true": true, "union": true, "unique": true,
	"user": true, "using": true, "variadic": true, "verbose": true, "when": true, "where": true, "window": true,
	"with": true,
}

// pgText is the value's PostgreSQL text, with bytea always as \x hex.
func pgText(value result.Cell, column result.Column) string {
	if orderFor(column) == byBytes || strings.EqualFold(column.Type, "bytea") {
		return `\x` + hex.EncodeToString(value.Bytes)
	}
	return value.Text
}
