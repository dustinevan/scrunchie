package sqledit

import (
	"reflect"
	"strings"
	"testing"
)

// analyzeAt reads a cursor marked with | from text.
func analyzeAt(text string) Site {
	cursor := strings.Index(text, "|")
	return Analyze(text[:cursor]+text[cursor+1:], cursor)
}

type ref struct {
	schema, name, alias string
	columns             []string
	local               bool
}

func refs(site Site) []ref {
	var out []ref
	for _, r := range site.References {
		out = append(out, ref{r.Schema, r.Name, r.Alias, r.Columns, r.Local})
	}
	return out
}

func TestAnalyzeFindsWordQualifierAndClause(t *testing.T) {
	cases := []struct {
		text      string
		word      string
		qualifier []string
		clause    Clause
	}{
		{"sel|", "sel", nil, KeywordClause},
		{"|", "", nil, KeywordClause},
		{"SELECT b.| FROM claims.beneficiary b", "", []string{"b"}, ColumnClause},
		{"SELECT b.na|me FROM claims.beneficiary b", "na", []string{"b"}, ColumnClause},
		{"SELECT * FROM claims.|", "", []string{"claims"}, RelationClause},
		{"select * from Claims.Bene|", "Bene", []string{"claims"}, RelationClause},
		{"SELECT * FROM cl|", "cl", nil, RelationClause},
		{"SELECT * FROM a x |", "", nil, KeywordClause},
		{"SELECT * FROM a x w|", "w", nil, KeywordClause},
		{"SELECT * FROM a x, |", "", nil, RelationClause},
		{"SELECT * FROM a JOIN |", "", nil, RelationClause},
		{"SELECT * FROM a JOIN b ON |", "", nil, ColumnClause},
		{"SELECT * FROM a JOIN b USING (|", "", nil, ColumnClause},
		{"SELECT * FROM a WHERE x = 1 AND |", "", nil, ColumnClause},
		{"SELECT * FROM a GROUP BY |", "", nil, ColumnClause},
		{"SELECT * FROM a ORDER BY |", "", nil, ColumnClause},
		{"UPDATE claims.beneficiary SET |", "", nil, ColumnClause},
		{"UPDATE |", "", nil, RelationClause},
		{"DELETE FROM |", "", nil, RelationClause},
		{"INSERT INTO claims.claim_line (|", "", nil, ColumnClause},
		{"INSERT INTO |", "", nil, RelationClause},
		{"TRUNCATE |", "", nil, RelationClause},
		{"SELECT extract(year FROM |", "", nil, ColumnClause},
		{"SELECT * FROM a WHERE x IS DISTINCT FROM |", "", nil, ColumnClause},
		{"SELECT * FROM a WHERE id IN (SELECT id FROM |", "", nil, RelationClause},
		{"SELECT * FROM (SELECT | FROM b) s", "", nil, ColumnClause},
		{"SELECT count(|", "", nil, ColumnClause},
		{`SELECT "Mixed Case".| FROM public."Mixed Case"`, "", []string{"Mixed Case"}, ColumnClause},
		{`SELECT "Mi|`, "Mi", nil, ColumnClause},
		{`SELECT "Mi|xed" FROM t`, "Mi", nil, ColumnClause},
		{"SELECT a.b.| FROM a.b", "", []string{"a", "b"}, ColumnClause},
		{"SELECT 1;|", "", nil, KeywordClause},
		{"SELECT 1; SELECT * FROM |", "", nil, RelationClause},
		{"SELECT /* x */ na|", "na", nil, ColumnClause},
		{"SELECT x AS |", "", nil, KeywordClause},
	}
	for _, test := range cases {
		site := analyzeAt(test.text)
		if site.Word != test.word || !reflect.DeepEqual(site.Qualifier, test.qualifier) || site.Clause != test.clause {
			t.Errorf("%q: word %q qualifier %q clause %d; want %q %q %d", test.text, site.Word, site.Qualifier, site.Clause, test.word, test.qualifier, test.clause)
		}
	}
}

func TestAnalyzeDeclinesStringsAndComments(t *testing.T) {
	for _, text := range []string{
		"SELECT 'abc|", "SELECT 'ab|c' FROM t", "SELECT 1 -- comm|", "SELECT 1 -- comment|\nFROM t",
		"SELECT /* op|", "SELECT /* a /* b */ c| */", "SELECT $$ a| $$", "SELECT $body$ a|", "SELECT E'it\\'s|",
	} {
		if site := analyzeAt(text); site.Clause != NoClause {
			t.Errorf("%q: clause %d, want none", text, site.Clause)
		}
	}
	for _, text := range []string{"SELECT 'abc' |", "SELECT /* c */ |", "SELECT $$x$$, |", "SELECT 'it''s', |"} {
		if site := analyzeAt(text); site.Clause != ColumnClause {
			t.Errorf("%q: clause %d, want columns after a closed literal", text, site.Clause)
		}
	}
}

func TestAnalyzeReplacementRange(t *testing.T) {
	text := "SELECT b.na|me FROM t b"
	site := analyzeAt(text)
	plain := strings.Replace(text, "|", "", 1)
	if plain[site.Start:site.End] != "name" {
		t.Fatalf("range %q, want the whole word", plain[site.Start:site.End])
	}
	text = `SELECT "Mixed Ca|se" FROM t`
	site = analyzeAt(text)
	plain = strings.Replace(text, "|", "", 1)
	if plain[site.Start:site.End] != `"Mixed Case"` || !site.Quoted || site.Word != "Mixed Ca" {
		t.Fatalf("quoted range %q word %q", plain[site.Start:site.End], site.Word)
	}
	text = `SELECT b."Mi| FROM claims.beneficiary b`
	site = analyzeAt(text)
	plain = strings.Replace(text, "|", "", 1)
	if plain[site.Start:site.End] != `"Mi` || len(refs(site)) != 1 || refs(site)[0].alias != "b" {
		t.Fatalf("open quote range %q refs %+v", plain[site.Start:site.End], refs(site))
	}
	text = "SELECT 雪.| FROM t 雪"
	site = analyzeAt(text)
	if !reflect.DeepEqual(site.Qualifier, []string{"雪"}) || len(refs(site)) != 1 || refs(site)[0].alias != "雪" {
		t.Fatalf("Unicode alias: %q %+v", site.Qualifier, refs(site))
	}
}

func TestAnalyzeFindsReferencesInScope(t *testing.T) {
	cases := []struct {
		text string
		want []ref
	}{
		{"SELECT b.| FROM claims.beneficiary b", []ref{{"claims", "beneficiary", "b", nil, true}}},
		{"SELECT X.| FROM Claims.Beneficiary AS X", []ref{{"claims", "beneficiary", "x", nil, true}}},
		{`SELECT | FROM public."Mixed Case" "M c"`, []ref{{"public", "Mixed Case", "M c", nil, true}}},
		{"SELECT 1 FROM a x; SELECT y.| FROM b y; SELECT 3 FROM c z", []ref{{"", "b", "y", nil, true}}},
		{"SELECT $$;$$, x.| FROM t x -- ; FROM u", []ref{{"", "t", "x", nil, true}}},
		{"SELECT | FROM a, b AS bb JOIN c ON true LEFT JOIN d dd USING (id)", []ref{
			{"", "a", "", nil, true}, {"", "b", "bb", nil, true}, {"", "c", "", nil, true}, {"", "d", "dd", nil, true}}},
		{"SELECT | FROM a WHERE EXISTS (SELECT 1 FROM b)", []ref{{"", "a", "", nil, true}, {"", "b", "", nil, false}}},
		{"SELECT * FROM a WHERE EXISTS (SELECT 1 FROM b WHERE b.x = |)", []ref{{"", "a", "", nil, true}, {"", "b", "", nil, true}}},
		{"SELECT * FROM (a JOIN b ON a.id = b.id) WHERE |", []ref{{"", "a", "", nil, true}, {"", "b", "", nil, true}}},
		{"SELECT s.| FROM (SELECT id, mbi AS m, t.name, count(*) FROM claims.beneficiary t) s", []ref{
			{"claims", "beneficiary", "t", nil, false}, {"", "", "s", []string{"id", "m", "name", "count"}, true}}},
		{"SELECT g.| FROM generate_series(1, 3) AS g(n)", []ref{{"", "", "g", []string{"n"}, true}}},
		{"UPDATE ONLY claims.beneficiary b SET name = b.| FROM claims.claim_line l", []ref{
			{"claims", "beneficiary", "b", nil, true}, {"claims", "claim_line", "l", nil, true}}},
		{"DELETE FROM a USING b WHERE |", []ref{{"", "a", "", nil, true}, {"", "b", "", nil, true}}},
		{"INSERT INTO claims.claim_line (|) VALUES (1)", []ref{{"claims", "claim_line", "", nil, true}}},
		{"SELECT * FROM a FOR UPDATE NOWAIT|", []ref{{"", "a", "", nil, true}}},
		{"SELECT * FROM a x WHERE x.|", []ref{{"", "a", "x", nil, true}}},
	}
	for _, test := range cases {
		if got := refs(analyzeAt(test.text)); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", test.text, got, test.want)
		}
	}
}

func TestAnalyzeReadsWithQueries(t *testing.T) {
	site := analyzeAt("WITH recent (id, total) AS (SELECT 1, 2), named AS MATERIALIZED (SELECT a, b AS c, x.d FROM y x) SELECT r.| FROM recent r JOIN named n ON true")
	if len(site.CTEs) != 2 || site.CTEs[0].Name != "recent" || !reflect.DeepEqual(site.CTEs[0].Columns, []string{"id", "total"}) ||
		site.CTEs[1].Name != "named" || !reflect.DeepEqual(site.CTEs[1].Columns, []string{"a", "c", "d"}) {
		t.Fatalf("CTEs = %+v", site.CTEs)
	}
	got := refs(site)
	want := []ref{{"", "y", "x", nil, false}, {"", "recent", "r", []string{"id", "total"}, true}, {"", "named", "n", []string{"a", "c", "d"}, true}}
	if !reflect.DeepEqual(got, want) || !site.References[1].CTE {
		t.Fatalf("references = %+v", got)
	}
	site = analyzeAt("WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM t WHERE |) SELECT * FROM t")
	if len(site.CTEs) != 1 || len(site.References) != 2 || !site.References[0].Local || site.References[1].Local {
		t.Fatalf("recursive CTE scope: %+v", site.References)
	}
}

func TestQuoteIdentifier(t *testing.T) {
	cases := map[string]string{
		"name": "name", "birth_date": "birth_date", "_x1$": "_x1$", "comment": "comment", "type": "type",
		"Mixed Case": `"Mixed Case"`, "select": `"select"`, "user": `"user"`, "Id": `"Id"`, `a"b`: `"a""b"`,
		"1abc": `"1abc"`, "": `""`, "日本": `"日本"`, "order": `"order"`, "a-b": `"a-b"`,
	}
	for name, want := range cases {
		if got := QuoteIdentifier(name); got != want {
			t.Errorf("QuoteIdentifier(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestChangesCatalog(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT 1":                                false,
		"UPDATE t SET a = 1":                      false,
		"create temp table x (a int)":             true,
		"SELECT 1; -- note\n ALTER TABLE t ADD b": true,
		"SET search_path TO claims":               true,
		"SELECT 'CREATE'; SELECT 2":               false,
		"/* DROP */ SELECT 1":                     false,
	} {
		if got := ChangesCatalog(sql); got != want {
			t.Errorf("ChangesCatalog(%q) = %t", sql, got)
		}
	}
}

func TestAnalyzeIsolatesFromSubqueries(t *testing.T) {
	got := refs(analyzeAt("SELECT * FROM a, (SELECT | FROM b) s"))
	want := []ref{{"", "a", "", nil, false}, {"", "b", "", nil, true}, {"", "", "s", []string{}, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// Completion runs on every keystroke, so no prefix or cursor may panic.
func TestAnalyzeAcceptsEveryCursorInIncompleteSQL(t *testing.T) {
	inputs := []string{
		`WITH r(a) AS (SELECT 1), s AS NOT MATERIALIZED (SELECT x.* FROM y x) SELECT r.a, "q""x".b FROM r, LATERAL (SELECT 1) l(c) JOIN "s" USING (a) WHERE a IN (SELECT 1 FROM z) ORDER BY 1;`,
		`INSERT INTO t (a, b) SELECT DISTINCT ON (a) a, b FROM u ON CONFLICT (a) DO UPDATE SET b = excluded.b RETURNING *; DELETE FROM t USING u WHERE t.a = u.a;`,
		`SELECT E'it\'s', $tag$ ; $tag$, U&"d\0061ta", 1.5e3, b.c::text, (((, )), ARRAY[1,2], extract(year from now()) /* c /* n */ */ -- tail`,
		`UPDATE ONLY t AS x SET (a, b) = (SELECT 1, 2) FROM generate_series(1, 2) WITH ORDINALITY AS g(n, i) WHERE x.a = g.n;`,
		`SELECT "unclosed FROM t`, `SELECT 'unclosed FROM t`, `SELECT /* unclosed FROM t`, `SELECT $$ unclosed`, `)))) SELECT (((`, `.`, `"`, `""."".`,
	}
	for _, input := range inputs {
		for cursor := 0; cursor <= len(input); cursor++ {
			site := Analyze(input, cursor)
			if site.Start < 0 || site.Start > site.End || site.End > len(input) {
				t.Fatalf("%q at %d: range %d..%d", input, cursor, site.Start, site.End)
			}
		}
	}
	if site := Analyze(string([]byte{0xff, 'a'}), 1); site.Clause != NoClause {
		t.Fatal("invalid UTF-8 was analyzed")
	}
}

func FuzzAnalyze(f *testing.F) {
	f.Add("SELECT b.na FROM claims.beneficiary b WHERE (", 9)
	f.Add(`WITH x(a) AS (SELECT "q""".* FROM t) SELECT FROM x, (VALUES (1)) v(n)`, 40)
	f.Add("SELECT $a$ ; $a$, E'\\'' /* /* */", 3)
	f.Fuzz(func(t *testing.T, sql string, cursor int) {
		site := Analyze(sql, cursor)
		if site.Start < 0 || site.Start > site.End || site.End > len(sql) {
			t.Fatalf("range %d..%d for %d bytes", site.Start, site.End, len(sql))
		}
	})
}
