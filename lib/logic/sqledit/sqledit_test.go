package sqledit

import (
	"strings"
	"testing"
)

func TestTokenizeIsLosslessForCompleteAndIncompleteText(t *testing.T) {
	inputs := []string{
		"", " \n\t", "select 'unfinished", `select "unfinished`, "/* outer /* nested */",
		`select E'it\'s', U&"d\0061ta", B'01', X'ff', $1::int`,
		"select E'a'\n'b\\'; text';", "$body$begin; 'x'; $inner$y$inner$ end$body$", "select foo$bar, 日本語",
		"a\x00b", string([]byte{0xff, 0xfe, '\''}),
	}
	for _, input := range inputs {
		var joined strings.Builder
		for _, token := range Tokenize(input) {
			joined.WriteString(token.Text)
		}
		if joined.String() != input {
			t.Errorf("tokenization changed %q into %q", input, joined.String())
		}
	}
}

func TestTokenizeRecognizesPostgreSQLLexemes(t *testing.T) {
	cases := []struct {
		text string
		kind Kind
	}{
		{"select", Keyword}, {`"select"`, Identifier}, {"日本語", Identifier}, {"foo$bar", Identifier}, {"$12", Identifier},
		{`E'it\'s'`, String}, {`U&'d\0061ta'`, String}, {`U&"d\0061ta"`, Identifier}, {`B'0101'`, String}, {`X'FF'`, String},
		{"$tag$body;'x'$tag$", String}, {"1.2e-99", Number}, {".1", Number}, {"0x_FF", Number}, {"0o_755", Number}, {"1_000", Number},
		{"/* a /* b */ c */", Comment}, {"-- hello", Comment}, {"?&", Operator}, {"#>>", Operator}, {"->>", Operator}, {"+@", Operator}, {"::", Operator},
	}
	for _, test := range cases {
		tokens := Tokenize(test.text)
		if len(tokens) != 1 || tokens[0].Text != test.text || tokens[0].Kind != test.kind {
			t.Errorf("%q: got %+v, want one kind %d token", test.text, tokens, test.kind)
		}
	}
}

func TestFormatLaysOutSelectClausesAndLists(t *testing.T) {
	input := "select a,sum(b) as total from t where active=true and a> -1 group by a order by a;"
	want := "select\n  a,\n  sum(b) as total\nfrom t\nwhere active = true\n  and a > -1\ngroup by a\norder by a;"
	got, err := Format(input)
	if err != nil || got != want {
		t.Errorf("got:\n%s\nerror: %v\nwant:\n%s", got, err, want)
	}
}

func TestFormatPreservesTokensAndIsIdempotent(t *testing.T) {
	inputs := []string{
		`with q as(select a,b from t) select q.a from q;`,
		`select a[1:3], $1::jsonb->>'name', -1, 2*-3, 0x_FF, .1e+2 from t;`,
		`select x ?& array['a','b'], x #>> '{a,b}', 1 +@ 2 from t;`,
		"select E'it\\'s', U&'d\\0061ta', 'a''b'; -- trailing  \n",
		"select 'left'\n /* keep this */\n'right', E'left'\n'right';",
		"select a, -- belongs to a\n b /* outer /* inner */ end */ from t;",
		`select * from a natural left outer join b on a.id=b.id where a.x=1 or a.x=2;`,
		`update t set a=1,b=2 where id=3 returning a,b;`,
		`insert into t(a,b) values(1,2),(3,4) on conflict(a) do update set b=excluded.b;`,
		`create table t(a int primary key,b numeric(10,2),c text default 'where, select');`,
		"do $body$BEGIN\n RAISE NOTICE 'hello;';\nEND$body$; select 1;",
	}
	for _, input := range inputs {
		formatted, err := Format(input)
		if err != nil {
			t.Errorf("formatting %q: %v", input, err)
			continue
		}
		var before, after []Token
		for _, token := range Tokenize(input) {
			if token.Kind != Whitespace {
				before = append(before, token)
			}
		}
		for _, token := range Tokenize(formatted) {
			if token.Kind != Whitespace {
				after = append(after, token)
			}
		}
		if len(before) != len(after) {
			t.Errorf("changed tokens in %q", input)
			continue
		}
		for i := range before {
			if before[i] != after[i] {
				t.Errorf("changed token %d in %q", i, input)
			}
		}
		again, err := Format(formatted)
		if err != nil || again != formatted {
			t.Errorf("not idempotent for %q:\nfirst:\n%s\nsecond:\n%s\nerror: %v", input, formatted, again, err)
		}
	}
}

func TestFormatPreservesLiteralConcatenationGaps(t *testing.T) {
	for _, gap := range []string{"\n", " \r\n\t", "  ", "\n-- keep\n", " /* no newline */ ", "\n/* keep */\n"} {
		segment := "'a'" + gap + "'b'"
		got, err := Format("select " + segment + ",1;")
		if err != nil || !strings.Contains(got, segment) {
			t.Errorf("changed literal gap %q: %q, %v", gap, got, err)
		}
	}
}

func TestFormatRejectsAmbiguousAndIncompleteInput(t *testing.T) {
	inputs := []string{
		`select 'a\b';`, "select E'a'\n'b\\'; text';", `select 'unterminated`, `select "unterminated`,
		"/* unterminated", "select $tag$unterminated", "select (1]", "select (1", "select 1)",
		"select 1e+", "select 1__2", "select 0x_", "select 0b2", "select 1 . 2", "select \x00",
		strings.Repeat("(", MaxDepth+1) + "1" + strings.Repeat(")", MaxDepth+1),
	}
	for _, input := range inputs {
		if got, err := Format(input); err == nil {
			t.Errorf("accepted uncertain input %q as %q", input, got)
		}
	}
}

func TestTokenAndInputLimitsRetainAllEditorText(t *testing.T) {
	inputs := []string{strings.Repeat("a", MaxInputBytes+1), strings.Repeat("a+", MaxTokens)}
	for _, input := range inputs {
		var joined strings.Builder
		for _, token := range Tokenize(input) {
			joined.WriteString(token.Text)
		}
		if joined.String() != input {
			t.Fatal("resource limit lost editor text")
		}
		if _, err := Format(input); err == nil {
			t.Fatal("formatting accepted input beyond a resource limit")
		}
	}
}

func TestFormatAllowsOutputGrowthWithinOutputLimit(t *testing.T) {
	input := "SELECT '" + strings.Repeat("a", MaxInputBytes-10) + "';"
	got, err := Format(input)
	if err != nil || len(got) <= len(input) || len(got) > MaxOutputBytes {
		t.Errorf("formatting near input limit: output %d bytes, error %v", len(got), err)
	}
}
