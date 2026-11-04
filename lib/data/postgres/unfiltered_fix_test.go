package postgres

import (
	"testing"

	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
)

// PostgreSQL decides whether each statement changes every row; Unfiltered
// must report exactly those. Each case runs in a transaction that is rolled
// back, against a temporary table.
func TestIntegration_UnfilteredMatchesRowsPostgreSQLChanges(t *testing.T) {
	session := openMode(t, testDSN(t), Options{AllowWrites: true})
	executeSQL(t, session, "CREATE TEMP TABLE unfiltered_rows (n int, v text)")
	executeSQL(t, session, "PREPARE unfiltered_read AS SELECT n FROM unfiltered_rows")
	t.Cleanup(func() { executeSQL(t, session, "DEALLOCATE unfiltered_read") })
	for _, c := range []struct {
		sql        string
		changesAll bool
	}{
		{"CREATE TEMP TABLE unfiltered_copy AS WITH d AS (DELETE FROM unfiltered_rows RETURNING *) SELECT * FROM d", true},
		{"CREATE TEMP TABLE unfiltered_copy AS WITH u AS (UPDATE unfiltered_rows SET v = 'x' RETURNING *) SELECT * FROM u", true},
		{"CREATE UNLOGGED TABLE unfiltered_copy (a, b) WITH (fillfactor = 70) AS (WITH d AS (DELETE FROM unfiltered_rows RETURNING *) SELECT * FROM d) WITH DATA", true},
		{"create local temporary table if not exists unfiltered_copy on commit drop as with d as (delete from unfiltered_rows returning n) table d", true},
		{`EXPLAIN ("analyze") DELETE FROM unfiltered_rows`, true},
		{`EXPLAIN (FORMAT TEXT, "analyze" true) UPDATE unfiltered_rows SET v = 'x'`, true},
		{`EXPLAIN (U&"analyze") DELETE FROM unfiltered_rows`, true},
		{`EXPLAIN (U&"\0061nalyze", COSTS false) DELETE FROM unfiltered_rows`, true},
		{"EXPLAIN ANALYZE CREATE TABLE unfiltered_copy AS WITH d AS (DELETE FROM unfiltered_rows RETURNING *) SELECT * FROM d", true},
		{"CREATE FUNCTION pg_temp.begin() RETURNS int LANGUAGE sql RETURN 1; DELETE FROM unfiltered_rows;", true},
		{"CREATE FUNCTION pg_temp.unfiltered_f(begin int) RETURNS int LANGUAGE sql RETURN begin; DELETE FROM unfiltered_rows", true},
		{"CREATE PROCEDURE pg_temp.unfiltered_p() LANGUAGE sql BEGIN ATOMIC SELECT begin atomic FROM (SELECT 1 AS begin) AS s; END; DELETE FROM unfiltered_rows", true},
		{"CREATE PROCEDURE pg_temp.unfiltered_p() LANGUAGE sql BEGIN ATOMIC\n  SELECT 1;\n  DELETE FROM unfiltered_rows;\nEND", false},
		{"CREATE OR REPLACE FUNCTION pg_temp.unfiltered_f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN true THEN 1 END; DELETE FROM unfiltered_rows RETURNING 1; END", false},
		{"CREATE RULE unfiltered_r AS ON INSERT TO unfiltered_rows DO ALSO (NOTIFY unfiltered; DELETE FROM unfiltered_rows)", false},
		{"CREATE TEMP TABLE unfiltered_copy AS EXECUTE unfiltered_read", false},
		{"CREATE TEMP TABLE unfiltered_copy AS WITH d AS (DELETE FROM unfiltered_rows WHERE n = 1 RETURNING *) SELECT * FROM d", false},
		{"CREATE TEMP TABLE unfiltered_copy AS SELECT * FROM unfiltered_rows WITH NO DATA", false},
		{"EXPLAIN (VERBOSE, COSTS false) DELETE FROM unfiltered_rows", false},
	} {
		executeSQL(t, session, "BEGIN; INSERT INTO unfiltered_rows SELECT g, 'v' FROM generate_series(1, 3) AS g")
		_, err := executeTimed(t, session, c.sql)
		if err != nil {
			executeSQL(t, session, "ROLLBACK")
			t.Fatalf("PostgreSQL rejected %q: %v", c.sql, err)
		}
		unchanged := executeSQL(t, session, "SELECT count(*) FROM unfiltered_rows WHERE v = 'v'").Results[0].Rows[0][0].Text
		executeSQL(t, session, "ROLLBACK")
		if changed := unchanged == "0"; changed != c.changesAll {
			t.Fatalf("%q left %s unchanged rows; the case expects changesAll=%t", c.sql, unchanged, c.changesAll)
		}
		findings := sqledit.Unfiltered(c.sql, false)
		for _, finding := range findings {
			if finding.Problem != "" {
				t.Fatalf("%q could not be checked: %s", c.sql, finding.Problem)
			}
		}
		if flagged := len(findings) > 0; flagged != c.changesAll {
			t.Errorf("%q changes every row: %t, but findings are %+v", c.sql, c.changesAll, findings)
		}
	}
}
