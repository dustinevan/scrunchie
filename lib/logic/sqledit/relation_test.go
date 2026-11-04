package sqledit

import (
	"fmt"
	"testing"
)

func TestRelationNamesOnlySimpleSingleTableQueries(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT * FROM claims.beneficiary ORDER BY id LIMIT 200;":    "[claims beneficiary]",
		"select id, name\nfrom Claims.Beneficiary b\nwhere b.id > 1": "[claims beneficiary]",
		`select * from public."Mixed Case" as m where m."Id" > 1`:    "[public Mixed Case]",
		`select * from "a""b"`:                                                `[a"b]`,
		"TABLE Claims.Beneficiary;":                                           "[claims beneficiary]",
		"select extract(year from d) from t -- from x\nfor update":            "[t]",
		"select * from t where id in (select id from u) /* join */":           "[t]",
		"select count(*) from db.s.t group by 1 having count(*) > 1 offset 2": "[db s t]",
		"SELECT * FROM ÜBER":                                                  "[Über]",
		"select 1":                                                            "false",
		"select * from a join b on true":                                      "false",
		"select * from a natural join b":                                      "false",
		"select * from a, b":                                                  "false",
		"select * from a x, b y":                                              "false",
		"with x as (select 1) select * from x":                                "false",
		"select * from (select 1) s":                                          "false",
		"select * from generate_series(1, 3)":                                 "false",
		"select * from only t":                                                "false",
		"select a from t union all select a from u":                           "false",
		"select * into copy_t from t":                                         "false",
		"select * from t; select 1":                                           "false",
		"select * from t tablesample system (1)":                              "false",
		"select * from a.b.c.d":                                               "false",
		"select * from t where x = 'it''s'":                                   "[t]",
		`select * from t where x = 'C:\temp'`:                                 "false",
		"select * from t where x = 'unterminated":                             "false",
		"insert into t values (1) returning *":                                "false",
		"":                                                                    "false",
	} {
		relation, ok := Relation(sql)
		got := "false"
		if ok {
			got = fmt.Sprint(relation)
		}
		if got != want {
			t.Errorf("Relation(%q) = %s, want %s", sql, got, want)
		}
	}
}

func TestRelationIgnoresTablesInsideParentheses(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT (SELECT x FROM other)":                                 "false",
		"SELECT (SELECT x FROM other) AS z":                            "false",
		"SELECT (SELECT x FROM other), (SELECT y FROM another)":        "false",
		"SELECT ARRAY(SELECT x FROM other)":                            "false",
		"SELECT EXISTS (SELECT 1 FROM other WHERE other.id = 1)":       "false",
		"SELECT ((SELECT x FROM other))":                               "false",
		"SELECT x, (SELECT y FROM u WHERE u.id = t.id) FROM t":         "[t]",
		"SELECT (SELECT x FROM other) FROM t WHERE t.id IN (SELECT 1)": "[t]",
		"SELECT x) (FROM t":                                            "false",
		"SELECT 1) FROM t":                                             "false",
	} {
		relation, ok := Relation(sql)
		got := "false"
		if ok {
			got = fmt.Sprint(relation)
		}
		if got != want {
			t.Errorf("Relation(%q) = %s, want %s", sql, got, want)
		}
	}
}
