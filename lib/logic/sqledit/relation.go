package sqledit

import "strings"

// Relation returns the table read by one simple statement, either
// `SELECT … FROM [schema.]table [[AS] alias] [WHERE …]` or `TABLE name`, as
// identifier parts: unquoted names fold to lower case and quoted names lose
// their quotes. It reports false for joins, comma lists, set operations,
// CTEs, subqueries or functions in FROM, and any text that does not scan
// cleanly, so callers can fall back to a placeholder rather than guess.
func Relation(sql string) ([]string, bool) {
	tokens, err := scan(sql, MaxInputBytes, unknownStrings)
	if err != nil {
		return nil, false
	}
	var words []Token
	for _, token := range tokens {
		if token.Kind != Whitespace && token.Kind != Comment {
			words = append(words, token)
		}
	}
	for len(words) > 0 && words[len(words)-1].Text == ";" {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return nil, false
	}
	if strings.EqualFold(words[0].Text, "TABLE") {
		name, next, ok := qualifiedNameAt(words, 1)
		if !ok || next != len(words) {
			return nil, false
		}
		return name, true
	}
	if !strings.EqualFold(words[0].Text, "SELECT") {
		return nil, false
	}
	// Only a FROM outside parentheses names the statement's table; subqueries
	// in the select list read other tables.
	from, depth := -1, 0
	for i, word := range words {
		switch word.Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth < 0 {
				return nil, false
			}
		case ";":
			return nil, false
		}
		if depth != 0 || word.Kind != Keyword {
			continue
		}
		switch strings.ToUpper(word.Text) {
		case "UNION", "INTERSECT", "EXCEPT", "JOIN", "INTO":
			return nil, false
		case "FROM":
			if from >= 0 {
				return nil, false
			}
			from = i
		}
	}
	if from < 0 {
		return nil, false
	}
	name, next, ok := qualifiedNameAt(words, from+1)
	if !ok {
		return nil, false
	}
	if next < len(words) && strings.EqualFold(words[next].Text, "AS") {
		next++
		if next >= len(words) || words[next].Kind != Identifier {
			return nil, false
		}
		next++
	} else if next < len(words) && words[next].Kind == Identifier {
		next++
	}
	if next == len(words) {
		return name, true
	}
	switch strings.ToUpper(words[next].Text) {
	case "WHERE", "GROUP", "HAVING", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH", "FOR":
		return name, true
	}
	return nil, false
}

// qualifiedNameAt reads one to three dot-separated identifiers starting at i and
// returns the index after them. A following parenthesis is a function call.
func qualifiedNameAt(words []Token, i int) ([]string, int, bool) {
	var parts []string
	for {
		if i >= len(words) || words[i].Kind != Identifier {
			return nil, i, false
		}
		text := words[i].Text
		switch {
		case strings.HasPrefix(text, `"`):
			parts = append(parts, strings.ReplaceAll(text[1:len(text)-1], `""`, `"`))
		case strings.HasPrefix(text, "$") || strings.HasPrefix(text, "U&") || strings.HasPrefix(text, "u&"):
			return nil, i, false
		default:
			// PostgreSQL folds only ASCII letters in unquoted identifiers.
			folded := []byte(text)
			for j, c := range folded {
				if c >= 'A' && c <= 'Z' {
					folded[j] = c + 'a' - 'A'
				}
			}
			parts = append(parts, string(folded))
		}
		i++
		if i < len(words) && words[i].Text == "." && len(parts) < 3 {
			i++
			continue
		}
		break
	}
	if i < len(words) && (words[i].Text == "(" || words[i].Text == "." || words[i].Text == "*") {
		return nil, i, false
	}
	return parts, i, true
}
