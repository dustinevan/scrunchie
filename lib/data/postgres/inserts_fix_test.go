package postgres

import (
	"strconv"
	"testing"

	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/pkg/result"
)

const insertsFixture = `
DROP SCHEMA IF EXISTS "scrunch inserts" CASCADE;
CREATE SCHEMA "scrunch inserts";
CREATE TABLE "scrunch inserts"."Src ""T""" (
  k int, "Mixed Case" text, "select" text, "a""b" varchar(20), "new
line" text, "雪" text, c5 char(5), i2 int2, i8 int8, n numeric, f4 real, f8 float8,
  b bool, by bytea, j json, jb jsonb, ta text[], ia int[], d date, tz timestamptz, iv interval, u uuid, ip inet, m money,
  "values" int, "user" text, bit1 bit(3), vb varbit, x xml, pt point, r int4range, e2 text
);
INSERT INTO "scrunch inserts"."Src ""T""" VALUES
 (1, 'it''s', E'back\\slash', 'q"uote', E'line1\nline2\r\n', '雪🙂 名前', 'ab', -32768, -9223372036854775808, 12345678901234567890.1234567890123, 0.1, 0.1,
  true, '\x00ff5c27', '{"a": "b\"c\\d", "e": [1, 2.50, null]}', '{"a": "b\"c\\d"}', ARRAY['a,b', 'c"d', E'e\\f', NULL, '', 'NULL'], ARRAY[1,NULL,3], '2024-02-29', '2024-01-01 00:00:00.123456+00', '1 mon 2 days 03:04:05.5', 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '192.168.0.1/24', 12.34,
  1, 'u', B'101', B'1', '<a>x&amp;y</a>', '(1,2)', '[1,5)', E'tab\there\x01ctl'),
 (2, '', E'\\', NULL, E'\\x41', '''', NULL, 0, 0, 'NaN', 'NaN', '-Infinity',
  false, '', '[]', 'null', '{}', '{}', 'infinity', '-infinity', '-178000000 years', NULL, '::1', -0.01,
  NULL, NULL, NULL, '', NULL, NULL, 'empty', E'E''\\'''),
 (3, E'\\''); DROP TABLE x; --', 'O''Reilly', 'x', '', ' ', '  ', 1, 1, '-0.000', '-0', '-0',
  NULL, '\x5c78', '"str"', '1e400', '{"\\\\x"}', NULL, '0001-01-01 BC', '2024-06-01 12:00:00+05:30', '-00:00:01', NULL, NULL, 0,
  NULL, NULL, NULL, NULL, NULL, NULL, NULL, '$$; DELETE $$'),
 (4, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, '1e-20', '3.4028235e+38', '1.7976931348623157e+308',
  NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
 (5, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, '-1.5', '-1.17549435e-38', '-5e-324',
  NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
CREATE TABLE "scrunch inserts"."Dst ""T""" (LIKE "scrunch inserts"."Src ""T""");
`

// insertsDifferences lists source columns whose copied value differs, by
// to_jsonb and by text, which keeps the sign of float zero.
const insertsDifferences = `SELECT src.k, e.key FROM "scrunch inserts"."Src ""T""" AS src
  JOIN "scrunch inserts"."Dst ""T""" AS dst ON dst.k = src.k, LATERAL jsonb_each(to_jsonb(src)) AS e
  WHERE (to_jsonb(dst) -> e.key) IS DISTINCT FROM e.value
     OR src.f4::text IS DISTINCT FROM dst.f4::text OR src.f8::text IS DISTINCT FROM dst.f8::text
UNION ALL SELECT k, 'missing row' FROM "scrunch inserts"."Src ""T""" WHERE k NOT IN (SELECT k FROM "scrunch inserts"."Dst ""T""")
UNION ALL SELECT -1, 'extra rows' WHERE (SELECT count(*) FROM "scrunch inserts"."Dst ""T""") <> 5`

// Copied INSERT and IN-list SQL must mean the same in either string mode,
// whichever mode the session had when it was copied.
func TestIntegration_CopiedSQLRoundTripsInEitherStringMode(t *testing.T) {
	session := openMode(t, testDSN(t), Options{AllowWrites: true})
	executeSQL(t, session, insertsFixture)
	t.Cleanup(func() {
		executeSQL(t, session, `SET standard_conforming_strings = on; DROP SCHEMA "scrunch inserts" CASCADE`)
	})
	source := executeSQL(t, session, `SELECT * FROM "scrunch inserts"."Src ""T""" ORDER BY k`).Results[0]
	table := cell.Table{Columns: source.Columns, Rows: source.Rows}
	relation := []string{"scrunch inserts", `Dst "T"`}
	inserts := cell.Inserts(table, relation, false)
	if other := cell.Inserts(table, relation, true); other != inserts {
		t.Fatalf("Copy as INSERT depends on the string mode:\n%s\n%s", inserts, other)
	}
	for _, setting := range []string{"on", "off"} {
		executeSQL(t, session, "SET escape_string_warning = off; SET standard_conforming_strings = "+setting)
		executeSQL(t, session, `BEGIN; `+inserts)
		differences := executeSQL(t, session, insertsDifferences).Results[0].Rows
		executeSQL(t, session, "ROLLBACK")
		for _, row := range differences {
			t.Errorf("standard_conforming_strings=%s: row %s column %q changed", setting, row[0].Text, row[1].Text)
		}
		for c, column := range source.Columns {
			switch column.Name {
			case "j", "x", "pt":
				continue // json, xml, and point have no equality operator.
			}
			var values []result.Cell
			for _, row := range source.Rows {
				if !row[c].Null {
					values = append(values, row[c])
				}
			}
			if len(values) == 0 {
				continue
			}
			list := cell.InList(values, column, setting == "off")
			query := `SELECT count(*) FROM "scrunch inserts"."Src ""T""" WHERE ` + cell.Identifier(column.Name) + " IN " + list
			if got := executeSQL(t, session, query).Results[0].Rows[0][0].Text; got != strconv.Itoa(len(values)) {
				t.Errorf("standard_conforming_strings=%s: IN list for %q matched %s of %d rows: %s", setting, column.Name, got, len(values), list)
			}
		}
	}
}
