package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dustinevan/scrunchie/pkg/result"
)

// Every write below targets the synthetic table inside a transaction that is
// rolled back, so an unexpected success cannot change the fixture data.
const fixtureWrite = `BEGIN; INSERT INTO public."Mixed Case" ("Id", "select") VALUES (-1, 'read-only probe')`

func openMode(t *testing.T, dsn string, options Options) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Open(ctx, dsn, options)
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

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN is unset; integration tests need an isolated PostgreSQL database")
	}
	return dsn
}

// tryWrite reports the write's SQLSTATE, or "" when it succeeded, then rolls back.
func tryWrite(t *testing.T, session *Session) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.Execute(ctx, fixtureWrite, result.Limits{})
	if batch := executeSQL(t, session, "ROLLBACK"); batch.TxStatus != 'I' {
		t.Fatalf("rollback left status %q", batch.TxStatus)
	}
	if err == nil {
		return ""
	}
	var pgErr Error
	if !errors.As(err, &pgErr) {
		t.Fatalf("write failed without a PostgreSQL error: %v", err)
	}
	return pgErr.Code
}

func TestIntegration_DefaultSessionRefusesWrites(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	if !session.ReadOnly() || session.Standby() {
		t.Fatalf("default session ReadOnly=%t Standby=%t", session.ReadOnly(), session.Standby())
	}
	if code := tryWrite(t, session); code != "25006" {
		t.Fatalf("default session write got SQLSTATE %q, want 25006", code)
	}
	batch, err := session.Execute(context.Background(), "CREATE TEMP TABLE read_only_probe (n int)", result.Limits{})
	var pgErr Error
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" || !batch.ReadOnly || !batch.Connected {
		t.Fatalf("temporary table got %v, batch read-only %t", err, batch.ReadOnly)
	}
	if batch := executeSQL(t, session, "SELECT count(*) FROM claims.beneficiary"); batch.Results[0].Rows[0][0].Text == "" || !batch.ReadOnly {
		t.Fatal("read-only session could not read")
	}
}

func TestIntegration_AllowWritesSessionWrites(t *testing.T) {
	session := openMode(t, testDSN(t), Options{AllowWrites: true})
	if session.ReadOnly() {
		t.Fatal("writable session reported read-only")
	}
	if code := tryWrite(t, session); code != "" {
		t.Fatalf("writable session write failed with %s", code)
	}
}

func TestIntegration_ResetRestoresStartupReadOnlyMode(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	batch := executeSQL(t, session, "SET default_transaction_read_only = off")
	if batch.ReadOnly || session.ReadOnly() {
		t.Fatal("manual SET off was not reflected after Execute")
	}
	if code := tryWrite(t, session); code != "" {
		t.Fatalf("write after manual SET failed with %s", code)
	}
	for _, reset := range []string{"RESET ALL", "DISCARD ALL", "RESET default_transaction_read_only"} {
		executeSQL(t, session, "SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE")
		if session.ReadOnly() {
			t.Fatal("SET SESSION CHARACTERISTICS was not reflected")
		}
		if batch := executeSQL(t, session, reset); !batch.ReadOnly || !session.ReadOnly() {
			t.Fatalf("%s did not restore the read-only startup value", reset)
		}
		if code := tryWrite(t, session); code != "25006" {
			t.Fatalf("write after %s got SQLSTATE %q", reset, code)
		}
	}
}

func TestIntegration_SetReadOnlyTogglesLaterTransactions(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.SetReadOnly(ctx, false); err != nil {
		t.Fatal(err)
	}
	if session.ReadOnly() {
		t.Fatal("allowing writes left the session read-only")
	}
	if code := tryWrite(t, session); code != "" {
		t.Fatalf("write after allowing writes failed with %s", code)
	}
	// RESET ALL returns to the startup default, not to the toggled value.
	executeSQL(t, session, "RESET ALL")
	if !session.ReadOnly() {
		t.Fatal("RESET ALL kept a runtime write toggle")
	}
	if err := session.SetReadOnly(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := session.SetReadOnly(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !session.ReadOnly() {
		t.Fatal("read-only toggle was not reflected")
	}
	if code := tryWrite(t, session); code != "25006" {
		t.Fatalf("write after read-only toggle got SQLSTATE %q", code)
	}
	executeSQL(t, session, "BEGIN")
	err := session.SetReadOnly(ctx, false)
	if err == nil || !strings.Contains(err.Error(), "open transaction") {
		t.Fatalf("toggle inside a transaction got %v", err)
	}
	if batch := executeSQL(t, session, "SHOW default_transaction_read_only"); batch.TxStatus != 'T' || batch.Results[0].Rows[0][0].Text != "on" {
		t.Fatal("refused toggle changed the transaction or setting")
	}
	executeSQL(t, session, "ROLLBACK")
}

func TestIntegration_SetReadOnlyWaitsForTheConnection(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := session.Execute(context.Background(), "SELECT pg_sleep(0.3)", result.Limits{})
		done <- err
	}()
	<-started
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.SetReadOnly(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if session.ReadOnly() {
		t.Fatal("toggle after a running query did not apply")
	}
}

func TestIntegration_ConnectionStringCannotDisableReadOnly(t *testing.T) {
	dsn := testDSN(t)
	if strings.Contains(dsn, "://") {
		t.Skip("appending settings needs keyword connection syntax")
	}
	for _, extra := range []string{
		" options='-c default_transaction_read_only=off'",
		" default_transaction_read_only=off",
		" DEFAULT_TRANSACTION_READ_ONLY=off",
	} {
		session := openMode(t, dsn+extra, Options{})
		if !session.ReadOnly() {
			t.Fatalf("%q weakened read-only mode", extra)
		}
		if code := tryWrite(t, session); code != "25006" {
			t.Fatalf("%q allowed a write (SQLSTATE %q)", extra, code)
		}
	}
	session := openMode(t, dsn+" options='-c default_transaction_read_only=on'", Options{AllowWrites: true})
	if !session.ReadOnly() {
		t.Fatal("writable session ignored the connection string's read-only setting")
	}
}

// refreshUnreported runs Execute's write-mode check as if the server had not
// reported default_transaction_read_only, as before PostgreSQL 14.
func refreshUnreported(t *testing.T, session *Session) bool {
	t.Helper()
	<-session.available
	defer func() { session.available <- struct{}{} }()
	return session.refreshWriteMode("")
}

func TestIntegration_ReadWriteTransactionInReadOnlySessionReportsWritable(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	for _, c := range []struct {
		steps []string
		end   string
	}{
		{[]string{"BEGIN READ WRITE"}, "COMMIT"},
		{[]string{"START TRANSACTION ISOLATION LEVEL READ COMMITTED, READ WRITE"}, "ROLLBACK"},
		{[]string{"BEGIN", "SET TRANSACTION READ WRITE"}, "COMMIT"},
		{[]string{"BEGIN", "SET transaction_read_only = off"}, "ROLLBACK"},
		{[]string{"BEGIN; SET LOCAL transaction_read_only TO off"}, "END"},
	} {
		var batch result.Batch
		for _, step := range c.steps {
			batch = executeSQL(t, session, step)
		}
		if batch.ReadOnly || batch.TxStatus != 'T' || !session.ReadOnly() {
			t.Fatalf("%q: batch read-only %t, status %q, session read-only %t", c.steps, batch.ReadOnly, batch.TxStatus, session.ReadOnly())
		}
		// The write is undone before the transaction ends either way.
		batch = executeSQL(t, session, `SAVEPOINT probe; INSERT INTO public."Mixed Case" ("Id", "select") VALUES (-1, 'write-mode probe'); ROLLBACK TO SAVEPOINT probe`)
		if batch.ReadOnly || batch.TxStatus != 'T' {
			t.Fatalf("%q: write left batch read-only %t, status %q", c.steps, batch.ReadOnly, batch.TxStatus)
		}
		if batch = executeSQL(t, session, c.end); !batch.ReadOnly || batch.TxStatus != 'I' || !session.ReadOnly() {
			t.Fatalf("%q then %s: batch read-only %t, status %q", c.steps, c.end, batch.ReadOnly, batch.TxStatus)
		}
	}
	if count := executeSQL(t, session, `SELECT count(*) FROM public."Mixed Case" WHERE "Id" = -1`).Results[0].Rows[0][0].Text; count != "0" {
		t.Fatalf("%s probe rows remain", count)
	}
}

func TestIntegration_ReadOnlyTransactionInWritableSessionReportsReadOnly(t *testing.T) {
	session := openMode(t, testDSN(t), Options{AllowWrites: true})
	for _, steps := range [][]string{{"BEGIN READ ONLY"}, {"BEGIN", "SET TRANSACTION READ ONLY"}} {
		var batch result.Batch
		for _, step := range steps {
			batch = executeSQL(t, session, step)
		}
		if !batch.ReadOnly || session.ReadOnly() {
			t.Fatalf("%q: batch read-only %t, session read-only %t", steps, batch.ReadOnly, session.ReadOnly())
		}
		if batch = executeSQL(t, session, "ROLLBACK"); batch.ReadOnly {
			t.Fatalf("%q then ROLLBACK reported read-only", steps)
		}
	}
}

func TestIntegration_FailedTransactionKeepsItsWriteMode(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	executeSQL(t, session, "BEGIN READ WRITE")
	batch, err := session.Execute(context.Background(), "SELECT 1/0", result.Limits{})
	if err == nil || batch.TxStatus != 'E' || batch.ReadOnly {
		t.Fatalf("failed statement got %v, status %q, read-only %t", err, batch.TxStatus, batch.ReadOnly)
	}
	if refreshUnreported(t, session) {
		t.Fatal("failed transaction lost its write mode")
	}
	if batch := executeSQL(t, session, "ROLLBACK"); batch.TxStatus != 'I' || !batch.ReadOnly {
		t.Fatalf("ROLLBACK got status %q, read-only %t", batch.TxStatus, batch.ReadOnly)
	}
}

func TestIntegration_WriteModeCheckHidesItsNotices(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	// The superuser fixture can log statements to the client.
	executeSQL(t, session, "BEGIN READ WRITE; SET LOCAL log_statement = 'all'; SET LOCAL client_min_messages = 'log'")
	for range 2 {
		batch := executeSQL(t, session, "SELECT 1")
		if len(batch.Notices) != 1 || !strings.Contains(batch.Notices[0], "SELECT 1") || batch.ReadOnly || batch.TxStatus != 'T' {
			t.Fatalf("got notices %q, read-only %t, status %q", batch.Notices, batch.ReadOnly, batch.TxStatus)
		}
	}
	executeSQL(t, session, "ROLLBACK")
}

func TestIntegration_UnreportedDefaultUsesShowOutsideTransactions(t *testing.T) {
	session := openMode(t, testDSN(t), Options{})
	for _, c := range []struct {
		sql      string
		readOnly bool
	}{
		{"SET default_transaction_read_only = off", false},
		{"RESET ALL", true},
		{"SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE", false},
		{"DISCARD ALL", true},
	} {
		executeSQL(t, session, c.sql)
		// A server that does not report the setting leaves the old mode.
		session.readOnly.Store(!c.readOnly)
		if got := refreshUnreported(t, session); got != c.readOnly || session.ReadOnly() != c.readOnly {
			t.Fatalf("%s: got %t, session %t, want %t", c.sql, got, session.ReadOnly(), c.readOnly)
		}
	}
	// Inside a transaction, SHOW describes only that transaction.
	executeSQL(t, session, "BEGIN READ WRITE")
	if refreshUnreported(t, session) || !session.ReadOnly() {
		t.Fatalf("transaction mode leaked into the session default")
	}
	if batch := executeSQL(t, session, "SHOW transaction_read_only"); batch.TxStatus != 'T' || batch.Results[0].Rows[0][0].Text != "off" {
		t.Fatalf("write-mode check changed the transaction: %#v", batch)
	}
	executeSQL(t, session, "ROLLBACK")
}
