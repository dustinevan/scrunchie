package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dustinevan/scrunchie/pkg/result"
)

func executePage(t *testing.T, session *Session, sql string, page int) (result.Batch, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return session.Execute(ctx, sql, result.Limits{PageRows: page})
}

func openCursors(t *testing.T, session *Session) string {
	t.Helper()
	return executeSQL(t, session, "SELECT count(*) FROM pg_catalog.pg_cursors").Results[0].Rows[0][0].Text
}

func TestIntegration_PageFetchesFirstRowsAndReportsMore(t *testing.T) {
	session := testSession(t)
	batch, err := executePage(t, session, "SELECT id, mbi, attrs, photo FROM claims.beneficiary ORDER BY id; -- first page", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Truncated || batch.TxStatus != 'I' || !batch.Connected {
		t.Fatalf("unexpected page batch: results=%d truncated=%v tx=%q", len(batch.Results), batch.Truncated, batch.TxStatus)
	}
	set := batch.Results[0]
	if len(set.Rows) != 1000 || set.RowCount != 1000 || !set.HasMore || set.Truncated || set.Command != "SELECT 1000" {
		t.Fatalf("page rows=%d count=%d more=%v truncated=%v command=%q", len(set.Rows), set.RowCount, set.HasMore, set.Truncated, set.Command)
	}
	if len(set.Columns) != 4 || set.Columns[2].Type != "jsonb" || set.Rows[0][0].Text != "1" || set.Rows[999][0].Text != "1000" {
		t.Fatalf("page columns or order changed: %#v first=%q last=%q", set.Columns, set.Rows[0][0].Text, set.Rows[999][0].Text)
	}
	if got := openCursors(t, session); got != "0" {
		t.Errorf("%s cursors left open after an idle page", got)
	}
}

func TestIntegration_LargerPageReRunsFromTheStart(t *testing.T) {
	session := testSession(t)
	sql := "SELECT id FROM claims.beneficiary ORDER BY id"
	batch, err := executePage(t, session, sql, 10000)
	if err != nil {
		t.Fatal(err)
	}
	set := batch.Results[0]
	if len(set.Rows) != 10000 || !set.HasMore || set.Rows[0][0].Text != "1" || set.Rows[9999][0].Text != "10000" {
		t.Fatalf("second page rows=%d more=%v", len(set.Rows), set.HasMore)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	batch, err = session.Execute(ctx, sql, result.Limits{MaxRows: 100000, PageRows: 100000})
	if err != nil {
		t.Fatal(err)
	}
	set = batch.Results[0]
	if len(set.Rows) != 50000 || set.RowCount != 50000 || set.HasMore || set.Command != "SELECT 50000" {
		t.Fatalf("complete page rows=%d count=%d more=%v command=%q", len(set.Rows), set.RowCount, set.HasMore, set.Command)
	}
	batch, err = executePage(t, session, "SELECT id FROM claims.beneficiary WHERE id <= 1000", 1000)
	if err != nil || batch.Results[0].HasMore || len(batch.Results[0].Rows) != 1000 {
		t.Fatalf("exact page reported more rows: %#v, %v", batch.Results, err)
	}
}

func TestIntegration_PageStopsServerAfterProbeRow(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMPORARY SEQUENCE scrunch_page_probe")
	batch, err := executePage(t, session, "SELECT nextval('scrunch_page_probe') FROM generate_series(1, 100000)", 1000)
	if err != nil || !batch.Results[0].HasMore || len(batch.Results[0].Rows) != 1000 {
		t.Fatalf("page got %#v, %v", batch.Results, err)
	}
	if got := executeSQL(t, session, "SELECT currval('scrunch_page_probe')").Results[0].Rows[0][0].Text; got != "1001" {
		t.Errorf("server computed %s rows, want the page plus one probe row", got)
	}
}

func TestIntegration_PageInsideTransactionKeepsTransactionOpen(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_page_tx (value int)")
	executeSQL(t, session, "BEGIN; INSERT INTO scrunch_page_tx SELECT generate_series(1, 30)")
	batch, err := executePage(t, session, "SELECT value FROM scrunch_page_tx ORDER BY value", 10)
	if err != nil || batch.TxStatus != 'T' || !batch.Results[0].HasMore || len(batch.Results[0].Rows) != 10 {
		t.Fatalf("transaction page got %#v, %v", batch, err)
	}
	if got := openCursors(t, session); got != "0" {
		t.Errorf("%s cursors left open inside the transaction", got)
	}
	batch = executeSQL(t, session, "SELECT count(*) FROM scrunch_page_tx")
	if batch.TxStatus != 'T' || batch.Results[0].Rows[0][0].Text != "30" {
		t.Fatalf("page ended or changed the user's transaction: %#v", batch)
	}
	executeSQL(t, session, "ROLLBACK")
	if got := executeSQL(t, session, "SELECT count(*) FROM scrunch_page_tx").Results[0].Rows[0][0].Text; got != "0" {
		t.Errorf("rolled-back rows remain: %s", got)
	}
}

func TestIntegration_FailedTransactionRunsQueryUnchanged(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "BEGIN")
	if _, err := executePage(t, session, "SELECT 1/0", 10); err == nil {
		t.Fatal("division by zero succeeded")
	}
	batch, err := executePage(t, session, "SELECT 1", 10)
	var pgErr Error
	if !errors.As(err, &pgErr) || pgErr.Code != "25P02" || batch.TxStatus != 'E' || pgErr.Position != 0 {
		t.Fatalf("failed transaction got %#v, %v", batch, err)
	}
	if batch := executeSQL(t, session, "ROLLBACK"); batch.TxStatus != 'I' {
		t.Errorf("ROLLBACK status %q", batch.TxStatus)
	}
}

func TestIntegration_PageErrorsMatchUnwrappedQuery(t *testing.T) {
	session := testSession(t)
	for _, sql := range []string{
		"SELECT 'é龍', missing_column FROM claims.beneficiary",
		"\n  -- leading comment\nSELECT nope FROM claims.beneficiary;",
		"SELECT (1",
		"SELECT * FROM",
		"SELECT $1",
		"WITH x AS (SELECT 1) SELECT y FROM x -- trailing",
	} {
		_, plain := session.Execute(context.Background(), sql, result.Limits{})
		for _, tx := range []string{"", "BEGIN"} {
			if tx != "" {
				executeSQL(t, session, tx)
			}
			_, paged := executePage(t, session, sql, 10)
			var want, got Error
			if !errors.As(plain, &want) || !errors.As(paged, &got) {
				t.Fatalf("%q: plain %v, paged %v", sql, plain, paged)
			}
			if got.Message != want.Message || got.Code != want.Code || got.Position != want.Position || want.Position == 0 {
				t.Errorf("%q %s: paged error %q at %d, want %q at %d", sql, tx, got.Message, got.Position, want.Message, want.Position)
			}
			if tx != "" {
				executeSQL(t, session, "ROLLBACK")
			}
		}
	}
	// PostgreSQL stores a FETCH result before sending it, so a runtime error
	// inside the page arrives without partial rows.
	batch, err := executePage(t, session, "SELECT 1/(id - 5) FROM claims.beneficiary ORDER BY id", 1000)
	var pgErr Error
	if !errors.As(err, &pgErr) || pgErr.Code != "22012" || batch.TxStatus != 'I' || len(batch.Results) != 0 {
		t.Fatalf("runtime error got %#v, %v", batch, err)
	}
}

func TestIntegration_PageKeepsNotices(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE FUNCTION pg_temp.scrunch_noisy(n int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE 'row %', n; RETURN n; END $$")
	batch, err := executePage(t, session, "SELECT pg_temp.scrunch_noisy(g) FROM generate_series(1, 5) g", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Results[0].HasMore || len(batch.Results[0].Rows) != 2 {
		t.Fatalf("notice page got %#v", batch.Results)
	}
	if strings.Join(batch.Notices, "|") != "NOTICE: row 1|NOTICE: row 2|NOTICE: row 3" {
		t.Errorf("notices %q, want the page and probe rows only", batch.Notices)
	}
}

func TestIntegration_PageAcceptsEveryReadForm(t *testing.T) {
	session := testSession(t)
	for _, sql := range []string{
		"VALUES (1), (2), (3)",
		"TABLE claims.claim_line",
		"(SELECT 1) UNION ALL (SELECT 2) UNION ALL (SELECT 3) ORDER BY 1",
		"((SELECT g FROM generate_series(1, 9) g))",
		"WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r WHERE n < 5) SELECT * FROM r",
		"select * from claims.md_beneficiaries -- trailing comment",
		"SELECT claims.paid_total(id) FROM claims.beneficiary;\n-- after the semicolon",
		`SELECT "Id", "select" FROM public."Mixed Case" UNION ALL VALUES (1, 'a'), (2, 'b;'), (3, $$c$$)`,
	} {
		batch, err := executePage(t, session, sql, 2)
		if err != nil {
			t.Errorf("%q: %v", sql, err)
			continue
		}
		if len(batch.Results) != 1 || !batch.Results[0].HasMore || len(batch.Results[0].Rows) != 2 || batch.Results[0].Command != "SELECT 2" || batch.TxStatus != 'I' {
			t.Errorf("%q: got %#v", sql, batch)
		}
	}
}

func TestIntegration_IneligibleSQLRunsUnchanged(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE scrunch_page_writes (value int); INSERT INTO scrunch_page_writes SELECT generate_series(1, 5)")
	for _, test := range []struct {
		sql     string
		results int
	}{
		{"SELECT 1; SELECT generate_series(1, 5)", 2},
		{"SELECT value FROM scrunch_page_writes FOR UPDATE", 1},
		{"WITH gone AS (DELETE FROM scrunch_page_writes WHERE value = 5 RETURNING value) SELECT value FROM gone", 1},
		{"SELECT value INTO TEMP scrunch_page_into FROM scrunch_page_writes", 1},
		{"SELECT 'C:\\path', value FROM scrunch_page_writes", 1},
	} {
		batch, err := executePage(t, session, test.sql, 1)
		if err != nil || len(batch.Results) != test.results {
			t.Fatalf("%q: got %#v, %v", test.sql, batch, err)
		}
		for _, set := range batch.Results {
			if set.HasMore {
				t.Errorf("%q was paged", test.sql)
			}
		}
	}
	if got := executeSQL(t, session, "SELECT count(*) FROM scrunch_page_writes").Results[0].Rows[0][0].Text; got != "4" {
		t.Errorf("data-modifying WITH ran %s rows, want one deletion", got)
	}
	if got := executeSQL(t, session, "SELECT count(*) FROM scrunch_page_into").Results[0].Rows[0][0].Text; got != "4" {
		t.Errorf("SELECT INTO created %s rows", got)
	}
}

func TestIntegration_RejectedCursorRunsQueryUnchanged(t *testing.T) {
	session := testSession(t)
	// Only a cursor with this session's random name can collide.
	executeSQL(t, session, "DECLARE "+session.pageCursor+" CURSOR WITH HOLD FOR SELECT 1")
	batch, err := executePage(t, session, "SELECT generate_series(1, 5)", 2)
	if err != nil || len(batch.Results) != 1 || batch.Results[0].HasMore || len(batch.Results[0].Rows) != 5 || batch.Results[0].Command != "SELECT 5" || batch.TxStatus != 'I' {
		t.Fatalf("name collision got %#v, %v", batch, err)
	}
	executeSQL(t, session, "CLOSE "+session.pageCursor)
}

func TestIntegration_PageCursorNameIsRandomPerSession(t *testing.T) {
	first, second := testSession(t), testSession(t)
	for _, name := range []string{first.pageCursor, second.pageCursor} {
		suffix, ok := strings.CutPrefix(name, "scrunch_page_")
		if _, err := hex.DecodeString(suffix); !ok || len(suffix) != 16 || err != nil || strings.ToLower(suffix) != suffix {
			t.Fatalf("cursor name %q is not scrunch_page_ and 16 lower-case hex digits", name)
		}
	}
	if first.pageCursor == second.pageCursor {
		t.Fatalf("two sessions share cursor name %q", first.pageCursor)
	}
}

func TestIntegration_UserCursorNamedScrunchPageKeepsTransaction(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "BEGIN; DECLARE scrunch_page CURSOR FOR SELECT 'mine'")
	batch, err := executePage(t, session, "SELECT generate_series(1, 5)", 2)
	if err != nil || batch.TxStatus != 'T' || len(batch.Results) != 1 || !batch.Results[0].HasMore || len(batch.Results[0].Rows) != 2 {
		t.Fatalf("paged read beside the user's cursor got %#v, %v", batch, err)
	}
	batch = executeSQL(t, session, "FETCH scrunch_page")
	if batch.TxStatus != 'T' || batch.Results[0].Rows[0][0].Text != "mine" {
		t.Fatalf("the user's cursor changed: %#v", batch)
	}
	executeSQL(t, session, "ROLLBACK")
}

func TestIntegration_PageByteLimitAndCancel(t *testing.T) {
	session := testSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	batch, err := session.Execute(ctx, "SELECT repeat('x', 4096) FROM generate_series(1, 100)", result.Limits{MaxBytes: 64 << 10, PageRows: 50})
	if err != nil {
		t.Fatal(err)
	}
	set := batch.Results[0]
	if !batch.Truncated || !set.Truncated || !set.HasMore || set.RowCount != 50 || len(set.Rows) >= 50 || len(set.Rows) == 0 {
		t.Fatalf("byte-limited page rows=%d count=%d more=%v truncated=%v", len(set.Rows), set.RowCount, set.HasMore, set.Truncated)
	}

	observer := testSession(t)
	pid := session.conn.PID()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan queryOutcome, 1)
	go func() {
		batch, err := session.Execute(ctx, "SELECT pg_sleep(20) FROM generate_series(1, 3)", result.Limits{PageRows: 1000})
		completed <- queryOutcome{batch: batch, err: err}
	}()
	waitForActiveQuery(t, observer, pid)
	cancel()
	select {
	case outcome := <-completed:
		var pgErr Error
		if !errors.As(outcome.err, &pgErr) || pgErr.Code != "57014" || !outcome.batch.Connected || outcome.batch.TxStatus != 'I' {
			t.Fatalf("cancel got %#v, %v", outcome.batch, outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation took longer than 3 seconds")
	}
	if got := openCursors(t, session); got != "0" {
		t.Errorf("%s cursors left open after cancellation", got)
	}
}

func TestIntegration_DisplayLimitAddsNoSyntheticNotice(t *testing.T) {
	session := testSession(t)
	batch, err := session.Execute(context.Background(), "SELECT generate_series(1, 50)", result.Limits{MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Truncated || batch.Results[0].RowCount != 50 || len(batch.Results[0].Rows) != 10 || batch.Results[0].HasMore || len(batch.Notices) != 0 {
		t.Fatalf("drained result got truncated=%v count=%d notices=%q", batch.Truncated, batch.Results[0].RowCount, batch.Notices)
	}
}
