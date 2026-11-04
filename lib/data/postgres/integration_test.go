package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
)

type queryOutcome struct {
	batch result.Batch
	err   error
}

func testSession(t *testing.T) *Session {
	t.Helper()
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN is unset; integration tests need an isolated PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// Fixtures create temporary tables and catalog changes inside rollbacks.
	session, err := Open(ctx, dsn, Options{AllowWrites: true})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := session.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return session
}

func executeSQL(t *testing.T, session *Session, sql string) result.Batch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	batch, err := session.Execute(ctx, sql, result.Limits{})
	if err != nil {
		t.Fatalf("executing fixture %q: %v", sql, err)
	}
	return batch
}

func waitForActiveQuery(t *testing.T, observer *Session, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		batch := executeSQL(t, observer, "SELECT state FROM pg_catalog.pg_stat_activity WHERE pid = "+strconv.FormatUint(uint64(pid), 10))
		if len(batch.Results) == 1 && len(batch.Results[0].Rows) == 1 && batch.Results[0].Rows[0][0].Text == "active" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("query never became active on PostgreSQL")
}

func TestIntegration_SessionPreservesTemporaryTablesAndSettings(t *testing.T) {
	session := testSession(t)
	batch := executeSQL(t, session, "CREATE TEMP TABLE scrunch_session (value int); INSERT INTO scrunch_session VALUES (7); SET application_name = 'scrunch_fixture'")
	if len(batch.Results) != 3 || batch.Results[0].Command != "CREATE TABLE" || batch.Results[1].Command != "INSERT 0 1" || batch.Results[2].Command != "SET" {
		t.Fatalf("unexpected command results: %#v", batch.Results)
	}
	batch = executeSQL(t, session, "SELECT value, current_setting('application_name') FROM scrunch_session; SELECT 2")
	if len(batch.Results) != 2 || batch.Results[0].Rows[0][0].Text != "7" || batch.Results[0].Rows[0][1].Text != "scrunch_fixture" || batch.Results[1].Rows[0][0].Text != "2" {
		t.Fatalf("session did not preserve state: %#v", batch)
	}
	if batch.TxStatus != 'I' || !batch.Connected {
		t.Errorf("unexpected status: %#v", batch)
	}
}

func TestIntegration_TransactionReportsIdleActiveAndFailed(t *testing.T) {
	session := testSession(t)
	if batch := executeSQL(t, session, "BEGIN"); batch.TxStatus != 'T' {
		t.Fatalf("BEGIN status got %q, want T", batch.TxStatus)
	}
	batch, err := session.Execute(context.Background(), "SELECT 1/0", result.Limits{})
	var pgErr Error
	if !errors.As(err, &pgErr) || pgErr.Code != "22012" || batch.TxStatus != 'E' || !batch.Connected {
		t.Fatalf("division error got status %q, error %v", batch.TxStatus, err)
	}
	batch, err = session.Execute(context.Background(), "SELECT 1", result.Limits{})
	if !errors.As(err, &pgErr) || pgErr.Code != "25P02" || batch.TxStatus != 'E' {
		t.Fatalf("aborted transaction got status %q, error %v", batch.TxStatus, err)
	}
	if batch := executeSQL(t, session, "ROLLBACK"); batch.TxStatus != 'I' {
		t.Errorf("ROLLBACK status got %q, want I", batch.TxStatus)
	}
}

func TestIntegration_BatchErrorRollsBackEarlierWrites(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_atomic (value int)")
	batch, err := session.Execute(context.Background(), "INSERT INTO scrunch_atomic VALUES (1); SELECT 1/0; INSERT INTO scrunch_atomic VALUES (2)", result.Limits{})
	if err == nil || !batch.Connected || batch.TxStatus != 'I' {
		t.Fatalf("unexpected failed batch status: %#v, %v", batch, err)
	}
	batch = executeSQL(t, session, "SELECT count(*) FROM scrunch_atomic")
	if got := batch.Results[0].Rows[0][0].Text; got != "0" {
		t.Errorf("earlier write survived batch failure: count %s", got)
	}
}

func TestIntegration_RowLimitDrainsWritesWithoutRepeatingThem(t *testing.T) {
	session := testSession(t)
	batch, err := session.Execute(context.Background(), "CREATE TEMP TABLE scrunch_limits (value int); INSERT INTO scrunch_limits SELECT generate_series(1, 20) RETURNING value; SELECT 99", result.Limits{MaxRows: 2, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 3 || len(batch.Results[1].Rows) != 2 || batch.Results[1].RowCount != 20 || batch.Results[1].Command != "INSERT 0 20" || !batch.Results[1].Truncated || !batch.Truncated {
		t.Fatalf("unexpected retained batch: %#v", batch)
	}
	if batch.Results[2].RowCount != 1 || len(batch.Results[2].Rows) != 0 || batch.Results[2].Command != "SELECT 1" {
		t.Errorf("later result was not drained: %#v", batch.Results[2])
	}
	batch = executeSQL(t, session, "SELECT count(*), sum(value) FROM scrunch_limits")
	if batch.Results[0].Rows[0][0].Text != "20" || batch.Results[0].Rows[0][1].Text != "210" {
		t.Errorf("writes lost or repeated: %#v", batch.Results[0].Rows)
	}
}

func TestIntegration_ByteLimitDoesNotCommitAnOpenTransaction(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_byte_limit (value int)")
	executeSQL(t, session, "BEGIN")
	batch, err := session.Execute(context.Background(), "INSERT INTO scrunch_byte_limit SELECT generate_series(1, 8) RETURNING repeat('x', 2048); SELECT 42", result.Limits{MaxRows: 100, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Truncated || batch.TxStatus != 'T' || batch.Results[0].RowCount != 8 || len(batch.Results[0].Rows) != 0 {
		t.Fatalf("unexpected byte-limited result: %#v", batch)
	}
	if len(batch.Results) != 2 || batch.Results[1].Rows[0][0].Text != "42" {
		t.Errorf("later small result was lost: %#v", batch.Results)
	}
	executeSQL(t, session, "ROLLBACK")
	batch = executeSQL(t, session, "SELECT count(*) FROM scrunch_byte_limit")
	if got := batch.Results[0].Rows[0][0].Text; got != "0" {
		t.Errorf("display limit committed writes: count %s", got)
	}
}

func TestIntegration_ResultSetLimitStillExecutesEveryStatementOnce(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_many_sets (value int)")
	sql := strings.Repeat("INSERT INTO scrunch_many_sets VALUES (1);", maxResultSets+3)
	batch, err := session.Execute(context.Background(), sql, result.Limits{MaxRows: 10, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != maxResultSets || !batch.Truncated {
		t.Fatalf("retained %d results, truncated %v", len(batch.Results), batch.Truncated)
	}
	batch = executeSQL(t, session, "SELECT count(*) FROM scrunch_many_sets")
	if got := batch.Results[0].Rows[0][0].Text; got != strconv.Itoa(maxResultSets+3) {
		t.Errorf("got %s writes, want %d", got, maxResultSets+3)
	}
}

func TestIntegration_CancelPreservesSessionAndRejectsConcurrentCatalog(t *testing.T) {
	session := testSession(t)
	observer := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_cancel (value int); INSERT INTO scrunch_cancel VALUES (8)")
	pid := session.conn.PID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan queryOutcome, 1)
	go func() {
		batch, err := session.Execute(ctx, "SELECT pg_sleep(20)", result.Limits{})
		completed <- queryOutcome{batch: batch, err: err}
	}()
	waitForActiveQuery(t, observer, pid)
	if _, err := session.Schemas(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent catalog got %v, want ErrBusy", err)
	}
	if _, err := session.Databases(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent database discovery got %v, want ErrBusy", err)
	}
	started := time.Now()
	cancel()
	select {
	case outcome := <-completed:
		var pgErr Error
		if !errors.As(outcome.err, &pgErr) || pgErr.Code != "57014" || !outcome.batch.Connected || outcome.batch.TxStatus != 'I' {
			t.Fatalf("cancellation lost session or wrong error: %#v, %v", outcome.batch, outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation took longer than 3 seconds")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("cancellation took %s", elapsed)
	}
	batch := executeSQL(t, session, "SELECT value FROM scrunch_cancel")
	if batch.Results[0].Rows[0][0].Text != "8" {
		t.Error("temporary table was lost after cancellation")
	}
	if databases, err := session.Databases(context.Background()); err != nil || len(databases) == 0 {
		t.Errorf("database discovery after cancellation got %v, %v", databases, err)
	}
}

func TestIntegration_DatabasesFiltersTemplatesConnectionsAndPrivileges(t *testing.T) {
	session := testSession(t)
	batch := executeSQL(t, session, "SELECT current_database()")
	current := batch.Results[0].Rows[0][0].Text
	quoted := `"` + strings.ReplaceAll(current, `"`, `""`) + `"`
	discovered, err := session.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, database := range discovered {
		if database == current {
			found = true
		}
		if database == "template0" || database == "template1" {
			t.Errorf("template database was offered: %s", database)
		}
		if i > 0 && discovered[i-1] > database {
			t.Errorf("database names are not ordered: %v", discovered)
		}
	}
	if !found {
		t.Fatalf("current database missing from discovery: %v", discovered)
	}

	// All catalog changes stay in this transaction and disappear on rollback.
	// SCRUNCH_TEST_DSN must point at the isolated test cluster with a superuser.
	executeSQL(t, session, "BEGIN")
	t.Cleanup(func() { executeSQL(t, session, "ROLLBACK") })
	executeSQL(t, session, "ALTER DATABASE "+quoted+" WITH IS_TEMPLATE true")
	discovered, err = session.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, database := range discovered {
		if database == current {
			t.Error("template database was offered")
		}
	}
	executeSQL(t, session, "ALTER DATABASE "+quoted+" WITH IS_TEMPLATE false; ALTER DATABASE template1 WITH IS_TEMPLATE false ALLOW_CONNECTIONS false")
	discovered, err = session.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, database := range discovered {
		if database == "template1" {
			t.Error("database that prohibits connections was offered")
		}
	}
	role := "scrunch_catalog_" + strconv.FormatUint(uint64(session.conn.PID()), 10)
	executeSQL(t, session, "CREATE ROLE "+role+" NOLOGIN; REVOKE CONNECT ON DATABASE "+quoted+" FROM PUBLIC; SET LOCAL ROLE "+role)
	discovered, err = session.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, database := range discovered {
		if database == current {
			t.Error("database without CONNECT privilege was offered")
		}
	}
}

func TestIntegration_DatabasesCancelledContextPreservesSession(t *testing.T) {
	session := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Databases(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery got %v, want context.Canceled", err)
	}
	if databases, err := session.Databases(context.Background()); err != nil || len(databases) == 0 {
		t.Errorf("database discovery after cancelled context got %v, %v", databases, err)
	}
}

func TestIntegration_CancelInsideTransactionRequiresRollback(t *testing.T) {
	session := testSession(t)
	observer := testSession(t)
	executeSQL(t, session, "BEGIN")
	pid := session.conn.PID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan queryOutcome, 1)
	go func() {
		batch, err := session.Execute(ctx, "SELECT pg_sleep(20)", result.Limits{})
		completed <- queryOutcome{batch: batch, err: err}
	}()
	waitForActiveQuery(t, observer, pid)
	cancel()
	select {
	case outcome := <-completed:
		if outcome.err == nil || outcome.batch.TxStatus != 'E' || !outcome.batch.Connected {
			t.Fatalf("cancelled transaction got %#v, %v", outcome.batch, outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	executeSQL(t, session, "ROLLBACK; SELECT 1")
}

func TestIntegration_ByteaDistinguishesNullEmptyAndEscapeOutput(t *testing.T) {
	session := testSession(t)
	sql := `SELECT NULL::bytea, ''::bytea, decode('00ff5c410a', 'hex'), NULL::text, ''::text, '123456789012345678901234567890.123456789'::numeric`
	for _, output := range []string{"hex", "escape"} {
		executeSQL(t, session, "SET bytea_output = '"+output+"'")
		batch := executeSQL(t, session, sql)
		row := batch.Results[0].Rows[0]
		if !row[0].Null || row[1].Null || len(row[1].Bytes) != 0 || row[2].Null || !bytes.Equal(row[2].Bytes, []byte{0, 255, '\\', 'A', '\n'}) || !row[3].Null || row[4].Null || row[4].Text != "" {
			t.Errorf("%s output lost NULL or byte fidelity: %#v", output, row)
		}
		if row[5].Text != "123456789012345678901234567890.123456789" {
			t.Errorf("numeric lost precision: %s", row[5].Text)
		}
		if output == "escape" && !strings.Contains(row[2].Text, `\000`) {
			t.Errorf("original bytea escape text was not preserved: %q", row[2].Text)
		}
	}
}

func TestIntegration_BinaryCursorPreservesTypedValues(t *testing.T) {
	session := testSession(t)
	selectSQL := `SELECT 9223372036854775807::int8 AS n, 123456789012345678901234567890.1200::numeric AS decimal, decode('00ff5c41','hex') AS data, '{"n":123456789012345678901234567890,"a":[1,null]}'::jsonb AS payload, '00112233-4455-6677-8899-aabbccddeeff'::uuid AS uuid, NULL::bytea AS missing, ''::bytea AS empty`
	want := executeSQL(t, session, selectSQL).Results[0]
	batch := executeSQL(t, session, "BEGIN; DECLARE scrunch_binary BINARY CURSOR FOR "+selectSQL+"; FETCH ALL FROM scrunch_binary; ROLLBACK")
	if len(batch.Results) != 4 {
		t.Fatalf("got %d results, want 4", len(batch.Results))
	}
	got := batch.Results[2]
	if len(got.Rows) != 1 || len(got.Columns) != len(want.Columns) {
		t.Fatalf("unexpected binary result shape: %#v", got)
	}
	for i, value := range got.Rows[0] {
		expected := want.Rows[0][i]
		if value.Null != expected.Null || value.Text != expected.Text || !bytes.Equal(value.Bytes, expected.Bytes) {
			t.Errorf("binary column %s got %#v, want %#v", got.Columns[i].Name, value, expected)
		}
	}
	if batch.TxStatus != 'I' {
		t.Errorf("binary batch status got %q", batch.TxStatus)
	}
}

func TestIntegration_UnknownBinaryTypeErrorsAndDrainsLaterResults(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "BEGIN; CREATE TEMP TABLE scrunch_enum_scope (value int); CREATE TYPE pg_temp.scrunch_enum AS ENUM ('value')")
	batch, err := session.Execute(context.Background(), "DECLARE scrunch_enum_cursor BINARY CURSOR FOR SELECT 'value'::pg_temp.scrunch_enum; FETCH ALL FROM scrunch_enum_cursor; SELECT 42", result.Limits{})
	if err == nil || !strings.Contains(err.Error(), "unsupported binary type") || !batch.Connected || batch.TxStatus != 'T' {
		t.Fatalf("unexpected binary-type handling: %#v, %v", batch, err)
	}
	if len(batch.Results) != 3 || len(batch.Results[1].Rows) != 0 || !batch.Results[1].Truncated || batch.Results[2].Rows[0][0].Text != "42" {
		t.Errorf("unsupported binary result was misrepresented or not drained: %#v", batch.Results)
	}
	for _, notice := range batch.Notices {
		if strings.Contains(notice, "Display limit") {
			t.Errorf("decode failure was reported as a display limit: %s", notice)
		}
	}
	executeSQL(t, session, "ROLLBACK; SELECT 7")
}

func TestIntegration_CatalogQuotesNamesAndReportsColumnDetails(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, `BEGIN; CREATE SCHEMA "scrunch test ""schema"; CREATE TABLE "scrunch test ""schema"."order ""rows" ("row id" bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, payload jsonb NOT NULL DEFAULT '{}', optional text)`)
	schemas, err := session.Schemas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, schema := range schemas {
		found = found || schema.Name == `scrunch test "schema`
	}
	if !found {
		t.Fatal("quoted schema missing from catalog")
	}
	relations, err := session.Relations(context.Background(), `scrunch test "schema`)
	if err != nil || len(relations) != 2 {
		// The identity column also creates a sequence in this schema.
		t.Fatalf("relations got %#v, error %v", relations, err)
	}
	var table Relation
	for _, relation := range relations {
		if relation.Kind == "table" {
			table = relation
		}
	}
	if table.OID == 0 || table.Name != `order "rows` {
		t.Fatalf("quoted table missing: %#v", relations)
	}
	columns, err := session.Columns(context.Background(), table.OID)
	if err != nil || len(columns) != 3 {
		t.Fatalf("columns got %#v, error %v", columns, err)
	}
	if columns[0].Name != "row id" || !columns[0].PrimaryKey || columns[0].Nullable || columns[0].Type != "bigint" || columns[1].Nullable || !strings.Contains(columns[1].Default, "jsonb") || !columns[2].Nullable {
		t.Errorf("incorrect column metadata: %#v", columns)
	}
	executeSQL(t, session, "SELECT * FROM "+table.QualifiedName())
	if unrelated, err := session.Relations(context.Background(), `doesn't exist'; SELECT 1; --`); err != nil || len(unrelated) != 0 {
		t.Errorf("schema parameter was not treated literally: %#v, %v", unrelated, err)
	}
	executeSQL(t, session, "ROLLBACK")
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_temp_catalog (value int)")
	schemas, err = session.Schemas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, schema := range schemas {
		if strings.HasPrefix(schema.Name, "pg_temp_") {
			relations, err := session.Relations(context.Background(), schema.Name)
			if err != nil {
				t.Fatal(err)
			}
			for _, relation := range relations {
				found = found || relation.Name == "scrunch_temp_catalog"
			}
		}
	}
	if !found {
		t.Error("current session's temporary table missing from catalog")
	}
}

func TestIntegration_CopyAndNULPreflightPreventEarlierWrites(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_preflight (value int)")
	cases := []string{
		"INSERT INTO scrunch_preflight VALUES (1); COPY scrunch_preflight FROM STDIN;",
		"INSERT INTO scrunch_preflight VALUES (1); SELECT E'a'\n'b\\'; text'; COPY scrunch_preflight TO STDOUT;",
		"INSERT INTO scrunch_preflight VALUES (1); SELECT 2\x00; SELECT 3",
	}
	for _, sql := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		batch, err := session.Execute(ctx, sql, result.Limits{})
		cancel()
		if err == nil || !batch.Connected || batch.TxStatus != 'I' || len(batch.Results) != 0 {
			t.Fatalf("preflight sent SQL: %#v, %v", batch, err)
		}
		count := executeSQL(t, session, "SELECT count(*) FROM scrunch_preflight").Results[0].Rows[0][0].Text
		if count != "0" {
			t.Errorf("preflight allowed earlier writes: %s", count)
		}
	}
}

func TestIntegration_LexicalModeMatchesPostgreSQLAcrossSET(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "SET standard_conforming_strings = on")
	batch := executeSQL(t, session, `SET standard_conforming_strings = off; SELECT '\'; SELECT 2;`)
	if len(batch.Results) != 3 || batch.Results[1].Rows[0][0].Text != `\` || !session.BackslashEscapes() {
		t.Fatalf("SET changed parsing within its simple query message: %#v", batch)
	}
	sql := `SELECT 'a\'; b'; SELECT 3;`
	statements := sqltext.SplitMode(sql, session.BackslashEscapes())
	if len(statements) != 2 {
		t.Fatalf("split got %#v", statements)
	}
	batch = executeSQL(t, session, sql)
	if batch.Results[0].Rows[0][0].Text != "a'; b" || batch.Results[1].Rows[0][0].Text != "3" {
		t.Fatalf("ordinary escape semantics lost: %#v", batch)
	}
	batch = executeSQL(t, session, `SET standard_conforming_strings = on; SELECT 'a\'; b'; SELECT 4;`)
	if len(batch.Results) != 3 || batch.Results[1].Rows[0][0].Text != "a'; b" || session.BackslashEscapes() {
		t.Fatalf("SET-on mode mismatch: %#v", batch)
	}
	batch = executeSQL(t, session, "SELECT E'a' -- continuation\n'b\\'; COPY is text'; SELECT 5;")
	if batch.Results[0].Rows[0][0].Text != "ab'; COPY is text" || batch.Results[1].Rows[0][0].Text != "5" {
		t.Errorf("escape continuation mismatch: %#v", batch)
	}
}

func TestIntegration_ErrorPositionCountsCharacters(t *testing.T) {
	session := testSession(t)
	sql := "SELECT 'é龍', missing_column"
	_, err := session.Execute(context.Background(), sql, result.Limits{})
	var pgErr Error
	want := len([]rune("SELECT 'é龍', ")) + 1
	if !errors.As(err, &pgErr) || pgErr.Code != "42703" || pgErr.Position != want {
		t.Errorf("got error %v with position %d, want position %d", err, pgErr.Position, want)
	}
}

func TestIntegration_FormatterPreservesPostgreSQLResults(t *testing.T) {
	session := testSession(t)
	cases := []string{
		`SELECT 2*-3 AS n, 1 - -2 AS difference, .5::numeric AS half`,
		`SELECT (ARRAY[10,20,30])[1:2] AS slice, 3::bigint AS n`,
		`SELECT '{"a":["x"]}'::jsonb#>>'{a,0}' AS text, '{"a":1}'::jsonb->>'a' AS value`,
		"SELECT 'a'\n'b' AS joined",
		"SELECT E'a'\n'b' AS joined",
		"SELECT 'a'\n-- literal continuation\n'b' AS joined",
		`SELECT E'it\'s' AS text, $body$a; 'b'$body$ AS body, $body$c;d$body$ AS other`,
		`SELECT U&'d\0061t\+000061' AS word, 123456789012345678901234567890.1200::numeric AS n`,
		`WITH a AS (SELECT 2 AS n), b AS (SELECT n+1 AS n FROM a) SELECT a.n,b.n FROM a JOIN b ON b.n>a.n WHERE a.n=2 AND b.n=3`,
		"SELECT 1 /* outer /* nested */ comment */ AS n; SELECT 'second' AS n;",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			formatted, err := sqledit.Format(sql)
			if err != nil {
				t.Fatal(err)
			}
			before := executeSQL(t, session, sql)
			after := executeSQL(t, session, formatted)
			if len(before.Results) != len(after.Results) {
				t.Fatalf("result count changed from %d to %d", len(before.Results), len(after.Results))
			}
			for i, expected := range before.Results {
				actual := after.Results[i]
				if len(expected.Rows) != len(actual.Rows) || len(expected.Columns) != len(actual.Columns) || expected.Command != actual.Command {
					t.Fatalf("result shape changed: %#v versus %#v", expected, actual)
				}
				for col, column := range expected.Columns {
					if column != actual.Columns[col] {
						t.Errorf("column metadata changed: %#v versus %#v", column, actual.Columns[col])
					}
				}
				for row, values := range expected.Rows {
					for col, value := range values {
						got := actual.Rows[row][col]
						if value.Text != got.Text || value.Null != got.Null || !bytes.Equal(value.Bytes, got.Bytes) {
							t.Errorf("row %d column %d changed: %#v versus %#v", row, col, value, got)
						}
					}
				}
			}
		})
	}
}
