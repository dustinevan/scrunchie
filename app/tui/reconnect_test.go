package tui

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestShutdownPreventsUnstartedReconnect(t *testing.T) {
	m := New(Config{DSN: "host=must-not-be-contacted.invalid"})
	cmd := m.reconnect()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	msg := cmd().(reconnectMsg)
	if !errors.Is(msg.err, context.Canceled) || msg.session != nil {
		t.Fatal("shutdown did not prevent the pending connection from starting")
	}
}

func TestReconnectHandoffClosesUndeliveredSession(t *testing.T) {
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN must name an isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	original, err := postgres.Open(ctx, dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: original, DSN: dsn})
	cmd := m.reconnect()
	if cmd == nil {
		t.Fatal("missing reconnect command")
	}
	msg := cmd().(reconnectMsg)
	if msg.err != nil || msg.session == nil {
		t.Fatalf("reconnect: %v", msg.err)
	}
	// Simulate program shutdown between command completion and event delivery.
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := msg.session.Execute(ctx, "SELECT 1;", result.Limits{})
	if err == nil || batch.Connected {
		t.Fatal("workspace shutdown leaked the undelivered session")
	}
}

func TestReconnectStartsFreshSessionWithoutSQLReplay(t *testing.T) {
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN must name an isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn})
	defer func() {
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := session.Execute(ctx, "CREATE TEMP TABLE reconnect_test (n int);", result.Limits{}); err != nil {
		t.Fatal(err)
	}
	m.results().batch = result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{{Command: "original snapshot"}}}
	m.conn().query.sql = "CREATE TEMP TABLE reconnect_test (n int);"
	cmd := m.reconnect()
	msg := cmd().(reconnectMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	// Keep catalog loading queued while testing the new session directly.
	m.reconnectResult(msg)
	batch, err := m.conn().session.Execute(ctx, "SELECT to_regclass('pg_temp.reconnect_test') IS NULL;", result.Limits{})
	if err != nil || batch.Results[0].Rows[0][0].Text != "t" {
		t.Fatalf("new session replayed SQL: %v", err)
	}
	if m.results().batch.Results[0].Command != "original snapshot" {
		t.Fatal("reconnect discarded retained result snapshots")
	}
}
