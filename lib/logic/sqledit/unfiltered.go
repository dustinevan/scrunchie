package sqledit

import "strings"

// Finding is a command that changes every row of its table, or SQL that could
// not be checked. Command is DELETE, UPDATE, or TRUNCATE, and is empty when
// Problem explains why the text could not be read. Target is the table name as
// written, when it can be read simply.
type Finding struct {
	Command string
	Target  string
	Problem string
}

// String describes the finding in one sentence. Target is user SQL; terminal
// output must still make its control characters visible.
func (f Finding) String() string {
	switch {
	case f.Command == "":
		return "Could not check this SQL for UPDATE or DELETE without WHERE: " + f.Problem + "."
	case f.Command == "TRUNCATE" && f.Target != "":
		return "TRUNCATE removes every row from " + f.Target + "."
	case f.Command == "TRUNCATE":
		return "TRUNCATE removes every row from its tables."
	case f.Target != "":
		return f.Command + " without WHERE affects every row in " + f.Target + "."
	}
	return f.Command + " without WHERE affects every row in its table."
}

// Unfiltered reports DELETE and UPDATE commands with no WHERE clause at their
// own parenthesis depth, and every TRUNCATE. Data-modifying CTEs are judged
// inside their parentheses, so a WHERE in a subquery or another clause does
// not count; WHERE CURRENT OF does. ON CONFLICT DO UPDATE, row locks, MERGE
// actions, and EXPLAIN without ANALYZE are not reported. CREATE, ALTER, DROP,
// COMMENT, GRANT, and REVOKE statements are not reported either, except the
// query of CREATE TABLE … AS, which runs. Comments, strings, and dollar-quoted
// bodies are never read as SQL. backslashEscapes must match the session's
// standard_conforming_strings=off. Text that cannot be tokenized exactly
// returns one finding with a Problem, so callers fail closed.
func Unfiltered(sql string, backslashEscapes bool) []Finding {
	mode := standardStrings
	if backslashEscapes {
		mode = escapeStrings
	}
	tokens, err := scan(sql, MaxInputBytes, mode)
	if err != nil {
		return []Finding{{Problem: err.Error()}}
	}
	var findings []Finding
	var statement []Token
	// PostgreSQL parses the whole text before running any of it, so a
	// semicolon inside unbalanced parentheses means nothing runs. Balanced
	// parentheses hold semicolons only in multi-action rules. A SQL-standard
	// routine body holds them between BEGIN ATOMIC and its END; inside it, END
	// is reserved for the body and for CASE, which is reserved too. This
	// follows the server's grammar rather than psql's looser BEGIN count, so
	// a routine named begin cannot hide the statements after it.
	splitDepth, bodyDepth := 0, 0
	for i := 0; i <= len(tokens); i++ {
		if i < len(tokens) && (tokens[i].Text != ";" || splitDepth > 0 || bodyDepth > 0) {
			if tokens[i].Text == "(" {
				splitDepth++
			} else if tokens[i].Text == ")" {
				splitDepth = max(0, splitDepth-1)
			}
			if tokens[i].Kind == Whitespace || tokens[i].Kind == Comment {
				continue
			}
			statement = append(statement, tokens[i])
			if splitDepth > 0 {
				continue
			}
			switch command := word(tokens[i]); {
			case bodyDepth > 0 && command == "CASE":
				bodyDepth++
			case bodyDepth > 0 && command == "END":
				bodyDepth--
			case bodyDepth == 0 && command == "ATOMIC" && len(statement) >= 4 && word(statement[len(statement)-2]) == "BEGIN" && word(statement[0]) == "CREATE":
				routine := word(statement[1])
				if routine == "OR" && word(statement[2]) == "REPLACE" {
					routine = word(statement[3])
				}
				if routine == "FUNCTION" || routine == "PROCEDURE" {
					bodyDepth = 1
				}
			}
			continue
		}
		if len(statement) == 0 {
			continue
		}
		analyzed := statement
		switch word(statement[0]) {
		case "CREATE":
			// CREATE [GLOBAL|LOCAL] [TEMP|TEMPORARY|UNLOGGED] TABLE … AS runs its
			// query, and that query may hold a data-modifying CTE. The first AS
			// outside parentheses follows the name, columns, and options.
			table := 1
			if table < len(statement) && (word(statement[table]) == "GLOBAL" || word(statement[table]) == "LOCAL") {
				table++
			}
			if table < len(statement) && (word(statement[table]) == "TEMP" || word(statement[table]) == "TEMPORARY" || word(statement[table]) == "UNLOGGED") {
				table++
			}
			query := -1
			if table < len(statement) && word(statement[table]) == "TABLE" {
				depth := 0
				for j := table + 1; j < len(statement) && query < 0; j++ {
					switch {
					case statement[j].Text == "(":
						depth++
					case statement[j].Text == ")":
						depth = max(0, depth-1)
					case depth == 0 && word(statement[j]) == "AS":
						query = j + 1
					}
				}
			}
			if query < 0 {
				// Routine bodies, rule actions, trigger events, and FK actions
				// name commands without running them. Views reject
				// data-modifying CTEs.
				statement = statement[:0]
				continue
			}
			analyzed = statement[query:]
		case "ALTER", "DROP", "COMMENT", "GRANT", "REVOKE":
			// Privileges and FK actions name commands without running them.
			statement = statement[:0]
			continue
		case "EXPLAIN":
			analyze := false
			for _, token := range statement {
				// ANALYZE is reserved, so it can only be an EXPLAIN option here.
				analyze = analyze || word(token) == "ANALYZE" || word(token) == "ANALYSE"
			}
			if len(statement) > 1 && statement[1].Text == "(" {
				// Option names may be quoted, as "analyze" or U&"…", so any
				// quoted name could be ANALYZE.
				for _, option := range statement[2:] {
					if option.Text == ")" {
						break
					}
					analyze = analyze || option.Kind == Identifier && strings.Contains(option.Text, `"`)
				}
			}
			if !analyze {
				statement = statement[:0]
				continue
			}
		}
		depth := 0
		for j, token := range analyzed {
			if token.Text == "(" {
				depth++
				continue
			}
			if token.Text == ")" {
				depth = max(0, depth-1)
				continue
			}
			command := word(token)
			if command != "DELETE" && command != "UPDATE" && command != "TRUNCATE" {
				continue
			}
			if j > 0 {
				previous := word(analyzed[j-1])
				if previous == "" {
					previous = analyzed[j-1].Text
				}
				// ON DELETE, DO UPDATE, FOR [NO KEY] UPDATE, THEN DELETE, trigger
				// events, privilege lists, qualified names, and column labels.
				switch previous {
				case "ON", "DO", "FOR", "KEY", "THEN", "BEFORE", "AFTER", "OR", "OF", "SELECT", ",", ".", "::":
					continue
				}
			}
			next := ""
			if j+1 < len(analyzed) {
				next = word(analyzed[j+1])
			}
			// Read this command's own clauses, stopping where its parentheses close.
			hasWhere, hasSet, level := false, false, 0
			for _, clause := range analyzed[j+1:] {
				if clause.Text == "(" {
					level++
				} else if clause.Text == ")" {
					if level == 0 {
						break
					}
					level--
				} else if level == 0 && word(clause) == "WHERE" {
					hasWhere = true
				} else if level == 0 && word(clause) == "SET" {
					hasSet = true
				}
			}
			finding := Finding{Command: command}
			switch command {
			case "DELETE":
				if next != "FROM" || hasWhere {
					continue
				}
				finding.Target = tableNames(analyzed[j+2:], false)
			case "UPDATE":
				if !hasSet || hasWhere {
					continue
				}
				finding.Target = tableNames(analyzed[j+1:], false)
			case "TRUNCATE":
				if j+1 >= len(analyzed) || next != "TABLE" && next != "ONLY" && !unfilteredName(analyzed[j+1]) {
					continue
				}
				names := analyzed[j+1:]
				if next == "TABLE" {
					names = names[1:]
				}
				finding.Target = tableNames(names, true)
			}
			findings = append(findings, finding)
		}
		statement = statement[:0]
	}
	return findings
}

// word returns an unquoted keyword or identifier in upper case, or "".
func word(token Token) string {
	if (token.Kind != Keyword && token.Kind != Identifier) || strings.ContainsAny(token.Text, `"$`) {
		return ""
	}
	return strings.ToUpper(token.Text)
}

func unfilteredName(token Token) bool {
	return (token.Kind == Keyword || token.Kind == Identifier) && !strings.HasPrefix(token.Text, "$")
}

// tableNames reads [ONLY] name[.name...][*], and a comma-separated list when
// list is set. It returns "" when the first name is not simply readable.
func tableNames(tokens []Token, list bool) string {
	var names []string
	for i := 0; i < len(tokens); {
		if word(tokens[i]) == "ONLY" {
			i++
		}
		if i >= len(tokens) || !unfilteredName(tokens[i]) {
			break
		}
		qualified := tokens[i].Text
		i++
		for i+1 < len(tokens) && tokens[i].Text == "." && unfilteredName(tokens[i+1]) {
			qualified += "." + tokens[i+1].Text
			i += 2
		}
		names = append(names, qualified)
		if i < len(tokens) && tokens[i].Text == "*" {
			i++
		}
		if !list || i >= len(tokens) || tokens[i].Text != "," {
			break
		}
		i++
	}
	return strings.Join(names, ", ")
}
