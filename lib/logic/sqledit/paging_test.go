package sqledit

import "testing"

func TestPagedQueryAcceptsOnePlainRead(t *testing.T) {
	for _, test := range []struct{ sql, query string }{
		{"select 1", "select 1"},
		{"SELECT * FROM claims.beneficiary;", "SELECT * FROM claims.beneficiary"},
		{"select 1; -- done\n/* really */  ", "select 1"},
		{"select 1 -- trailing comment", "select 1 -- trailing comment"},
		{"-- leading\n/* block */ VALUES (1), (2)", "-- leading\n/* block */ VALUES (1), (2)"},
		{"TABLE claims.claim_line", "TABLE claims.claim_line"},
		{"((select 1)) union (select 2);", "((select 1)) union (select 2)"},
		{"with x as (select 1) select * from x", "with x as (select 1) select * from x"},
		{"select 'insert; delete', \"update\", $$merge into$$, E'\\'into' from t", "select 'insert; delete', \"update\", $$merge into$$, E'\\'into' from t"},
		{"select 1 /* for update */ from t -- into", "select 1 /* for update */ from t -- into"},
		{"select * from t fetch first 3 rows only", "select * from t fetch first 3 rows only"},
	} {
		query, ok := PagedQuery(test.sql)
		if !ok || query != test.query {
			t.Errorf("PagedQuery(%q) = %q, %v; want %q", test.sql, query, ok, test.query)
		}
	}
}

func TestPagedQueryRejectsEverythingElse(t *testing.T) {
	for _, sql := range []string{
		"", " ;", "-- only a comment", "(", "1",
		"select 1; select 2", "select 1;;", "select 1; begin",
		"insert into t values (1)", "update t set a = 1", "delete from t", "merge into t using s on true when matched then do nothing",
		"with d as (delete from t returning *) select * from d",
		"with u as (update t set a = 1 returning *) select * from u",
		"with i as (insert into t values (1) returning *) select * from i",
		"select * into new_table from t", "select 1 into temp x",
		"select * from t for update", "select * from t for no key update", "select * from t for share", "select * from t for key share",
		"explain select 1", "show search_path", "declare c cursor for select 1", "fetch 10 from c", "copy t to stdout", "call p()",
		"select 'unterminated", "select 1 /* unterminated", "select $$unterminated", `select "unterminated`,
		"select 'C:\\path'", "select 1 \\gx", "select 1\x00",
	} {
		if query, ok := PagedQuery(sql); ok {
			t.Errorf("PagedQuery(%q) accepted %q", sql, query)
		}
	}
}
