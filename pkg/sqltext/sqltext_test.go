package sqltext

import "testing"

func TestSplit_PreservesQuotedAndCommentedSemicolons(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"ordinary strings", " select 'a;b''c'; select 2; ", []string{"select 'a;b''c';", "select 2;"}},
		{"quoted identifiers", `SELECT "a;b""c" FROM "t;t"; SELECT 2`, []string{`SELECT "a;b""c" FROM "t;t";`, "SELECT 2"}},
		{"nested comments", "/* a; /* b; */ c */ SELECT 1; -- d;\nSELECT 2;", []string{"/* a; /* b; */ c */ SELECT 1;", "-- d;\nSELECT 2;"}},
		{"dollar bodies", `DO $body$ BEGIN PERFORM ';'; END $body$; SELECT $$a;b$$;`, []string{`DO $body$ BEGIN PERFORM ';'; END $body$;`, `SELECT $$a;b$$;`}},
		{"repeated dollar tags", `SELECT $same$a;b$same$, $same$c;d$same$; SELECT 2;`, []string{`SELECT $same$a;b$same$, $same$c;d$same$;`, "SELECT 2;"}},
		{"escape strings", `SELECT E'a\';b'; SELECT 2;`, []string{`SELECT E'a\';b';`, "SELECT 2;"}},
		{"escape continuation", "SELECT E'a'\n'b\\'; text'; SELECT 2;", []string{"SELECT E'a'\n'b\\'; text';", "SELECT 2;"}},
		{"escape continuation comments", "SELECT E'a' -- continuation\r\n'b\\'; text'; SELECT 2;", []string{"SELECT E'a' -- continuation\r\n'b\\'; text';", "SELECT 2;"}},
		{"unicode quotes", `SELECT U&'a\003bb', U&"a\003bb"; SELECT 2;`, []string{`SELECT U&'a\003bb', U&"a\003bb";`, "SELECT 2;"}},
		{"multibyte source", "  SELECT 'é;龍';\n SELECT 2;", []string{"SELECT 'é;龍';", "SELECT 2;"}},
		{"empty statements", " ; \n ; ", nil},
		{"incomplete dollar", "SELECT $x$; still editing", []string{"SELECT $x$; still editing"}},
		{"incomplete quote", "SELECT 'still; editing", []string{"SELECT 'still; editing"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Split(c.sql)
			if len(got) != len(c.want) {
				t.Fatalf("got %d statements, want %d: %#v", len(got), len(c.want), got)
			}
			for i, statement := range got {
				if statement.Text != c.want[i] {
					t.Errorf("statement %d got %q, want %q", i, statement.Text, c.want[i])
				}
				if statement.Start < 0 || statement.End > len(c.sql) || statement.Start >= statement.End {
					t.Fatalf("invalid byte offsets: %#v", statement)
				}
				if c.sql[statement.Start:statement.End] != statement.Text {
					t.Errorf("statement %d does not match its original byte range", i)
				}
			}
		})
	}
}

func TestSplitMode_RespectsOrdinaryBackslashes(t *testing.T) {
	sql := `SELECT 'a\'; b'; SELECT 2;`
	statements := SplitMode(sql, true)
	if len(statements) != 2 || statements[0].Text != `SELECT 'a\'; b';` || statements[1].Text != "SELECT 2;" {
		t.Fatalf("incorrect escape-mode statements: %#v", statements)
	}
	if got := CurrentMode(sql, 10, true); got != statements[0].Text {
		t.Errorf("got current %q, want %q", got, statements[0].Text)
	}
}

func TestCurrent_UsesByteOffsetsAndPrecedingStatement(t *testing.T) {
	sql := " SELECT 'é';   SELECT 2;  "
	cases := []struct {
		offset int
		want   string
	}{
		{-10, "SELECT 'é';"},
		{10, "SELECT 'é';"},
		{len(" SELECT 'é';"), "SELECT 'é';"},
		{len(" SELECT 'é';  "), "SELECT 'é';"},
		{len(" SELECT 'é';   "), "SELECT 2;"},
		{len(sql) + 10, "SELECT 2;"},
	}
	for _, c := range cases {
		if got := Current(sql, c.offset); got != c.want {
			t.Errorf("offset %d got %q, want %q", c.offset, got, c.want)
		}
	}
	if got := Current(" ; ", 0); got != "" {
		t.Errorf("empty input got %q", got)
	}
}

func TestHasCopy_DistinguishesCommandsFromQuotedText(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		escapes bool
		want    bool
	}{
		{"plain copy", "COPY x FROM STDIN", false, true},
		{"comment prefix", "/* one /* two */ */\nCoPy (SELECT 1) TO STDOUT;", false, true},
		{"multiple statements", "SELECT 1; -- comment\nCOPY x FROM STDIN;", false, true},
		{"quoted text", `SELECT 'COPY x FROM STDIN;', "copy" FROM x`, false, false},
		{"nested comments only", "/* COPY x FROM STDIN; /* COPY y */ */ SELECT 1", false, false},
		{"dollar text", `DO $a$ BEGIN RAISE NOTICE 'COPY'; END $a$; SELECT $a$COPY;a$a$;`, false, false},
		{"dollar then copy", `SELECT $a$COPY;one$a$, $a$two;a$a$; COPY x TO STDOUT;`, false, true},
		{"unicode string", `SELECT U&'COPY\003b x';`, false, false},
		{"named prepare", `PREPARE copy(int) AS SELECT $1; EXECUTE copy(2);`, false, false},
		{"escape continuation bypass", "SELECT E'a'\n'b\\'; text'; COPY (SELECT 1) TO STDOUT;", false, true},
		{"escape continuation only", "SELECT E'a' -- same literal\n'b\\'; COPY x TO STDOUT; text';", false, false},
		{"ordinary off", `SELECT 'a\'; b'; COPY x FROM STDIN;`, true, true},
		{"ordinary on", `SELECT '\'; COPY x FROM STDIN;`, false, true},
		{"comment carriage return", "-- copy\rCOPY x FROM STDIN", false, true},
		{"empty", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasCopy(c.sql, c.escapes); got != c.want {
				t.Errorf("got %v, want %v for %q", got, c.want, c.sql)
			}
		})
	}
}

func TestSplit_KeepsParenthesesAndRoutineBodiesTogetherLikePsql(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"multi-action rule", "CREATE RULE r AS ON UPDATE TO t DO ALSO (DELETE FROM u; UPDATE v SET a = 1); SELECT 2;",
			[]string{"CREATE RULE r AS ON UPDATE TO t DO ALSO (DELETE FROM u; UPDATE v SET a = 1);", "SELECT 2;"}},
		{"procedure body", "CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t WHERE id = 1;\nEND;\nSELECT 2;",
			[]string{"CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t WHERE id = 1;\nEND;", "SELECT 2;"}},
		{"or replace function with case", "create or replace function f(x int) returns int language sql begin atomic select case when x > 0 then 1 else 0 end; end; select 2;",
			[]string{"create or replace function f(x int) returns int language sql begin atomic select case when x > 0 then 1 else 0 end; end;", "select 2;"}},
		{"comments between leading words", "CREATE /* or */ OR -- replace\nREPLACE PROCEDURE p() BEGIN ATOMIC SELECT 1; END; SELECT 2;",
			[]string{"CREATE /* or */ OR -- replace\nREPLACE PROCEDURE p() BEGIN ATOMIC SELECT 1; END;", "SELECT 2;"}},
		{"case before begin", "CREATE FUNCTION f() RETURNS int LANGUAGE sql RETURN CASE WHEN true THEN 1 END; SELECT 2;",
			[]string{"CREATE FUNCTION f() RETURNS int LANGUAGE sql RETURN CASE WHEN true THEN 1 END;", "SELECT 2;"}},
		{"transaction blocks", "BEGIN; SELECT 1; END; begin; commit;", []string{"BEGIN;", "SELECT 1;", "END;", "begin;", "commit;"}},
		{"other create", "CREATE TABLE begin_log (id int); CREATE OR REPLACE VIEW v AS SELECT CASE WHEN true THEN 1 END AS begin; SELECT 2;",
			[]string{"CREATE TABLE begin_log (id int);", "CREATE OR REPLACE VIEW v AS SELECT CASE WHEN true THEN 1 END AS begin;", "SELECT 2;"}},
		{"words in quotes and comments", "CREATE FUNCTION f(\"begin\" int) RETURNS text LANGUAGE sql RETURN 'begin' || \"begin\" /* begin */ -- begin\n; SELECT 2;",
			[]string{"CREATE FUNCTION f(\"begin\" int) RETURNS text LANGUAGE sql RETURN 'begin' || \"begin\" /* begin */ -- begin\n;", "SELECT 2;"}},
		{"dollar body", "CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$; SELECT 2;",
			[]string{"CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$;", "SELECT 2;"}},
		{"begin inside parentheses", "CREATE FUNCTION f(x int DEFAULT (SELECT 1 FROM begin)) RETURNS int LANGUAGE sql RETURN x; SELECT 2;",
			[]string{"CREATE FUNCTION f(x int DEFAULT (SELECT 1 FROM begin)) RETURNS int LANGUAGE sql RETURN x;", "SELECT 2;"}},
		// psql counts words, not grammar: a parameter named begin opens a block.
		{"psql word heuristic", "CREATE FUNCTION f(begin int) RETURNS int LANGUAGE sql RETURN begin; SELECT 2;",
			[]string{"CREATE FUNCTION f(begin int) RETURNS int LANGUAGE sql RETURN begin; SELECT 2;"}},
		{"ascii folding only", "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1 AS caſe; END; SELECT 2;",
			[]string{"CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1 AS caſe; END;", "SELECT 2;"}},
		{"string prefixes are not words", "CREATE FUNCTION f() RETURNS text LANGUAGE sql BEGIN ATOMIC SELECT E'a\\';b', U&'d\\0061t', x'1F'; END; SELECT 2;",
			[]string{"CREATE FUNCTION f() RETURNS text LANGUAGE sql BEGIN ATOMIC SELECT E'a\\';b', U&'d\\0061t', x'1F'; END;", "SELECT 2;"}},
		{"unterminated body", "CREATE PROCEDURE p() BEGIN ATOMIC SELECT 1; SELECT 2;", []string{"CREATE PROCEDURE p() BEGIN ATOMIC SELECT 1; SELECT 2;"}},
		{"unbalanced open parenthesis", "SELECT (1; SELECT 2;", []string{"SELECT (1; SELECT 2;"}},
		{"unbalanced close parenthesis", "SELECT 1); SELECT (2);", []string{"SELECT 1);", "SELECT (2);"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Split(c.sql)
			if len(got) != len(c.want) {
				t.Fatalf("got %d statements, want %d: %#v", len(got), len(c.want), got)
			}
			for i, statement := range got {
				if statement.Text != c.want[i] || c.sql[statement.Start:statement.End] != statement.Text {
					t.Errorf("statement %d got %q, want %q", i, statement.Text, c.want[i])
				}
			}
		})
	}
}

func TestHasCopy_ChecksAfterEverySemicolonEvenInsideBodies(t *testing.T) {
	for _, sql := range []string{
		"CREATE RULE r AS ON INSERT TO t DO ALSO (NOTIFY x; COPY t TO STDOUT)",
		"CREATE PROCEDURE p() BEGIN ATOMIC SELECT 1; END; COPY x FROM STDIN;",
		"CREATE FUNCTION begin() RETURNS int LANGUAGE sql RETURN 1; COPY x FROM STDIN;",
		"SELECT (1; COPY x FROM STDIN;",
	} {
		if !HasCopy(sql, false) {
			t.Errorf("missed COPY in %q", sql)
		}
	}
}

// Each case was checked against the statements psql 15 sends.
func TestSplit_ReadsTokensAfterNumbersAndDollarQuotesLikePsql(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT 1$$;$$; SELECT 2;", []string{"SELECT 1$$;$$;", "SELECT 2;"}},
		{"SELECT 1.5$$;$$;", []string{"SELECT 1.5$$;$$;"}},
		{"SELECT 1e+5$$;$$;", []string{"SELECT 1e+5$$;$$;"}},
		{"SELECT $1$$;$$;", []string{"SELECT $1$$;$$;"}},
		{"SELECT 1e$$;$$;", []string{"SELECT 1e$$;", "$$;"}},
		{"SELECT 1a$$;$$;", []string{"SELECT 1a$$;", "$$;"}},
		{"SELECT 1e5$$;$$;", []string{"SELECT 1e5$$;", "$$;"}},
		{"SELECT $1a$$;$$;", []string{"SELECT $1a$$;", "$$;"}},
		{`SELECT $t$x$t$E'\';'; SELECT 2;`, []string{`SELECT $t$x$t$E'\';';`, "SELECT 2;"}},
		{"SELECT $a$x$a$$b$;$b$; SELECT 2;", []string{"SELECT $a$x$a$$b$;$b$;", "SELECT 2;"}},
		{"1e) CREATE FUNCTION BEGIN x; END; SELECT 2;", []string{"1e) CREATE FUNCTION BEGIN x; END;", "SELECT 2;"}},
		{"$1e CREATE FUNCTION BEGIN x; END; SELECT 2;", []string{"$1e CREATE FUNCTION BEGIN x; END;", "SELECT 2;"}},
		{"1..e CREATE FUNCTION BEGIN x; END; SELECT 2;", []string{"1..e CREATE FUNCTION BEGIN x;", "END;", "SELECT 2;"}},
		{"CREATE FUNCTION 1begin x; END; SELECT 2;", []string{"CREATE FUNCTION 1begin x;", "END;", "SELECT 2;"}},
		{"CREATE FUNCTION 1e+begin x; END; SELECT 2;", []string{"CREATE FUNCTION 1e+begin x; END;", "SELECT 2;"}},
		{"CREATE FUNCTION $begin x; END; SELECT 2;", []string{"CREATE FUNCTION $begin x; END;", "SELECT 2;"}},
	}
	for _, c := range cases {
		var got []string
		for _, statement := range Split(c.sql) {
			got = append(got, statement.Text)
		}
		if len(got) != len(c.want) {
			t.Errorf("Split(%q) = %q, want %q", c.sql, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Split(%q) = %q, want %q", c.sql, got, c.want)
				break
			}
		}
	}
}
