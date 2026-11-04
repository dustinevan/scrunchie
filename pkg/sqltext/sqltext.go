// Package sqltext finds PostgreSQL statement boundaries without changing SQL.
package sqltext

import "strings"

// Statement identifies text in its source buffer. End is an exclusive byte
// offset. Text includes a terminating semicolon when one was present. Body is
// the byte offset of the first token after leading comments; Body equals End
// when the text contains only comments.
type Statement struct {
	Text             string
	Start, End, Body int
	// source is the split buffer, which At reads for line breaks between
	// statements. Strings share storage, so this does not copy the buffer.
	source string
}

// Split returns nonempty statements, preserving comments, quoted strings, and
// dollar-quoted bodies. Ordinary strings use standard_conforming_strings=on.
func Split(sql string) []Statement {
	return SplitMode(sql, false)
}

// SplitMode returns statements using the session's ordinary-string escape
// mode. Statements end where psql would send them: at a semicolon outside
// quotes, comments, parentheses, and the BEGIN … END body of a CREATE
// FUNCTION or PROCEDURE.
func SplitMode(sql string, backslashEscapes bool) []Statement {
	var statements []Statement
	for start := 0; start < len(sql); {
		end := statementEnd(sql, start, backslashEscapes, true)
		text := strings.TrimSpace(sql[start:end])
		if text != "" && text != ";" {
			trimmedStart := start + strings.Index(sql[start:end], text)
			statements = append(statements, Statement{
				Text:   text,
				Start:  trimmedStart,
				End:    trimmedStart + len(text),
				Body:   bodyStart(sql, trimmedStart, trimmedStart+len(text)),
				source: sql,
			})
		}
		start = end
	}
	return statements
}

// At returns the index of the statement that Run uses for a cursor at a byte
// offset, or -1 when no statement has SQL beyond comments. A cursor from a
// statement's first token through the byte after its end selects it, so a
// cursor just after a semicolon selects the statement it terminates. Between
// statements, a cursor on the line where one ends, or on a blank line, selects
// the statement before it; a cursor on a later line with text, such as a
// comment introducing the next statement, selects the next statement. A
// cursor before the first statement selects the first.
func At(statements []Statement, byteOffset int) int {
	previous := -1
	for i, statement := range statements {
		if statement.Body >= statement.End {
			continue
		}
		if byteOffset < statement.Body {
			if previous < 0 {
				return i
			}
			source, end := statements[previous].source, statements[previous].End
			if byteOffset > len(source) || !strings.ContainsAny(source[end:byteOffset], "\r\n") {
				return previous
			}
			lineStart := strings.LastIndexAny(source[:byteOffset], "\r\n") + 1
			lineEnd := len(source)
			if n := strings.IndexAny(source[byteOffset:], "\r\n"); n >= 0 {
				lineEnd = byteOffset + n
			}
			if strings.TrimSpace(source[lineStart:lineEnd]) != "" {
				return i
			}
			return previous
		}
		if byteOffset <= statement.End {
			return i
		}
		previous = i
	}
	return previous
}

// Current returns the statement at a byte offset, following At.
func Current(sql string, byteOffset int) string {
	statements := Split(sql)
	index := At(statements, byteOffset)
	if index < 0 {
		return ""
	}
	return statements[index].Text
}

// CurrentMode selects a statement using the session's ordinary-string escape
// mode. Byte offsets have the same meaning as in Current.
func CurrentMode(sql string, byteOffset int, backslashEscapes bool) string {
	statements := SplitMode(sql, backslashEscapes)
	index := At(statements, byteOffset)
	if index < 0 {
		return ""
	}
	return statements[index].Text
}

// HasCopy reports a COPY statement, ignoring comments and quoted contents.
// backslashEscapes must match a session with standard_conforming_strings=off.
func HasCopy(sql string, backslashEscapes bool) bool {
	// Every semicolon outside quotes and comments starts a candidate, even
	// where psql would continue the statement, so nothing hides a COPY.
	for start := 0; start < len(sql); {
		end := statementEnd(sql, start, backslashEscapes, false)
		i := start
		for i < end {
			if strings.ContainsRune(" \t\r\n\f", rune(sql[i])) {
				i++
				continue
			}
			if i+1 < end && sql[i:i+2] == "--" {
				for i < end && sql[i] != '\n' && sql[i] != '\r' {
					i++
				}
				continue
			}
			if i+1 < end && sql[i:i+2] == "/*" {
				i = commentEnd(sql, i)
				continue
			}
			break
		}
		wordStart := i
		for i < end && identifierByte(sql[i]) {
			i++
		}
		if strings.EqualFold(sql[wordStart:i], "copy") {
			return true
		}
		start = end
	}
	return false
}

// statementEnd returns the offset after the semicolon that ends the statement
// beginning at start, or len(sql). Without nesting, every semicolon outside
// quotes and comments ends it. With nesting it follows psql's lexer
// (src/fe_utils/psqlscan.l): a semicolon ends the statement only outside
// parentheses and outside BEGIN … END. psql counts BEGIN, CASE inside BEGIN,
// and END as unquoted words outside parentheses, and only in statements whose
// leading words are CREATE [OR REPLACE] FUNCTION or PROCEDURE, so semicolons
// in a SQL-standard routine body stay in its CREATE statement.
func statementEnd(sql string, start int, backslashEscapes, nesting bool) int {
	parenDepth, beginDepth := 0, 0
	// Like psql, record the first letter of CREATE, OR, REPLACE, FUNCTION, or
	// PROCEDURE at its position among the statement's first four identifiers.
	var leading [4]byte
	identifiers := 0
	// A word or dollar quote can start right after a dollar quote or a lone
	// $, although $ also continues identifiers. boundary is that offset.
	boundary := -1
	// prefix is the letter before the next quote, as in E'…', or 0.
	var prefix byte
	for i := start; i < len(sql); {
		switch {
		case sql[i] == ';':
			i++
			if !nesting || parenDepth == 0 && beginDepth == 0 {
				return i
			}
		case sql[i] == '(':
			parenDepth++
			i++
		case sql[i] == ')':
			if parenDepth > 0 {
				parenDepth--
			}
			i++
		case (sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= 'A' && sql[i] <= 'Z' || sql[i] == '_' || sql[i] >= 128) && (i == 0 || i == boundary || !identifierByte(sql[i-1])):
			end := i + 1
			for end < len(sql) && identifierByte(sql[end]) {
				end++
			}
			word := sql[i:end]
			i = end
			// psql reads b'', e'', n'', x'', and U&… prefixes as other tokens.
			if len(word) == 1 && end < len(sql) && sql[end] == '\'' && strings.IndexByte("bBeEnNxX", word[0]) >= 0 {
				prefix = word[0]
				continue
			}
			if len(word) == 1 && end < len(sql) && sql[end] == '&' && (word[0] == 'u' || word[0] == 'U') {
				continue
			}
			if identifiers < len(leading) {
				for _, keyword := range []string{"create", "or", "replace", "function", "procedure"} {
					if asciiEqualFold(word, keyword) {
						leading[identifiers] = keyword[0]
					}
				}
			}
			identifiers++
			routine := leading[0] == 'c' && (leading[1] == 'f' || leading[1] == 'p' || leading[1] == 'o' && leading[2] == 'r' && (leading[3] == 'f' || leading[3] == 'p'))
			if !routine || parenDepth > 0 {
				continue
			}
			switch {
			case asciiEqualFold(word, "begin"):
				beginDepth++
			case asciiEqualFold(word, "case"):
				// CASE also ends with END, but psql counts it only inside BEGIN.
				if beginDepth > 0 {
					beginDepth++
				}
			case asciiEqualFold(word, "end"):
				if beginDepth > 0 {
					beginDepth--
				}
			}
		case sql[i] >= '0' && sql[i] <= '9':
			// psql 15 reads a number with any identifier attached as one
			// token (flex's longest match makes 1e5$$ such junk), so that
			// identifier is not a word and holds no dollar quote. A signed
			// exponent ends the token after its digits, or after 1e+ alone.
			// 1.. ends at 1.
			for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
				i++
			}
			if i < len(sql) && sql[i] == '.' && (i+1 == len(sql) || sql[i+1] != '.') {
				i++
				for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
					i++
				}
			}
			if i+1 < len(sql) && (sql[i] == 'e' || sql[i] == 'E') && (sql[i+1] == '+' || sql[i+1] == '-') {
				i += 2
				if i == len(sql) || sql[i] < '0' || sql[i] > '9' {
					boundary = i
					continue
				}
				for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
					i++
				}
			}
			if i < len(sql) && (sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= 'A' && sql[i] <= 'Z' || sql[i] == '_' || sql[i] >= 128) {
				for i < len(sql) && identifierByte(sql[i]) {
					i++
				}
			}
			boundary = i
		case i+1 < len(sql) && sql[i:i+2] == "--":
			for i < len(sql) && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
		case i+1 < len(sql) && sql[i:i+2] == "/*":
			i = commentEnd(sql, i)
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i]
			escaped := quote == '\'' && backslashEscapes
			switch prefix {
			case 'e', 'E':
				escaped = true
			case 'b', 'B', 'x', 'X':
				escaped = false
			}
			prefix = 0
			i++
			for i < len(sql) {
				if escaped && sql[i] == '\\' && i+1 < len(sql) {
					i += 2
					continue
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
				if quote == '\'' {
					// PostgreSQL concatenates literals across whitespace containing
					// a newline. An E-string keeps its escape mode in every piece.
					next := i
					newline := false
					for next < len(sql) {
						if strings.ContainsRune(" \t\r\n\f", rune(sql[next])) {
							newline = newline || sql[next] == '\n' || sql[next] == '\r'
							next++
							continue
						}
						if next+1 < len(sql) && sql[next:next+2] == "--" {
							for next < len(sql) && sql[next] != '\n' && sql[next] != '\r' {
								next++
							}
							continue
						}
						break
					}
					if newline && next < len(sql) && sql[next] == '\'' {
						i = next + 1
						continue
					}
				}
				break
			}
		case sql[i] == '$' && (i == 0 || i == boundary || !identifierByte(sql[i-1])):
			end := i + 1
			for end < len(sql) && ((sql[end] >= 'a' && sql[end] <= 'z') || (sql[end] >= 'A' && sql[end] <= 'Z') || sql[end] == '_' || sql[end] >= 128 || (end > i+1 && sql[end] >= '0' && sql[end] <= '9')) {
				end++
			}
			if end >= len(sql) || sql[end] != '$' {
				// psql reads this $ alone and the text after it again.
				i++
				boundary = i
				continue
			}
			delimiter := sql[i : end+1]
			closing := strings.Index(sql[end+1:], delimiter)
			if closing < 0 {
				return len(sql)
			}
			i = end + 1 + closing + len(delimiter)
			boundary = i
		default:
			i++
		}
	}
	return len(sql)
}

func commentEnd(sql string, start int) int {
	depth := 1
	i := start + 2
	for i < len(sql) && depth > 0 {
		if i+1 < len(sql) && sql[i:i+2] == "/*" {
			depth++
			i += 2
		} else if i+1 < len(sql) && sql[i:i+2] == "*/" {
			depth--
			i += 2
		} else {
			i++
		}
	}
	return i
}

func identifierByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '$' || b >= 128
}

// asciiEqualFold compares word with a lower-case keyword, folding only ASCII
// letters as psql's pg_strcasecmp does.
func asciiEqualFold(word, keyword string) bool {
	if len(word) != len(keyword) {
		return false
	}
	for i := 0; i < len(word); i++ {
		b := word[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if b != keyword[i] {
			return false
		}
	}
	return true
}

// bodyStart skips whitespace and comments between start and end. A lone
// terminating semicolon is not a body.
func bodyStart(sql string, start, end int) int {
	i := start
	for i < end {
		if strings.ContainsRune(" \t\r\n\f", rune(sql[i])) {
			i++
			continue
		}
		if i+1 < end && sql[i:i+2] == "--" {
			for i < end && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
			continue
		}
		if i+1 < end && sql[i:i+2] == "/*" {
			i = min(end, commentEnd(sql, i))
			continue
		}
		break
	}
	if i == end-1 && sql[i] == ';' {
		return end
	}
	return i
}
