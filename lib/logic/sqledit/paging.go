package sqledit

import "strings"

// PagedQuery reports whether sql is exactly one plain read that a NO SCROLL
// cursor can return a page of: its first keyword, after any opening
// parentheses, is SELECT, VALUES, TABLE, or WITH. INSERT, UPDATE, DELETE,
// MERGE, INTO, or SHARE anywhere outside strings, quoted names, and comments
// disqualifies it, which also excludes SELECT INTO and row-locking clauses.
// Text the scanner reports as uncertain, such as an unterminated quote or an
// ordinary string containing backslashes, is never eligible. The returned
// query omits the terminating semicolon and the comments or space after it.
func PagedQuery(sql string) (string, bool) {
	tokens, err := scan(sql, MaxInputBytes, unknownStrings)
	if err != nil {
		return "", false
	}
	first, end, offset := "", -1, 0
	for _, token := range tokens {
		start := offset
		offset += len(token.Text)
		if token.Kind == Whitespace || token.Kind == Comment {
			continue
		}
		if end >= 0 {
			return "", false
		}
		if token.Text == ";" {
			end = start
			continue
		}
		word := ""
		if token.Kind == Keyword {
			word = strings.ToUpper(token.Text)
		}
		if first == "" {
			if token.Text == "(" {
				continue
			}
			if word != "SELECT" && word != "VALUES" && word != "TABLE" && word != "WITH" {
				return "", false
			}
			first = word
			continue
		}
		switch word {
		case "INSERT", "UPDATE", "DELETE", "MERGE", "INTO", "SHARE":
			return "", false
		}
	}
	if first == "" {
		return "", false
	}
	if end < 0 {
		end = len(sql)
	}
	return sql[:end], true
}
