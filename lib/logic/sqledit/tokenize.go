// Package sqledit classifies and formats PostgreSQL text without changing SQL tokens.
package sqledit

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Kind identifies a lexical category for editor highlighting.
type Kind uint8

const (
	Whitespace Kind = iota
	Keyword
	Identifier
	String
	Number
	Comment
	Operator
)

const (
	MaxInputBytes  = 16 << 20
	MaxOutputBytes = 32 << 20
	MaxTokens      = 262_144
	MaxDepth       = 256
)

// Token retains an exact slice of the original input, including comments and
// whitespace. Joining every Text always reproduces the input byte for byte.
type Token struct {
	Text string
	Kind Kind
}

// Tokenize accepts incomplete editor text. Beyond an input/token limit, or for
// invalid UTF-8, the remaining text is one Identifier token. Formatting applies
// stricter checks and reports errors rather than interpreting uncertain text.
func Tokenize(sql string) []Token {
	tokens, err := scan(sql, MaxInputBytes, unknownStrings)
	if err != nil {
		return tokens
	}
	return tokens
}

// stringMode says how ordinary quoted strings treat backslashes. Without a
// session, unknownStrings reports any backslash in an ordinary string.
type stringMode uint8

const (
	unknownStrings  stringMode = iota
	standardStrings            // standard_conforming_strings = on
	escapeStrings              // standard_conforming_strings = off
)

func scan(sql string, maxBytes int, mode stringMode) ([]Token, error) {
	if len(sql) > maxBytes {
		return []Token{{Text: sql, Kind: Identifier}}, fmt.Errorf("input exceeds %d bytes", maxBytes)
	}
	if !utf8.ValidString(sql) {
		return []Token{{Text: sql, Kind: Identifier}}, fmt.Errorf("input is not valid UTF-8")
	}
	if strings.IndexByte(sql, 0) >= 0 {
		return []Token{{Text: sql, Kind: Identifier}}, fmt.Errorf("input contains a zero byte")
	}
	var tokens []Token
	var firstError error
	// PostgreSQL continues a literal across whitespace containing a newline;
	// the continuation keeps an E-string's escapes. This is the index of the
	// last escape-processed literal, or -1 once other SQL follows it.
	lastEscaped := -1
	for i := 0; i < len(sql); {
		if len(tokens) >= MaxTokens-1 {
			tokens = append(tokens, Token{Text: sql[i:], Kind: Identifier})
			if firstError == nil {
				firstError = fmt.Errorf("input exceeds %d tokens", MaxTokens)
			}
			break
		}
		start := i
		kind := Operator
		quoteAt := -1
		escaped := false
		ordinary := false
		if sql[i] == '\'' || sql[i] == '"' {
			quoteAt = i
			ordinary = sql[i] == '\''
		} else if i+1 < len(sql) && sql[i+1] == '\'' && strings.IndexByte("eEnNbBxX", sql[i]) >= 0 {
			quoteAt = i + 1
			escaped = sql[i] == 'E' || sql[i] == 'e'
			ordinary = sql[i] == 'N' || sql[i] == 'n'
		} else if i+2 < len(sql) && (sql[i] == 'U' || sql[i] == 'u') && sql[i+1] == '&' && (sql[i+2] == '\'' || sql[i+2] == '"') {
			quoteAt = i + 2
		}
		if mode == escapeStrings && ordinary {
			escaped = true
		}
		if mode != unknownStrings && quoteAt == i && sql[i] == '\'' && lastEscaped >= 0 {
			newline, continued := false, true
			for _, token := range tokens[lastEscaped+1:] {
				if token.Kind == Whitespace {
					newline = newline || strings.ContainsAny(token.Text, "\n\r")
				} else if token.Kind != Comment || !strings.HasPrefix(token.Text, "--") {
					continued = false
				}
			}
			escaped = escaped || newline && continued
		}
		switch {
		case strings.IndexByte(" \t\n\r\f", sql[i]) >= 0:
			kind = Whitespace
			for i < len(sql) && strings.IndexByte(" \t\n\r\f", sql[i]) >= 0 {
				i++
			}
		case strings.HasPrefix(sql[i:], "--"):
			kind = Comment
			i += 2
			for i < len(sql) && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
		case strings.HasPrefix(sql[i:], "/*"):
			kind = Comment
			i += 2
			depth := 1
			for i < len(sql) && depth > 0 {
				if strings.HasPrefix(sql[i:], "/*") {
					depth++
					i += 2
					if depth > MaxDepth && firstError == nil {
						firstError = fmt.Errorf("comment nesting exceeds %d levels", MaxDepth)
					}
				} else if strings.HasPrefix(sql[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 && firstError == nil {
				firstError = fmt.Errorf("unterminated block comment at byte %d", start)
			}
		case quoteAt >= 0:
			kind = String
			quote := sql[quoteAt]
			if quote == '"' {
				kind = Identifier
			}
			i = quoteAt + 1
			closed := false
			for i < len(sql) {
				if sql[i] == '\\' {
					if ordinary && mode == unknownStrings && firstError == nil {
						firstError = fmt.Errorf("ordinary string at byte %d contains backslashes; its interpretation depends on standard_conforming_strings or an E-string continuation", start)
					}
					if escaped && i+1 < len(sql) {
						i += 2
						continue
					}
				}
				if sql[i] != quote {
					i++
					continue
				}
				i++
				if i < len(sql) && sql[i] == quote {
					i++
					continue
				}
				closed = true
				break
			}
			if !closed && firstError == nil {
				firstError = fmt.Errorf("unterminated quoted value at byte %d", start)
			}
		case sql[i] == '$':
			if i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
				kind = Identifier
				i += 2
				for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
					i++
				}
				if i < len(sql) && identifierStart(sql[i]) && firstError == nil {
					firstError = fmt.Errorf("unexpected text after parameter at byte %d", start)
				}
				break
			}
			end := i + 1
			if end < len(sql) && identifierStart(sql[end]) {
				end++
				for end < len(sql) && (identifierStart(sql[end]) || sql[end] >= '0' && sql[end] <= '9') {
					end++
				}
			}
			if end >= len(sql) || sql[end] != '$' {
				i++
				if firstError == nil {
					firstError = fmt.Errorf("unrecognized dollar construct at byte %d", start)
				}
				break
			}
			kind = String
			delimiter := sql[i : end+1]
			closing := strings.Index(sql[end+1:], delimiter)
			if closing < 0 {
				i = len(sql)
				if firstError == nil {
					firstError = fmt.Errorf("unterminated dollar quote at byte %d", start)
				}
			} else {
				i = end + 1 + closing + len(delimiter)
			}
		case sql[i] >= '0' && sql[i] <= '9' || sql[i] == '.' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9':
			kind = Number
			end, err := numberEnd(sql, i)
			i = end
			if err != nil && firstError == nil {
				firstError = err
			}
		case identifierStart(sql[i]):
			kind = Identifier
			i++
			for i < len(sql) && (identifierStart(sql[i]) || sql[i] >= '0' && sql[i] <= '9' || sql[i] == '$') {
				i++
			}
			switch strings.ToUpper(sql[start:i]) {
			case "ALL", "ALTER", "ANALYSE", "ANALYZE", "AND", "ANY", "ARRAY", "AS", "ASC", "ASYMMETRIC", "AT", "AUTHORIZATION",
				"BEGIN", "BETWEEN", "BIGINT", "BINARY", "BIT", "BOOLEAN", "BOTH", "BY",
				"CALL", "CASCADE", "CASE", "CAST", "CHAR", "CHARACTER", "CHECK", "COLLATE", "COLUMN", "COMMENT", "COMMIT", "CONCURRENTLY", "CONFLICT", "CONSTRAINT", "COPY", "CREATE", "CROSS", "CURRENT", "CURRENT_DATE", "CURRENT_ROLE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER",
				"DATABASE", "DATE", "DAY", "DECIMAL", "DEFAULT", "DEFERRABLE", "DELETE", "DESC", "DISTINCT", "DO", "DOUBLE", "DROP",
				"ELSE", "END", "ESCAPE", "EXCEPT", "EXCLUDE", "EXCLUDED", "EXECUTE", "EXISTS", "EXPLAIN", "EXTRACT",
				"FALSE", "FETCH", "FILTER", "FIRST", "FLOAT", "FOLLOWING", "FOR", "FOREIGN", "FROM", "FULL", "FUNCTION",
				"GENERATED", "GRANT", "GROUP", "GROUPING", "GROUPS", "HAVING", "HOUR", "IDENTITY", "IF", "ILIKE", "IN", "INCLUDING", "INDEX", "INHERITS", "INITIALLY", "INNER", "INSERT", "INT", "INTEGER", "INTERSECT", "INTERVAL", "INTO", "IS", "ISNULL",
				"JOIN", "KEY", "LATERAL", "LEADING", "LEFT", "LIKE", "LIMIT", "LOCAL", "LOCK", "MATERIALIZED", "MERGE", "MINUTE", "MONTH", "NATURAL", "NEXT", "NO", "NOT", "NOTHING", "NOTNULL", "NULL", "NULLS", "NUMERIC",
				"OF", "OFFSET", "ON", "ONLY", "OPERATOR", "OR", "ORDER", "OUTER", "OVER", "OVERLAPS", "PARTITION", "PLACING", "PRECEDING", "PRECISION", "PRIMARY", "PROCEDURE",
				"RANGE", "REAL", "RECURSIVE", "REFERENCES", "REINDEX", "RELEASE", "REPLACE", "RESET", "RESTRICT", "RETURN", "RETURNING", "REVOKE", "RIGHT", "ROLLBACK", "ROW", "ROWS",
				"SAVEPOINT", "SCHEMA", "SECOND", "SELECT", "SESSION_USER", "SET", "SETOF", "SHARE", "SHOW", "SIMILAR", "SMALLINT", "SOME", "START", "SYMMETRIC",
				"TABLE", "TABLESAMPLE", "TEMP", "TEMPORARY", "THEN", "TIME", "TIMESTAMP", "TO", "TRAILING", "TRANSACTION", "TRIGGER", "TRUE", "TRUNCATE", "TYPE",
				"UESCAPE", "UNBOUNDED", "UNION", "UNIQUE", "UNKNOWN", "UNLOGGED", "UPDATE", "USER", "USING", "VACUUM", "VALUES", "VARCHAR", "VARIADIC", "VERBOSE", "VIEW", "WHEN", "WHERE", "WINDOW", "WITH", "WITHIN", "WITHOUT", "YEAR", "ZONE":
				kind = Keyword
			}
		case sql[i] == ':':
			i++
			if i < len(sql) && (sql[i] == ':' || sql[i] == '=') {
				i++
			}
		case strings.IndexByte("(),;[].", sql[i]) >= 0:
			i++
		case strings.IndexByte("+-*/<>=~!@#%^&|`?", sql[i]) >= 0:
			i++
			for i < len(sql) && strings.IndexByte("+-*/<>=~!@#%^&|`?", sql[i]) >= 0 {
				if strings.HasPrefix(sql[i:], "--") || strings.HasPrefix(sql[i:], "/*") {
					break
				}
				i++
			}
			if !strings.ContainsAny(sql[start:i], "~!@#%^&|`?") {
				for i > start+1 && (sql[i-1] == '+' || sql[i-1] == '-') {
					i--
				}
			}
		default:
			i++
			if firstError == nil {
				firstError = fmt.Errorf("unsupported SQL character at byte %d", start)
			}
		}
		if kind == String && escaped && quoteAt >= 0 && sql[quoteAt] == '\'' {
			lastEscaped = len(tokens)
		} else if kind != Whitespace && kind != Comment {
			lastEscaped = -1
		}
		tokens = append(tokens, Token{Text: sql[start:i], Kind: kind})
	}
	return tokens, firstError
}

func identifierStart(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' || b >= 0x80
}

func numberEnd(sql string, start int) (int, error) {
	i := start
	if i+1 < len(sql) && sql[i] == '0' && strings.IndexByte("xXoObB", sql[i+1]) >= 0 {
		base := sql[i+1]
		i += 2
		if i < len(sql) && sql[i] == '_' {
			i++
		}
		digitsStart := i
		var numberError error
		for i < len(sql) {
			b := sql[i]
			valid := b >= '0' && b <= '9'
			if base == 'x' || base == 'X' {
				valid = valid || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
			} else if base == 'o' || base == 'O' {
				valid = b >= '0' && b <= '7'
			} else {
				valid = b == '0' || b == '1'
			}
			if b == '_' {
				if i == digitsStart || sql[i-1] == '_' {
					numberError = fmt.Errorf("invalid numeric separator at byte %d", i)
				}
			} else if !valid {
				break
			}
			i++
		}
		if i == digitsStart || sql[i-1] == '_' || i < len(sql) && (identifierStart(sql[i]) || sql[i] >= '0' && sql[i] <= '9' || sql[i] == '$') {
			numberError = fmt.Errorf("invalid numeric constant at byte %d", start)
		}
		return i, numberError
	}
	var numberError error
	for part := 0; part < 3; part++ {
		digitsStart := i
		for i < len(sql) && (sql[i] >= '0' && sql[i] <= '9' || sql[i] == '_') {
			if sql[i] == '_' && (i == digitsStart || sql[i-1] == '_') {
				numberError = fmt.Errorf("invalid numeric separator at byte %d", i)
			}
			i++
		}
		if i > digitsStart && sql[i-1] == '_' {
			numberError = fmt.Errorf("invalid numeric separator at byte %d", i-1)
		}
		if part == 2 {
			if i == digitsStart {
				numberError = fmt.Errorf("missing numeric exponent at byte %d", start)
			}
			break
		}
		if part == 0 && i < len(sql) && sql[i] == '.' && (i+1 == len(sql) || sql[i+1] != '.') {
			i++
			continue
		}
		if i < len(sql) && (sql[i] == 'e' || sql[i] == 'E') {
			i++
			if i < len(sql) && (sql[i] == '+' || sql[i] == '-') {
				i++
			}
			part = 1
			continue
		}
		break
	}
	if i < len(sql) && (identifierStart(sql[i]) || sql[i] == '$') {
		numberError = fmt.Errorf("unexpected text after number at byte %d", start)
	}
	return i, numberError
}
