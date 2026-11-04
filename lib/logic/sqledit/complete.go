package sqledit

import (
	"strings"
	"unicode/utf8"
)

// Clause classifies what fits at a completion site.
type Clause uint8

const (
	// NoClause covers strings and comments, where completion offers nothing.
	NoClause Clause = iota
	// KeywordClause expects SQL syntax: a statement start, or the words after
	// a complete table reference such as WHERE or JOIN.
	KeywordClause
	// RelationClause expects a table after FROM, JOIN, UPDATE, INTO, TABLE,
	// USING, or TRUNCATE.
	RelationClause
	// ColumnClause expects an expression: select lists, conditions, SET,
	// GROUP BY, ORDER BY, RETURNING, and column lists.
	ColumnClause
)

// Reference is a table named by the statement around the cursor. Names are
// folded the way PostgreSQL folds them: bare words to lowercase, quoted words
// exactly as written.
type Reference struct {
	Schema  string
	Name    string // empty for subqueries and functions in FROM
	Alias   string
	Columns []string // declared or inferred names; nil when unknown
	CTE     bool     // Name refers to a WITH query of this statement
	Derived bool     // a subquery or function in FROM; only Columns describe it
	Local   bool     // visible at the cursor's query level
}

// Site describes the word a completion replaces and what fits there.
// Offsets are bytes into the analyzed text.
type Site struct {
	Start      int      // replacement start; at the opening quote of a quoted word
	End        int      // replacement end; covers the rest of the word after the cursor
	Word       string   // text typed before the cursor, without quotes
	Quoted     bool     // the word opens with a double quote
	Qualifier  []string // folded names before the word, such as an alias or schema.table
	Clause     Clause
	References []Reference // FROM, JOIN, UPDATE, INTO, and USING items in the statement
	CTEs       []Reference // WITH queries declared by the statement
}

type located struct {
	Token
	start int
}

func (t located) end() int {
	return t.start + len(t.Text)
}

// Analyze inspects the statement containing cursor, a byte offset into sql. It
// accepts incomplete SQL and reads the whole statement, so table references
// after the cursor count: in "SELECT b.| FROM claims.beneficiary b" the
// qualifier b resolves to claims.beneficiary.
func Analyze(sql string, cursor int) Site {
	cursor = max(0, min(cursor, len(sql)))
	for cursor > 0 && cursor < len(sql) && !utf8.RuneStart(sql[cursor]) {
		cursor--
	}
	site := Site{Start: cursor, End: cursor, Clause: KeywordClause}
	if len(sql) > MaxInputBytes || !utf8.ValidString(sql) || strings.IndexByte(sql, 0) >= 0 {
		site.Clause = NoClause
		return site
	}
	var tokens []located
	offset := 0
	for _, token := range Tokenize(sql) {
		tokens = append(tokens, located{token, offset})
		offset += len(token.Text)
	}
	at := -1
	for i, t := range tokens {
		if t.start < cursor && cursor <= t.end() {
			at = i
			break
		}
	}
	word := -1
	if at >= 0 {
		t := tokens[at]
		switch {
		case t.Kind == Comment || t.Kind == String:
			if cursor < t.end() || unclosed(t.Text) {
				site.Clause = NoClause
				return site
			}
		case t.Kind == Identifier && t.Text[0] == '"':
			open := unclosed(t.Text)
			inner := t.Text[1 : cursor-t.start]
			if !open && cursor == t.end() {
				inner = t.Text[1 : len(t.Text)-1]
			}
			site.Start, site.End, site.Quoted = t.start, t.end(), true
			site.Word = strings.ReplaceAll(inner, `""`, `"`)
			word = at
			if open {
				// An open quote swallows the rest of the buffer. Read the text
				// after the cursor as SQL so later table references still count.
				site.End = cursor
				tokens[at].Text = t.Text[:cursor-t.start]
				tokens = tokens[:at+1]
				rest := cursor
				for _, token := range Tokenize(sql[cursor:]) {
					tokens = append(tokens, located{token, rest})
					rest += len(token.Text)
				}
			}
		case bare(t.Token):
			site.Start, site.End = t.start, t.end()
			site.Word = t.Text[:cursor-t.start]
			word = at
		}
	}
	before := at
	if word >= 0 {
		before = word - 1
	}
	first := before + 1
	for q := before; q >= 1 && tokens[q].Text == "." && name(tokens[q-1].Token); q -= 2 {
		site.Qualifier = append(site.Qualifier, fold(tokens[q-1].Token))
		first = q - 1
	}
	for i, j := 0, len(site.Qualifier)-1; i < j; i, j = i+1, j-1 {
		site.Qualifier[i], site.Qualifier[j] = site.Qualifier[j], site.Qualifier[i]
	}
	boundary := site.Start
	if first < len(tokens) && first <= before {
		boundary = tokens[first].start
	}

	statementStart, statementEnd := 0, len(tokens)
	for i := first - 1; i >= 0; i-- {
		if tokens[i].Kind == Operator && tokens[i].Text == ";" {
			statementStart = i + 1
			break
		}
	}
	for i := max(first, word+1); i < len(tokens); i++ {
		if tokens[i].Kind == Operator && tokens[i].Text == ";" {
			statementEnd = i
			break
		}
	}
	var code []located
	for _, t := range tokens[statementStart:statementEnd] {
		if t.Kind != Whitespace && t.Kind != Comment {
			code = append(code, t)
		}
	}
	site.Clause = clauseAt(code, boundary, len(site.Qualifier) > 0)
	site.References, site.CTEs = references(code, boundary)
	return site
}

// clauseAt follows clause keywords up to boundary, tracking parentheses so a
// subquery or function call does not leak its clause into the enclosing one.
func clauseAt(code []located, boundary int, qualified bool) Clause {
	type level struct {
		clause   Clause
		function bool
	}
	levels := []level{{clause: KeywordClause}}
	previous, earlier := "", ""
	previousName := false
	for _, t := range code {
		if t.start >= boundary {
			break
		}
		top := &levels[len(levels)-1]
		switch word := keyword(t.Token); {
		case t.Text == "(":
			next := level{clause: top.clause}
			switch {
			case previous == "USING":
				next.clause = ColumnClause
			case top.clause == RelationClause && previousName:
				// INSERT INTO t (columns) or a function in FROM.
				next.clause = ColumnClause
			case top.clause != RelationClause && (previousName || functionWords[previous]):
				next.clause, next.function = ColumnClause, true
			}
			levels = append(levels, next)
		case t.Text == ")":
			if len(levels) > 1 {
				levels = levels[:len(levels)-1]
			}
		case word != "":
			clause, ok := clauseWords[word]
			if !ok {
				break
			}
			if word == "FROM" && (top.function || previous == "DISTINCT" && (earlier == "IS" || earlier == "NOT")) {
				clause = ColumnClause
			}
			if word == "UPDATE" && (previous == "FOR" || previous == "KEY" || previous == "DO") {
				clause = KeywordClause
			}
			top.clause = clause
		}
		earlier, previous = previous, keyword(t.Token)
		if previous == "" {
			previous = t.Text
		}
		previousName = t.Kind == Identifier && name(t.Token)
	}
	clause := levels[len(levels)-1].clause
	if clause == RelationClause && !qualified && !relationSlots[previous] {
		// After "FROM claims.beneficiary b" the next word is syntax, not a table.
		return KeywordClause
	}
	return clause
}

type scoped struct {
	Reference
	group int
}

type referenceLevel struct {
	group    int
	from     bool // reading a list of table references
	item     bool // the next name starts a table reference
	target   bool // INTO or TABLE target: a parenthesis after it lists columns
	call     bool // the name before the next parenthesis is a function in FROM
	derived  bool // this parenthesis is a FROM item; its alias follows the close
	function bool // this parenthesis holds function arguments
	isolated bool // a subquery in FROM or a WITH body: enclosing tables are not visible
	open     int  // index of the opening parenthesis
	cte      int  // index of the WITH query whose body this is, or -1
}

// visibleGroups lists the scopes the innermost level can see. Expression
// subqueries see their enclosing queries; FROM subqueries and WITH bodies
// do not.
func visibleGroups(levels []referenceLevel) []int {
	var groups []int
	for i := len(levels) - 1; i >= 0; i-- {
		groups = append(groups, levels[i].group)
		if levels[i].isolated {
			break
		}
	}
	return groups
}

// references lists the table references and WITH queries of one statement.
// Each parenthesized query is a scope; references visible from the cursor's
// scope are Local.
func references(code []located, boundary int) ([]Reference, []Reference) {
	levels := []referenceLevel{{cte: -1}}
	groups := 1
	var chain []int
	var found []scoped
	var ctes []Reference
	pendingCTE, pendingOpen := -1, -1
	for i := 0; i < len(code); i++ {
		t := code[i]
		if chain == nil && t.start >= boundary {
			chain = visibleGroups(levels)
		}
		top := &levels[len(levels)-1]
		word := keyword(t.Token)
		switch {
		case t.Text == "(":
			next := referenceLevel{group: groups, open: i, cte: -1}
			if i == pendingOpen {
				next.cte, next.isolated, pendingOpen = pendingCTE, true, -1
			}
			switch {
			case top.call:
				next.derived, next.function = true, true
				top.call, top.item = false, false
			case top.from && top.item:
				next.derived, next.isolated, top.item = true, true, false
				if i+1 < len(code) && !queryWords[keyword(code[i+1].Token)] {
					// A parenthesized join shares the enclosing scope.
					next.group, next.from, next.item, next.isolated = top.group, true, true, false
				}
			case i > 0 && (code[i-1].Kind == Identifier || functionWords[keyword(code[i-1].Token)]):
				next.function = true
			}
			if next.group == groups {
				groups++
			}
			levels = append(levels, next)
		case t.Text == ")":
			if len(levels) == 1 {
				continue
			}
			closed := levels[len(levels)-1]
			levels = levels[:len(levels)-1]
			top = &levels[len(levels)-1]
			if closed.derived {
				alias, columns, next := aliasAt(code, i+1)
				if columns == nil && !closed.function {
					columns = outputNames(code[closed.open+1 : i])
				}
				if alias != "" && closed.group != top.group {
					found = append(found, scoped{Reference{Alias: alias, Columns: columns, Derived: true}, top.group})
				}
				i = next - 1
				continue
			}
			if closed.cte < 0 {
				continue
			}
			if ctes[closed.cte].Columns == nil {
				ctes[closed.cte].Columns = outputNames(code[closed.open+1 : i])
			}
			if i+1 < len(code) && code[i+1].Text == "," {
				if cte, open, ok := cteAt(code, i+2); ok {
					ctes = append(ctes, cte)
					pendingCTE, pendingOpen = len(ctes)-1, open
					i = open - 1
				}
			}
		case word == "WITH":
			if cte, open, ok := cteAt(code, i+1); ok {
				ctes = append(ctes, cte)
				pendingCTE, pendingOpen = len(ctes)-1, open
				top.from, top.item = false, false
				i = open - 1
			}
		case top.from && t.Text == ",":
			top.item = true
		case top.from && top.item && (word == "ONLY" || word == "LATERAL"):
		case top.from && top.item && relationName(t.Token):
			parts := []string{fold(t.Token)}
			j := i + 1
			for j+1 < len(code) && code[j].Text == "." && name(code[j+1].Token) {
				parts = append(parts, fold(code[j+1].Token))
				j += 2
			}
			if j < len(code) && code[j].Text == "(" && !top.target {
				top.call = true
				i = j - 1
				continue
			}
			ref := Reference{Name: parts[len(parts)-1]}
			if len(parts) > 1 {
				ref.Schema = parts[len(parts)-2]
			}
			for _, cte := range ctes {
				if len(parts) == 1 && cte.Name == ref.Name {
					ref.CTE, ref.Columns = true, cte.Columns
				}
			}
			alias, columns, next := aliasAt(code, j)
			ref.Alias = alias
			if columns != nil {
				ref.Columns = columns
			}
			found = append(found, scoped{ref, top.group})
			top.item = false
			i = next - 1
		case fromWords[word]:
			switch {
			case word == "FROM" && (top.function || i >= 2 && keyword(code[i-1].Token) == "DISTINCT" && (keyword(code[i-2].Token) == "IS" || keyword(code[i-2].Token) == "NOT")):
			case word == "UPDATE" && i > 0 && (keyword(code[i-1].Token) == "FOR" || keyword(code[i-1].Token) == "KEY" || keyword(code[i-1].Token) == "DO"):
			case word == "USING" && i+1 < len(code) && code[i+1].Text == "(":
				top.from, top.item = false, false
			default:
				top.from, top.item, top.target = true, true, word == "INTO" || word == "TABLE"
			}
		case fromEndWords[word]:
			top.from, top.item, top.target = false, false, false
		}
	}
	if chain == nil {
		chain = visibleGroups(levels)
	}
	var refs []Reference
	for _, ref := range found {
		for _, group := range chain {
			if group == ref.group {
				ref.Local = true
			}
		}
		refs = append(refs, ref.Reference)
	}
	return refs, ctes
}

// cteAt reads "name [(columns)] AS [NOT] [MATERIALIZED] (" starting at j and
// returns the index of the body's opening parenthesis.
func cteAt(code []located, j int) (Reference, int, bool) {
	if j < len(code) && keyword(code[j].Token) == "RECURSIVE" {
		j++
	}
	if j >= len(code) || !relationName(code[j].Token) {
		return Reference{}, 0, false
	}
	cte := Reference{Name: fold(code[j].Token), CTE: true}
	j++
	if j < len(code) && code[j].Text == "(" {
		names, next, ok := nameList(code, j)
		if !ok {
			return Reference{}, 0, false
		}
		cte.Columns, j = names, next
	}
	if j >= len(code) || keyword(code[j].Token) != "AS" {
		return Reference{}, 0, false
	}
	j++
	if j < len(code) && keyword(code[j].Token) == "NOT" {
		j++
	}
	if j < len(code) && keyword(code[j].Token) == "MATERIALIZED" {
		j++
	}
	if j >= len(code) || code[j].Text != "(" {
		return Reference{}, 0, false
	}
	return cte, j, true
}

// aliasAt reads an optional "[AS] alias [(columns)]" starting at j.
func aliasAt(code []located, j int) (string, []string, int) {
	alias := ""
	switch {
	case j < len(code) && keyword(code[j].Token) == "AS":
		if j+1 >= len(code) || !name(code[j+1].Token) {
			return "", nil, j + 1
		}
		alias, j = fold(code[j+1].Token), j+2
	case j < len(code) && code[j].Kind == Identifier && name(code[j].Token):
		alias, j = fold(code[j].Token), j+1
	default:
		return "", nil, j
	}
	if j < len(code) && code[j].Text == "(" {
		if names, next, ok := nameList(code, j); ok {
			return alias, names, next
		}
	}
	return alias, nil, j
}

// nameList reads "(name, name, ...)" starting at the parenthesis at j.
func nameList(code []located, j int) ([]string, int, bool) {
	var names []string
	for j++; j < len(code) && name(code[j].Token); j++ {
		names = append(names, fold(code[j].Token))
		if j+1 < len(code) && code[j+1].Text == ")" {
			return names, j + 2, true
		}
		if j+1 >= len(code) || code[j+1].Text != "," {
			break
		}
		j++
	}
	return nil, j, false
}

// outputNames reads the column names a simple SELECT produces: explicit
// aliases, the last part of plain column references, and function names.
// Items it cannot name, such as *, are skipped.
func outputNames(body []located) []string {
	i := 0
	if i >= len(body) || keyword(body[i].Token) != "SELECT" {
		return nil
	}
	i++
	if i < len(body) && keyword(body[i].Token) == "ALL" {
		i++
	} else if i < len(body) && keyword(body[i].Token) == "DISTINCT" {
		i++
		if i+1 < len(body) && keyword(body[i].Token) == "ON" && body[i+1].Text == "(" {
			depth := 0
			for i++; i < len(body); i++ {
				if body[i].Text == "(" {
					depth++
				} else if body[i].Text == ")" {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
			}
		}
	}
	names := []string{}
	depth, item := 0, i
	for ; i <= len(body); i++ {
		if i < len(body) {
			t := body[i]
			if t.Text == "(" || t.Text == "[" {
				depth++
				continue
			}
			if t.Text == ")" || t.Text == "]" {
				depth--
				continue
			}
			if depth > 0 || t.Text != "," && !selectEndWords[keyword(t.Token)] {
				continue
			}
		}
		if output := outputName(body[item:i]); output != "" {
			names = append(names, output)
		}
		if i == len(body) || body[i].Text != "," {
			break
		}
		item = i + 1
	}
	return names
}

func outputName(item []located) string {
	n := len(item)
	if n == 0 {
		return ""
	}
	last := item[n-1]
	if n >= 2 && keyword(item[n-2].Token) == "AS" && name(last.Token) {
		return fold(last.Token)
	}
	chain := n%2 == 1
	for i, t := range item {
		if i%2 == 0 && !name(t.Token) || i%2 == 1 && t.Text != "." {
			chain = false
		}
	}
	if chain {
		return fold(last.Token)
	}
	if n >= 2 && last.Kind == Identifier && name(last.Token) && (item[n-2].Kind != Operator || item[n-2].Text == ")" || item[n-2].Text == "]") {
		return fold(last.Token)
	}
	if n >= 3 && item[0].Kind == Identifier && bare(item[0].Token) && item[1].Text == "(" && last.Text == ")" {
		depth := 0
		for i := 1; i < n; i++ {
			if item[i].Text == "(" {
				depth++
			} else if item[i].Text == ")" {
				depth--
				if depth == 0 && i != n-1 {
					return ""
				}
			}
		}
		return fold(item[0].Token)
	}
	return ""
}

// QuoteIdentifier returns name as SQL text: bare when it is a lowercase simple
// identifier that is not a PostgreSQL keyword requiring quotes, otherwise
// double-quoted with embedded quotes doubled.
func QuoteIdentifier(name string) string {
	simple := name != "" && !reservedWords[name]
	for i := 0; i < len(name) && simple; i++ {
		b := name[i]
		simple = b >= 'a' && b <= 'z' || b == '_' || i > 0 && (b >= '0' && b <= '9' || b == '$')
	}
	if simple {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ChangesCatalog reports whether any statement in sql begins with a command
// that can add, rename, or hide tables and columns, or change search_path.
func ChangesCatalog(sql string) bool {
	if len(sql) > 1<<20 {
		return true
	}
	start := true
	for _, token := range Tokenize(sql) {
		if token.Kind == Whitespace || token.Kind == Comment {
			continue
		}
		if start {
			switch strings.ToUpper(token.Text) {
			case "CREATE", "ALTER", "DROP", "SET", "RESET", "DISCARD", "IMPORT", "ROLLBACK":
				return true
			}
		}
		start = token.Text == ";"
	}
	return false
}

// unclosed reports whether a string, comment, or quoted identifier lacks its
// closing delimiter, so text typed at its end still belongs to it.
func unclosed(text string) bool {
	switch {
	case strings.HasPrefix(text, "--"):
		return true
	case strings.HasPrefix(text, "/*"):
		depth := 0
		for i := 0; i < len(text); {
			if strings.HasPrefix(text[i:], "/*") {
				depth++
				i += 2
			} else if strings.HasPrefix(text[i:], "*/") {
				depth--
				i += 2
			} else {
				i++
			}
		}
		return depth > 0
	case text[0] == '$':
		end := strings.IndexByte(text[1:], '$') + 1
		if end <= 0 {
			return true
		}
		delimiter := text[:end+1]
		return len(text) < 2*len(delimiter) || !strings.HasSuffix(text, delimiter)
	}
	quoteAt := strings.IndexAny(text, `'"`)
	if quoteAt < 0 {
		return false
	}
	quote := text[quoteAt]
	escaped := quoteAt == 1 && (text[0] == 'E' || text[0] == 'e')
	for i := quoteAt + 1; i < len(text); i++ {
		if escaped && text[i] == '\\' {
			i++
			continue
		}
		if text[i] != quote {
			continue
		}
		if i+1 < len(text) && text[i+1] == quote {
			i++
			continue
		}
		return false
	}
	return true
}

// bare reports whether a token is an unquoted word.
func bare(t Token) bool {
	if t.Kind != Keyword && t.Kind != Identifier || t.Text == "" || !identifierStart(t.Text[0]) {
		return false
	}
	for i := 1; i < len(t.Text); i++ {
		b := t.Text[i]
		if !identifierStart(b) && (b < '0' || b > '9') && b != '$' {
			return false
		}
	}
	return true
}

// name reports whether a token is a bare word or a closed quoted identifier.
func name(t Token) bool {
	return bare(t) || t.Kind == Identifier && len(t.Text) >= 2 && t.Text[0] == '"' && !unclosed(t.Text)
}

// relationName accepts names that can start a table reference: identifiers
// and keywords PostgreSQL does not reserve.
func relationName(t Token) bool {
	return t.Kind == Identifier && name(t) || t.Kind == Keyword && !reservedWords[strings.ToLower(t.Text)]
}

func fold(t Token) string {
	if t.Text != "" && t.Text[0] == '"' {
		return strings.ReplaceAll(strings.TrimSuffix(t.Text[1:], `"`), `""`, `"`)
	}
	return strings.ToLower(t.Text)
}

func keyword(t Token) string {
	if !bare(t) {
		return ""
	}
	return strings.ToUpper(t.Text)
}

var clauseWords = map[string]Clause{
	"SELECT": ColumnClause, "WHERE": ColumnClause, "HAVING": ColumnClause, "ON": ColumnClause, "SET": ColumnClause,
	"RETURNING": ColumnClause, "WHEN": ColumnClause, "THEN": ColumnClause, "ELSE": ColumnClause, "CASE": ColumnClause,
	"AND": ColumnClause, "OR": ColumnClause, "NOT": ColumnClause, "DISTINCT": ColumnClause, "BY": ColumnClause,
	"VALUES": ColumnClause, "LIKE": ColumnClause, "ILIKE": ColumnClause, "IN": ColumnClause, "IS": ColumnClause,
	"BETWEEN": ColumnClause, "OVER": ColumnClause, "FILTER": ColumnClause, "LIMIT": ColumnClause, "OFFSET": ColumnClause,
	"ANY": ColumnClause, "SOME": ColumnClause, "EXISTS": ColumnClause,
	"FROM": RelationClause, "JOIN": RelationClause, "INTO": RelationClause, "TABLE": RelationClause,
	"TRUNCATE": RelationClause, "USING": RelationClause, "UPDATE": RelationClause,
	"INSERT": KeywordClause, "DELETE": KeywordClause, "MERGE": KeywordClause, "CREATE": KeywordClause, "ALTER": KeywordClause,
	"DROP": KeywordClause, "GRANT": KeywordClause, "REVOKE": KeywordClause, "EXPLAIN": KeywordClause, "ANALYZE": KeywordClause,
	"VACUUM": KeywordClause, "BEGIN": KeywordClause, "COMMIT": KeywordClause, "ROLLBACK": KeywordClause, "GROUP": KeywordClause,
	"ORDER": KeywordClause, "PARTITION": KeywordClause, "LEFT": KeywordClause, "RIGHT": KeywordClause, "INNER": KeywordClause,
	"OUTER": KeywordClause, "FULL": KeywordClause, "CROSS": KeywordClause, "NATURAL": KeywordClause, "UNION": KeywordClause,
	"EXCEPT": KeywordClause, "INTERSECT": KeywordClause, "WINDOW": KeywordClause, "FOR": KeywordClause, "DO": KeywordClause,
	"AS": KeywordClause, "WITH": KeywordClause, "RECURSIVE": KeywordClause, "MATERIALIZED": KeywordClause,
	"CONFLICT": KeywordClause, "NOTHING": KeywordClause, "FETCH": KeywordClause, "SHOW": KeywordClause, "COPY": KeywordClause,
	"LOCK": KeywordClause, "INDEX": KeywordClause, "VIEW": KeywordClause, "SCHEMA": KeywordClause, "FUNCTION": KeywordClause,
	"TRIGGER": KeywordClause,
}

// relationSlots are the tokens after which a table name is expected.
var relationSlots = map[string]bool{
	"FROM": true, "JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true, "TRUNCATE": true,
	"ONLY": true, "LATERAL": true, "USING": true, ",": true, "(": true,
}

var fromWords = map[string]bool{
	"FROM": true, "JOIN": true, "UPDATE": true, "INTO": true, "USING": true, "TABLE": true, "TRUNCATE": true,
}

var fromEndWords = map[string]bool{
	"WHERE": true, "GROUP": true, "ORDER": true, "HAVING": true, "LIMIT": true, "OFFSET": true, "SET": true,
	"ON": true, "RETURNING": true, "WINDOW": true, "UNION": true, "EXCEPT": true, "INTERSECT": true,
	"VALUES": true, "SELECT": true, "FETCH": true, "FOR": true, "DO": true, "DEFAULT": true, "WHEN": true, "THEN": true,
}

var selectEndWords = map[string]bool{
	"FROM": true, "INTO": true, "WHERE": true, "GROUP": true, "HAVING": true, "WINDOW": true, "ORDER": true,
	"LIMIT": true, "OFFSET": true, "FETCH": true, "FOR": true, "UNION": true, "EXCEPT": true, "INTERSECT": true,
}

var queryWords = map[string]bool{"SELECT": true, "WITH": true, "VALUES": true, "TABLE": true}

// functionWords are keywords whose parentheses hold arguments that may use
// FROM without naming a table, as in EXTRACT(year FROM created_at).
var functionWords = map[string]bool{
	"EXTRACT": true, "TRIM": true, "OVERLAY": true, "POSITION": true, "SUBSTRING": true, "CAST": true,
}

// reservedWords are PostgreSQL keywords outside the unreserved category
// (pg_get_keywords() catcode R, T, or C) in versions 15 through 17. A bare
// identifier spelled like one must be quoted.
var reservedWords = map[string]bool{}

func init() {
	for _, word := range strings.Fields(`all analyse analyze and any array as asc asymmetric authorization
		between bigint binary bit boolean both case cast char character check coalesce collate collation
		column concurrently constraint create cross current_catalog current_date current_role current_schema
		current_time current_timestamp current_user dec decimal default deferrable desc distinct do else end
		except exists extract false fetch float for foreign freeze from full grant greatest group grouping
		having ilike in initially inner inout int integer intersect interval into is isnull join
		json_array json_arrayagg json_exists json_object json_objectagg json_query json_scalar
		json_serialize json_table json_value lateral leading least left like limit localtime localtimestamp
		merge_action national natural nchar none normalize not notnull null nullif numeric offset on only or
		order out outer overlaps overlay placing position precision primary real references returning right
		row select session_user setof similar smallint some substring symmetric system_user table tablesample
		then time timestamp to trailing treat trim true union unique user using values varchar variadic verbose
		when where window with xmlattributes xmlconcat xmlelement xmlexists xmlforest xmlnamespaces xmlparse
		xmlpi xmlroot xmlserialize xmltable`) {
		reservedWords[word] = true
	}
}
