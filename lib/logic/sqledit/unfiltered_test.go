package sqledit

import (
	"strings"
	"testing"
)

func TestUnfiltered_ReportsCommandsThatChangeEveryRow(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		escapes bool
		want    []string // "COMMAND target"
	}{
		{"delete", "DELETE FROM claims.beneficiary", false, []string{"DELETE claims.beneficiary"}},
		{"lowercase with semicolon", "delete from claims.beneficiary;", false, []string{"DELETE claims.beneficiary"}},
		{"update", "UPDATE claims.beneficiary SET name = 'x'", false, []string{"UPDATE claims.beneficiary"}},
		{"truncate", "TRUNCATE claims.claim_line", false, []string{"TRUNCATE claims.claim_line"}},
		{"truncate list", "TRUNCATE TABLE ONLY a, ONLY b * RESTART IDENTITY CASCADE", false, []string{"TRUNCATE a, b"}},
		{"quoted target", `DELETE FROM "Mixed Case"`, false, []string{`DELETE "Mixed Case"`}},
		{"keyword target", "DELETE FROM ONLY public.user AS u", false, []string{"DELETE public.user"}},
		{"update only alias", "UPDATE ONLY s.t AS x SET a = 1 RETURNING *", false, []string{"UPDATE s.t"}},
		{"after other statements", "SELECT 1 WHERE true; DELETE FROM t; SELECT 2", false, []string{"DELETE t"}},
		{"several", "UPDATE a SET x = 1;\nDELETE FROM b;\nTRUNCATE c;", false, []string{"UPDATE a", "DELETE b", "TRUNCATE c"}},
		{"cte where does not count", "WITH old AS (SELECT id FROM t WHERE id < 5) DELETE FROM t", false, []string{"DELETE t"}},
		{"data-modifying cte", "WITH gone AS (DELETE FROM t RETURNING id) SELECT * FROM gone WHERE id > 1", false, []string{"DELETE t"}},
		{"materialized data cte", "WITH gone AS MATERIALIZED (UPDATE t SET a = 1 RETURNING id) SELECT 1", false, []string{"UPDATE t"}},
		{"filtered cte then unfiltered", "WITH a AS (UPDATE t SET x = 1 WHERE id = 1 RETURNING *) UPDATE u SET y = 2", false, []string{"UPDATE u"}},
		{"recursive cte cycle clause", "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r WHERE n < 3) CYCLE n SET looped USING path DELETE FROM t", false, []string{"DELETE t"}},
		{"subquery where in set", "UPDATE t SET x = (SELECT max(y) FROM u WHERE u.id = 1)", false, []string{"UPDATE t"}},
		{"subquery where in from", "UPDATE t SET x = u.y FROM (SELECT * FROM u WHERE u.ok) AS u", false, []string{"UPDATE t"}},
		{"returning subquery", "DELETE FROM t RETURNING (SELECT 1 WHERE true)", false, []string{"DELETE t"}},
		{"explain analyze", "EXPLAIN ANALYZE DELETE FROM t", false, []string{"DELETE t"}},
		{"explain analyse options", "EXPLAIN (FORMAT JSON, ANALYSE) UPDATE t SET a = 1", false, []string{"UPDATE t"}},
		{"prepare", "PREPARE wipe(int) AS DELETE FROM t", false, []string{"DELETE t"}},
		{"comments around", "/* note */ DELETE -- why\n FROM t; -- done", false, []string{"DELETE t"}},
		{"standard backslash ends string", `SELECT 'a\'; DELETE FROM t; --'`, false, []string{"DELETE t"}},
		{"escape string continuation", "SELECT E'a'\n'x\\'' ; DELETE FROM t; --'", false, []string{"DELETE t"}},
		{"escape continuation after comment", "SELECT E'a' -- more\n'x\\'' ; DELETE FROM t; --'", false, []string{"DELETE t"}},
		{"unbalanced parenthesis", "SELECT (1; DELETE FROM t;", false, []string{"DELETE t"}},
		{"unicode", "UPDATE \"患者\" SET 名前 = '雪'", false, []string{`UPDATE "患者"`}},
		{"no simple target", "DELETE FROM (", false, []string{"DELETE "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			findings := Unfiltered(c.sql, c.escapes)
			var got []string
			for _, finding := range findings {
				if finding.Problem != "" {
					t.Fatalf("unexpected problem: %s", finding.Problem)
				}
				got = append(got, finding.Command+" "+finding.Target)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestUnfiltered_IgnoresFilteredAndNonDataCommands(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		escapes bool
	}{
		{"where", "DELETE FROM t WHERE id = 1", false},
		{"where current of", "DELETE FROM t WHERE CURRENT OF c; UPDATE t SET a = 1 WHERE CURRENT OF c", false},
		{"update where", "UPDATE t SET a = 1 WHERE id = 2", false},
		{"update from where", "UPDATE t SET a = u.a FROM u WHERE t.id = u.id", false},
		{"delete using where", "DELETE FROM t USING u WHERE t.id = u.id", false},
		{"filtered cte", "WITH d AS (DELETE FROM t WHERE id = 1 RETURNING *) SELECT * FROM d", false},
		{"on conflict", "INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO UPDATE SET a = excluded.a", false},
		{"row locks", "SELECT * FROM t FOR UPDATE; SELECT * FROM t FOR NO KEY UPDATE SKIP LOCKED; SELECT * FROM t FOR SHARE; SELECT * FROM t FOR KEY SHARE", false},
		{"locked cte", "WITH l AS (SELECT * FROM t FOR UPDATE) SELECT * FROM l", false},
		{"table lock", "LOCK TABLE t IN SHARE UPDATE EXCLUSIVE MODE", false},
		{"foreign key actions", "CREATE TABLE c (p int REFERENCES t ON DELETE CASCADE ON UPDATE SET NULL)", false},
		{"alter foreign key", "ALTER TABLE c ADD FOREIGN KEY (p) REFERENCES t ON DELETE CASCADE", false},
		{"merge actions", "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN UPDATE SET a = s.a WHEN NOT MATCHED BY SOURCE THEN DELETE", false},
		{"trigger", "CREATE TRIGGER g BEFORE UPDATE OR DELETE OR TRUNCATE ON t FOR EACH STATEMENT EXECUTE FUNCTION f()", false},
		{"rule", "CREATE RULE r AS ON DELETE TO t DO INSTEAD UPDATE t SET a = 1", false},
		{"multi-action rule", "CREATE RULE r AS ON UPDATE TO t DO ALSO (DELETE FROM u; UPDATE v SET a = 1)", false},
		{"policy", "CREATE POLICY p ON t FOR DELETE USING (true)", false},
		{"function body", "CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $$ DELETE FROM t $$", false},
		{"privileges", "GRANT SELECT, UPDATE, DELETE, TRUNCATE ON t TO r; REVOKE DELETE ON t FROM r; ALTER DEFAULT PRIVILEGES GRANT DELETE ON TABLES TO r", false},
		{"comment statement", "COMMENT ON TABLE t IS 'DELETE FROM t'", false},
		{"drop", "DROP TABLE t", false},
		{"strings and comments", "SELECT 'DELETE FROM t', \"update\" FROM t -- TRUNCATE t\n/* UPDATE t SET a = 1 */", false},
		{"dollar body", "DO $$BEGIN DELETE FROM t; TRUNCATE u; END$$", false},
		{"tagged dollar body", "SELECT $x$ UPDATE t SET a = 1 $x$", false},
		{"explain without analyze", "EXPLAIN DELETE FROM t; EXPLAIN (FORMAT JSON) UPDATE t SET a = 1; EXPLAIN VERBOSE DELETE FROM t", false},
		{"column labels", "SELECT a, delete FROM t; SELECT update FROM t; SELECT x.truncate FROM x", false},
		{"backslash escapes keep string", `SELECT 'a\'; DELETE FROM t; --'`, true},
		{"standard continuation keeps string", "SELECT 'a'\n'x\\'' ; DELETE FROM t; --'", false},
		{"block comment ends continuation", "SELECT E'a' /* c */ 'x\\'' ; DELETE FROM t; --'", false},
		{"select", "SELECT * FROM t WHERE x IN (SELECT 1)", false},
		{"empty", "  ; -- nothing\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if findings := Unfiltered(c.sql, c.escapes); len(findings) != 0 {
				t.Errorf("unexpected findings %+v", findings)
			}
		})
	}
}

func TestUnfiltered_FailsClosedWhenTextCannotBeRead(t *testing.T) {
	for _, sql := range []string{
		string([]byte{'D', 'E', 'L', 0xff}),
		"DELETE FROM t WHERE x = 'unterminated",
		"SELECT 1 /* unterminated",
		"SELECT $tag$ unterminated",
		"a\x00b",
		strings.Repeat("a", MaxInputBytes+1),
		strings.Repeat("a+", MaxTokens),
	} {
		findings := Unfiltered(sql, false)
		if len(findings) != 1 || findings[0].Problem == "" || findings[0].Command != "" {
			t.Errorf("%.40q: got %+v, want one could-not-check finding", sql, findings)
			continue
		}
		if message := findings[0].String(); !strings.HasPrefix(message, "Could not check") || strings.Contains(message, "unterminated'") {
			t.Errorf("message %q", message)
		}
	}
}

func TestFinding_StringNamesTargets(t *testing.T) {
	cases := map[Finding]string{
		{Command: "DELETE", Target: "claims.beneficiary"}: "DELETE without WHERE affects every row in claims.beneficiary.",
		{Command: "UPDATE"}:                   "UPDATE without WHERE affects every row in its table.",
		{Command: "TRUNCATE", Target: "a, b"}: "TRUNCATE removes every row from a, b.",
		{Command: "TRUNCATE"}:                 "TRUNCATE removes every row from its tables.",
	}
	for finding, want := range cases {
		if got := finding.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestFormatStillRejectsUnknownBackslashMeaning(t *testing.T) {
	for _, input := range []string{`select 'a\b';`, "select E'a'\n'b\\'; text';"} {
		if _, err := Format(input); err == nil {
			t.Errorf("formatter accepted %q without knowing the session's string mode", input)
		}
	}
}

func TestUnfiltered_CreateTableAsRoutineBodiesAndExplainOptions(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string // "COMMAND target"; nil means no findings
	}{
		{"create table as delete cte", "CREATE TABLE x AS WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", []string{"DELETE t"}},
		{"temp table as update cte", "CREATE TEMP TABLE c2 AS WITH u AS (UPDATE t SET v = 'x' RETURNING *) SELECT * FROM u", []string{"UPDATE t"}},
		{"every table option", "CREATE GLOBAL TEMPORARY TABLE IF NOT EXISTS c3 (a, b) WITH (fillfactor = 70) ON COMMIT DROP AS (WITH d AS (DELETE FROM s.t RETURNING *) SELECT * FROM d) WITH DATA", []string{"DELETE s.t"}},
		{"unlogged lowercase", "create unlogged table x as with d as (delete from t returning 1) select 1", []string{"DELETE t"}},
		{"local temp", "CREATE LOCAL TEMP TABLE x AS WITH d AS (DELETE FROM t RETURNING 1) TABLE d", []string{"DELETE t"}},
		{"filtered cte", "CREATE TABLE x AS WITH d AS (DELETE FROM t WHERE id = 1 RETURNING *) SELECT * FROM d", nil},
		{"plain query", "CREATE TABLE x AS SELECT * FROM t", nil},
		{"execute", "CREATE TABLE x AS EXECUTE p", nil},
		{"execute arguments", "CREATE TEMP TABLE x AS EXECUTE wipe(1, 'delete')", nil},
		{"generated column", "CREATE TABLE t (a int GENERATED ALWAYS AS (1) STORED, b int GENERATED ALWAYS AS IDENTITY)", nil},
		{"sequence as type", "CREATE SEQUENCE s AS bigint", nil},
		{"materialized view", "CREATE MATERIALIZED VIEW mv AS WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", nil},
		{"view", "CREATE VIEW v AS SELECT * FROM t", nil},
		{"explain create table as", "EXPLAIN ANALYZE CREATE TABLE x AS WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", []string{"DELETE t"}},
		{"procedure body", "CREATE PROCEDURE p() LANGUAGE sql BEGIN ATOMIC\n  SELECT 1;\n  DELETE FROM t;\nEND", nil},
		{"function body with case", "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN true THEN 1 END; DELETE FROM t; END; DELETE FROM u", []string{"DELETE u"}},
		{"after procedure body", "CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t WHERE id = 1;\nEND;\nUPDATE t SET a = 1;", []string{"UPDATE t"}},
		{"routine named begin", "CREATE FUNCTION begin() RETURNS int LANGUAGE sql RETURN 1; DELETE FROM t;", []string{"DELETE t"}},
		{"parameter named begin", "CREATE FUNCTION f(begin int) RETURNS int LANGUAGE sql RETURN begin; DELETE FROM t", []string{"DELETE t"}},
		{"column named begin in body", "CREATE PROCEDURE p() BEGIN ATOMIC SELECT begin FROM x; END; DELETE FROM t", []string{"DELETE t"}},
		{"begin atomic column alias in body", "CREATE PROCEDURE p() BEGIN ATOMIC SELECT begin atomic FROM x; END; DELETE FROM t", []string{"DELETE t"}},
		{"not a routine", "CREATE TABLE begin (atomic int); DELETE FROM t", []string{"DELETE t"}},
		{"quoted analyze", `EXPLAIN ("analyze") DELETE FROM t`, []string{"DELETE t"}},
		{"quoted analyze with value", `EXPLAIN (FORMAT TEXT, "analyze" true) UPDATE t SET v = 'x'`, []string{"UPDATE t"}},
		{"unicode analyze", `EXPLAIN (U&"analyze") DELETE FROM t`, []string{"DELETE t"}},
		{"unicode escapes", `EXPLAIN (u&"\0061nalyze") DELETE FROM t`, []string{"DELETE t"}},
		{"mixed case option", "explain (Analyze, buffers) delete from t", []string{"DELETE t"}},
		{"any quoted option", `EXPLAIN ("verbose") DELETE FROM t`, []string{"DELETE t"}},
		{"unquoted options", "EXPLAIN (FORMAT JSON, VERBOSE, COSTS false) DELETE FROM t", nil},
		{"quoted name after options", `EXPLAIN (VERBOSE) DELETE FROM "analyze"`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, finding := range Unfiltered(c.sql, false) {
				if finding.Problem != "" {
					t.Fatalf("unexpected problem: %s", finding.Problem)
				}
				got = append(got, finding.Command+" "+finding.Target)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
