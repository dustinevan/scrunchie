package postgres

import (
	"context"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
)

// splitTargets are SELECT targets whose text hides semicolons, quotes, and
// comment markers from a naive splitter. Some are rejected in one string mode.
var splitTargets = []string{
	`1`, `';'`, `E'\';'`, `E'\\'`, `'\'`, `$$;$$`, `$a$;$$;$a$`, `1 AS ";"`, `/* ; /* ; */ ; */ 1`,
	"-- ;\n1", "'a'\n';'", "E'a'\n'\\';'", "'a' -- c;\n';'", `U&';'`, `B'1'`, `X'1F'`, `N';'`, `$_$;$_$`,
	`e'\\\';'`, `'''' || ';'`, `1/*;*/+1`, "1 --;\n", `'雪;'`, `"x;"."y;"`, `$ö$;$ö$`, "'a'\r\n';'",
	`E'\x3B'`, `b'0' || b'1'`, `'\\'';'`, `(SELECT ';')`, `(SELECT 1; SELECT 2)`, `CASE WHEN true THEN 'begin;' END`,
	`(SELECT 'end' AS begin)`,
}

var splitGlue = []string{";", "; ", ";\n", ";;", "; -- c\n", "; /* ; */ ", ";\n\n-- x;\n", ";/*/**/*/"}

func executeTimed(t *testing.T, session *Session, sql string) (result.Batch, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return session.Execute(ctx, sql, result.Limits{})
}

// Every program PostgreSQL accepts must split into statements that each run
// alone and return the same result, in both string modes.
func TestIntegration_SplitMatchesServerStatements(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	random := rand.New(rand.NewSource(1))
	for _, backslashEscapes := range []bool{false, true} {
		setting := "on"
		if backslashEscapes {
			setting = "off"
		}
		executeSQL(t, session, "SET escape_string_warning = off; SET standard_conforming_strings = "+setting)
		accepted := 0
		for range 3000 {
			var program strings.Builder
			count := 1 + random.Intn(3)
			for i := range count {
				program.WriteString("SELECT " + splitTargets[random.Intn(len(splitTargets))])
				if i < count-1 || random.Intn(2) == 0 {
					program.WriteString(splitGlue[random.Intn(len(splitGlue))])
				}
			}
			sql := program.String()
			whole, err := executeTimed(t, session, sql)
			if err != nil {
				continue // PostgreSQL rejected the whole text, so none of it ran.
			}
			accepted++
			var parts []string
			for _, statement := range sqltext.SplitMode(sql, backslashEscapes) {
				if statement.Body < statement.End {
					parts = append(parts, statement.Text)
				}
			}
			if len(parts) != len(whole.Results) {
				t.Fatalf("escapes %t: %q split into %d statements, PostgreSQL ran %d: %q", backslashEscapes, sql, len(parts), len(whole.Results), parts)
			}
			for i, part := range parts {
				one, err := executeTimed(t, session, part)
				if err != nil || len(one.Results) != 1 || len(one.Results[0].Rows) != 1 || one.Results[0].Rows[0][0].Text != whole.Results[i].Rows[0][0].Text || one.Results[0].Rows[0][0].Null != whole.Results[i].Rows[0][0].Null {
					t.Fatalf("escapes %t: statement %d %q of %q ran differently alone: %v", backslashEscapes, i, part, sql, err)
				}
			}
			if findings := sqledit.Unfiltered(sql, backslashEscapes); len(findings) > 0 {
				t.Fatalf("escapes %t: accepted reads %q got findings %+v", backslashEscapes, sql, findings)
			}
		}
		t.Logf("escapes %t: %d accepted programs", backslashEscapes, accepted)
		if accepted < 1000 {
			t.Fatalf("escapes %t: only %d accepted programs", backslashEscapes, accepted)
		}
	}
}

// Routine bodies and multi-action rules hold semicolons that do not end their
// CREATE statement; split statements must still each be accepted alone.
func TestIntegration_SplitKeepsRoutineBodiesAndRuleActionsWhole(t *testing.T) {
	session := openMode(t, testDSN(t), Options{AllowWrites: true})
	executeSQL(t, session, "CREATE TEMP TABLE split_source (n int); CREATE TEMP TABLE split_log (n int)")
	compound := []string{
		"CREATE PROCEDURE pg_temp.split_p%d() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO split_log VALUES (9);\n  DELETE FROM split_log WHERE n = 1;\nEND",
		"CREATE OR REPLACE FUNCTION pg_temp.split_f%d(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN x > 0 THEN 1 ELSE 0 END; END",
		"create or replace procedure pg_temp.split_q%d() language sql begin atomic select case when true then case when false then 0 end end; select 2; end",
		"create function pg_temp.split_g%d() returns int language sql return case when true then 1 end",
		"CREATE FUNCTION pg_temp.split_h%d() RETURNS text LANGUAGE sql BEGIN ATOMIC SELECT 'begin;' || $$end;$$; /* begin; */ SELECT 'x'; END",
		"CREATE FUNCTION pg_temp.split_k%d(x int DEFAULT (1 + 2)) RETURNS int LANGUAGE sql STABLE\nBEGIN ATOMIC\n  SELECT x + (SELECT count(*)::int FROM split_log);\nEND",
		"CREATE RULE split_r%d AS ON INSERT TO split_source DO ALSO (\n  INSERT INTO split_log VALUES (1);\n  DELETE FROM split_log WHERE n = 5;\n  NOTIFY split_rule\n)",
		"CREATE TABLE pg_temp.split_t%d (begin int, \"end\" text)",
		"SELECT 1",
		"VALUES ('end;')",
	}
	random := rand.New(rand.NewSource(2))
	accepted := 0
	for range 400 {
		var program strings.Builder
		count := 1 + random.Intn(4)
		for i := range count {
			program.WriteString(strings.ReplaceAll(compound[random.Intn(len(compound))], "%d", strconv.Itoa(i)))
			if i < count-1 || random.Intn(2) == 0 {
				program.WriteString(splitGlue[random.Intn(len(splitGlue))])
			}
		}
		sql := program.String()
		executeSQL(t, session, "BEGIN")
		whole, err := executeTimed(t, session, sql)
		executeSQL(t, session, "ROLLBACK")
		if err != nil {
			t.Fatalf("PostgreSQL rejected %q: %v", sql, err)
		}
		accepted++
		var parts []string
		for _, statement := range sqltext.SplitMode(sql, false) {
			if statement.Body < statement.End {
				parts = append(parts, statement.Text)
			}
		}
		if len(parts) != len(whole.Results) {
			t.Fatalf("%q split into %d statements, PostgreSQL ran %d: %q", sql, len(parts), len(whole.Results), parts)
		}
		executeSQL(t, session, "BEGIN")
		for _, part := range parts {
			if one, err := executeTimed(t, session, part); err != nil || len(one.Results) != 1 {
				t.Fatalf("statement %q of %q failed alone: %v", part, sql, err)
			}
		}
		executeSQL(t, session, "ROLLBACK")
	}
	if accepted != 400 {
		t.Fatalf("only %d programs ran", accepted)
	}
}

// Ctrl+J anywhere in a CREATE PROCEDURE with BEGIN ATOMIC runs all of it.
func TestIntegration_CursorAnywhereInProcedureRunsWholeProcedure(t *testing.T) {
	session := openMode(t, testDSN(t), Options{AllowWrites: true})
	const procedure = "CREATE PROCEDURE pg_temp.split_cleanup() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO split_cleanup_t VALUES (9);\n  DELETE FROM split_cleanup_t WHERE id = 1;\nEND;"
	buffer := "SELECT 1;\n" + procedure + "\nSELECT 2;"
	start := strings.Index(buffer, "CREATE")
	for offset := start; offset <= start+len(procedure); offset++ {
		if got := sqltext.CurrentMode(buffer, offset, false); got != procedure {
			t.Fatalf("cursor at %d selected %q", offset, got)
		}
	}
	executeSQL(t, session, "CREATE TEMP TABLE split_cleanup_t (id int); INSERT INTO split_cleanup_t VALUES (1), (2)")
	executeSQL(t, session, sqltext.CurrentMode(buffer, strings.Index(buffer, "WHERE"), false))
	executeSQL(t, session, "CALL pg_temp.split_cleanup()")
	if got := executeSQL(t, session, "SELECT string_agg(id::text, ',' ORDER BY id) FROM split_cleanup_t").Results[0].Rows[0][0].Text; got != "2,9" {
		t.Fatalf("procedure body ran as %q, want 2,9", got)
	}
}
