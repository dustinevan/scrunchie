package sqledit

import (
	"fmt"
	"strings"
)

type scope struct {
	open      byte
	multiline bool
	indent    int
	clause    string
	list      bool
}

// Format lays out clauses, select/set/returning lists, CTEs, subqueries, and
// CREATE TABLE definitions. Every non-whitespace token stays byte-identical.
// It rejects incomplete lexical forms, unmatched delimiters, ordinary strings
// containing ambiguous backslashes, and inputs beyond the package limits.
// It is a formatter, not a full SQL grammar validator.
func Format(sql string) (string, error) {
	tokens, err := scan(sql, MaxInputBytes, unknownStrings)
	if err != nil {
		return "", fmt.Errorf("formatting SQL: %w", err)
	}
	protected := make([]bool, len(tokens))
	var delimiters []byte
	lastCode := -1
	for i, token := range tokens {
		if token.Kind == Whitespace || token.Kind == Comment {
			continue
		}
		// A newline between literal segments changes PostgreSQL concatenation.
		// Preserve the full intervening region, including comment boundaries.
		if token.Kind == String && lastCode >= 0 && tokens[lastCode].Kind == String {
			for j := lastCode + 1; j <= i; j++ {
				protected[j] = true
			}
		}
		lastCode = i
		if token.Kind != Operator {
			continue
		}
		switch token.Text {
		case "(", "[":
			delimiters = append(delimiters, token.Text[0])
			if len(delimiters) > MaxDepth {
				return "", fmt.Errorf("formatting SQL: nesting exceeds %d levels", MaxDepth)
			}
		case ")", "]":
			if len(delimiters) == 0 || token.Text == ")" && delimiters[len(delimiters)-1] != '(' || token.Text == "]" && delimiters[len(delimiters)-1] != '[' {
				return "", fmt.Errorf("formatting SQL: unmatched %s", token.Text)
			}
			delimiters = delimiters[:len(delimiters)-1]
		}
	}
	if len(delimiters) != 0 {
		return "", fmt.Errorf("formatting SQL: unclosed %c", delimiters[len(delimiters)-1])
	}
	nextCode := make([]int, len(tokens))
	next := -1
	for i := len(tokens) - 1; i >= 0; i-- {
		nextCode[i] = next
		if tokens[i].Kind != Whitespace && tokens[i].Kind != Comment {
			next = i
		}
	}

	var output strings.Builder
	output.Grow(len(sql))
	scopes := []scope{{multiline: true}}
	previous := -1
	lastCode = -1
	codeBeforeLast := -1
	gap := ""
	createSeen := false
	tableDefinition := false
	for i, token := range tokens {
		if token.Kind == Whitespace {
			gap = token.Text
			continue
		}
		word := ""
		if token.Kind == Keyword {
			word = strings.ToUpper(token.Text)
		}
		previousText := ""
		if previous >= 0 {
			previousText = tokens[previous].Text
		}
		previousWord := ""
		if lastCode >= 0 && tokens[lastCode].Kind == Keyword {
			previousWord = strings.ToUpper(tokens[lastCode].Text)
		}
		nextWord := ""
		if nextCode[i] >= 0 && tokens[nextCode[i]].Kind == Keyword {
			nextWord = strings.ToUpper(tokens[nextCode[i]].Text)
		}
		closingMultiline := false
		closingIndent := 0
		if token.Kind == Operator && (token.Text == ")" || token.Text == "]") {
			closing := scopes[len(scopes)-1]
			closingMultiline = closing.multiline
			closingIndent = closing.indent - 1
			scopes = scopes[:len(scopes)-1]
		}
		current := &scopes[len(scopes)-1]
		indent := current.indent
		if current.list {
			indent++
		}
		separator := " "
		if previousText == "(" || previousText == "[" || previousText == "." || previousText == "::" || token.Text == ")" || token.Text == "]" || token.Text == "," || token.Text == ";" || token.Text == "." || token.Text == "::" || token.Text == "[" {
			separator = ""
		}
		if current.open == '[' && (previousText == ":" || token.Text == ":") {
			separator = ""
		}
		if token.Text == "(" && previous >= 0 && tokens[previous].Kind == Identifier {
			separator = ""
		}
		if token.Kind == Number && (previousText == "-" || previousText == "+") && codeBeforeLast >= 0 {
			beforeSign := tokens[codeBeforeLast]
			if beforeSign.Kind == Keyword || beforeSign.Kind == Operator && beforeSign.Text != ")" && beforeSign.Text != "]" {
				separator = ""
			}
		}
		if previousText == ";" || previousText == "(" && current.multiline || previousText == "," && (current.list || current.clause == "definition") {
			separator = "\n"
		}
		if previousWord == "SELECT" || previousWord == "SET" || previousWord == "WITH" || previousWord == "VALUES" || previousWord == "RETURNING" {
			if current.multiline && previous == lastCode {
				separator = "\n"
			}
		}
		if closingMultiline {
			separator = "\n"
			indent = closingIndent
		}
		boundary := false
		clause := word
		if current.multiline && token.Kind == Keyword {
			switch word {
			case "SELECT", "WITH", "FROM", "WHERE", "HAVING", "WINDOW", "LIMIT", "OFFSET", "FETCH", "RETURNING", "SET", "VALUES", "UNION", "INTERSECT", "EXCEPT":
				boundary = true
			case "ORDER", "GROUP":
				boundary = nextWord == "BY"
			case "FOR":
				boundary = nextWord == "UPDATE" || nextWord == "SHARE" || nextWord == "KEY" || nextWord == "NO"
			case "JOIN":
				boundary = previousWord != "LEFT" && previousWord != "RIGHT" && previousWord != "FULL" && previousWord != "INNER" && previousWord != "CROSS" && previousWord != "NATURAL" && previousWord != "OUTER"
				current.clause = "JOIN"
			case "LEFT", "RIGHT", "FULL", "INNER", "CROSS", "NATURAL":
				for j, count := nextCode[i], 0; j >= 0 && count < 4; j, count = nextCode[j], count+1 {
					candidate := strings.ToUpper(tokens[j].Text)
					if candidate == "JOIN" {
						boundary = previousWord != "NATURAL"
						clause = "JOIN"
						break
					}
					if candidate != "OUTER" && candidate != "LEFT" && candidate != "RIGHT" && candidate != "FULL" && candidate != "INNER" {
						break
					}
				}
			case "ON":
				boundary = current.clause == "JOIN" || nextWord == "CONFLICT"
			case "USING":
				boundary = current.clause == "JOIN"
			case "AND", "OR":
				if current.clause == "WHERE" || current.clause == "HAVING" || current.clause == "ON" {
					separator = "\n"
					indent = current.indent + 1
				}
			}
		}
		if boundary {
			current.clause = clause
			current.list = clause == "SELECT" || clause == "SET" || clause == "RETURNING" || clause == "WITH" || clause == "VALUES"
			separator = "\n"
			indent = current.indent
			if clause == "ON" && nextWord != "CONFLICT" || clause == "USING" {
				indent++
			}
			if word == "FROM" && previousWord == "DELETE" {
				separator = " "
			}
		}
		if token.Kind == Comment {
			separator = " "
			if strings.ContainsAny(gap, "\r\n") {
				separator = "\n"
			}
		}
		if previous >= 0 && tokens[previous].Kind == Comment {
			if strings.HasPrefix(previousText, "--") || strings.ContainsAny(gap, "\r\n") {
				separator = "\n"
			}
		}
		if protected[i] {
			separator = gap
		} else if separator == "\n" {
			separator += strings.Repeat("  ", indent)
		}
		if output.Len() == 0 {
			separator = ""
		}
		if output.Len()+len(separator)+len(token.Text) > MaxOutputBytes {
			return "", fmt.Errorf("formatting SQL: output exceeds %d bytes", MaxOutputBytes)
		}
		output.WriteString(separator)
		output.WriteString(token.Text)
		gap = ""
		if token.Kind == Operator && (token.Text == "(" || token.Text == "[") {
			multiline := token.Text == "(" && (nextWord == "SELECT" || nextWord == "WITH" || nextWord == "VALUES" || tableDefinition && len(scopes) == 1)
			child := scope{open: token.Text[0], indent: indent, multiline: multiline}
			if multiline {
				child.indent++
				if tableDefinition && len(scopes) == 1 {
					child.clause = "definition"
					tableDefinition = false
				}
			}
			scopes = append(scopes, child)
		}
		if len(scopes) == 1 {
			if word == "CREATE" {
				createSeen = true
			}
			if word == "TABLE" && createSeen {
				tableDefinition = true
			}
			if token.Text == ";" {
				scopes[0] = scope{multiline: true}
				createSeen = false
				tableDefinition = false
			}
		}
		previous = i
		if token.Kind != Comment {
			codeBeforeLast = lastCode
			lastCode = i
		}
	}
	formatted := output.String()
	check, err := scan(formatted, MaxOutputBytes, unknownStrings)
	if err != nil {
		return "", fmt.Errorf("formatting SQL: verifying token boundaries: %w", err)
	}
	originalIndex := 0
	for _, token := range check {
		if token.Kind == Whitespace {
			continue
		}
		for originalIndex < len(tokens) && tokens[originalIndex].Kind == Whitespace {
			originalIndex++
		}
		if originalIndex >= len(tokens) || token != tokens[originalIndex] {
			return "", fmt.Errorf("formatting SQL: spacing would change a token boundary")
		}
		originalIndex++
	}
	for originalIndex < len(tokens) && tokens[originalIndex].Kind == Whitespace {
		originalIndex++
	}
	if originalIndex != len(tokens) {
		return "", fmt.Errorf("formatting SQL: spacing would change a token boundary")
	}
	return formatted, nil
}
