package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/jackc/pgx/v5/pgconn"
)

func testConnection(t *testing.T) (string, connections.Profile) {
	t.Helper()
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN must name an isolated test database")
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parsing isolated test connection")
	}
	if !strings.HasPrefix(cfg.Host, "/") {
		t.Skip("profile session tests require an isolated Unix-socket cluster")
	}
	return dsn, connections.Profile{
		ID: "integration", Name: "Synthetic test database", Host: cfg.Host,
		Port: strconv.Itoa(int(cfg.Port)), Database: cfg.Database, User: cfg.User, SSLMode: "disable",
	}
}

// Exercise actual commands and their event-loop replies. Timer messages do
// not need further updates when no terminal renderer is running.
func finishCommands(t *testing.T, m *Model, command tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{command}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("commands did not settle")
		}
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case spinner.TickMsg:
			continue
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func closeTestModel(t *testing.T, m *Model) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
}

func TestFailedProfileSwitchPreservesActiveSessionAndResults(t *testing.T) {
	dsn, profile := testConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn, InitialSQL: "SELECT 7;", ProfilesPath: filepath.Join(t.TempDir(), ".scrunch")})
	closeTestModel(t, m)
	if _, err := session.Execute(ctx, "CREATE TEMP TABLE switch_marker (n int); INSERT INTO switch_marker VALUES (7);", result.Limits{}); err != nil {
		t.Fatal(err)
	}
	m.results().batch.Results = []result.Set{{Command: "previous result"}}
	profile.Port = "1"
	finishCommands(t, m, m.connectProfile(profile, "", false))
	if m.conn().session != session || m.connecting || m.modal.error == "" || !m.conn().connected {
		t.Fatal("failed switch lost the active connection or error")
	}
	batch, err := session.Execute(ctx, "SELECT n FROM switch_marker;", result.Limits{})
	if err != nil || batch.Results[0].Rows[0][0].Text != "7" || m.results().batch.Results[0].Command != "previous result" {
		t.Fatal("failed switch lost prior session data or results")
	}
}

func TestOpeningAnotherProfileKeepsTheFirstConnectionAndRemembersOnlySettings(t *testing.T) {
	dsn, profile := testConnection(t)
	other := os.Getenv("SCRUNCH_TEST_OTHER_DATABASE")
	if other == "" {
		t.Skip("SCRUNCH_TEST_OTHER_DATABASE must name the second isolated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), ".scrunch")
	m := New(Config{Session: session, DSN: dsn, InitialSQL: "SELECT 'keep this SQL';", ProfilesPath: path})
	closeTestModel(t, m)
	first := m.conn()
	m.results().batch.Results = []result.Set{{Command: "first target"}}
	m.history = []historyEntry{{sql: "SELECT 'first target';", conn: first.id, connName: first.name}}
	first.catalog = []catalogNode{{kind: "schema", name: "first_target", parent: -1}}
	profile.Database = other
	finishCommands(t, m, m.connectProfile(profile, "", true))
	second := m.conn()
	if second == nil || second == first || second.session == session || second.profile.Database != other || len(m.connections) != 2 {
		t.Fatalf("second connection did not open: %s", m.status)
	}
	if !strings.HasPrefix(m.connectionSummary(), other+" · ") {
		t.Fatal("header hid the actual database behind its connection name")
	}
	// The first buffer had SQL, so the new connection got its own buffer.
	if m.bufferIndex != 1 || m.buffers[0].conn != first.id || m.buffers[1].conn != second.id {
		t.Fatalf("buffers bound to %d and %d", m.buffers[0].conn, m.buffers[1].conn)
	}
	if string(m.buffers[0].editor.text) != "SELECT 'keep this SQL';" || m.buffers[0].results.batch.Results[0].Command != "first target" || len(m.history) != 1 {
		t.Fatal("opening another connection discarded the first one's SQL, results, or history")
	}
	if first.session != session || len(first.catalog) != 1 || first.catalog[0].name != "first_target" {
		t.Fatal("opening another connection replaced the first one's session or catalog")
	}
	for _, node := range second.catalog {
		if node.name == "first_target" {
			t.Fatal("the new connection shows the first connection's catalog")
		}
	}
	batch, err := second.session.Execute(ctx, "SELECT current_database();", result.Limits{})
	if err != nil || batch.Results[0].Rows[0][0].Text != other {
		t.Fatal("query did not use selected database")
	}
	if batch, err := session.Execute(ctx, "SELECT current_database();", result.Limits{}); err != nil || batch.Results[0].Rows[0][0].Text != "scrunch_test" {
		t.Fatalf("the first connection closed: %v", err)
	}
	saved, err := connections.Load(path)
	if err != nil || len(saved) != 1 || saved[0] != profile {
		t.Fatalf("connection settings did not persist: %v", err)
	}
	// Activating the open profile again switches to it instead of opening a duplicate.
	m.switchBuffer(0)
	if cmd := m.connectProfile(profile, "", false); cmd != nil || m.connecting || len(m.connections) != 2 || m.conn() != second {
		t.Fatalf("an open profile was opened twice: %s", m.status)
	}
}

func TestTransactionOnOneConnectionDoesNotBlockOpeningAnother(t *testing.T) {
	dsn, profile := testConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn})
	closeTestModel(t, m)
	runBuffer(t, m, "BEGIN;")
	first := m.conn()
	if first.txStatus != 'T' {
		t.Fatalf("transaction status %c", first.txStatus)
	}
	finishCommands(t, m, m.connectProfile(profile, "", false))
	if len(m.connections) != 2 || m.conn() == first || m.conn().txStatus != 'I' {
		t.Fatalf("an open transaction blocked or leaked into a new connection: %s", m.status)
	}
	batch, err := session.Execute(ctx, "SELECT 1;", result.Limits{})
	if err != nil || batch.TxStatus != 'T' || first.session != session {
		t.Fatal("opening another connection changed the first one's transaction")
	}
}

func TestPasswordEnvironmentIsRereadOnAuthenticatedReconnect(t *testing.T) {
	dsn, profile := testConnection(t)
	if os.Getenv("SCRUNCH_TEST_PASSWORD_USER") != "scrunch_password" {
		t.Skip("requires isolated SCRAM role scrunch_password initialized with password_one")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := control.Execute(ctx, "ALTER ROLE scrunch_password PASSWORD 'password_one';", result.Limits{}); err != nil {
			t.Error(err)
		}
		if err := control.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("SCRUNCH_SYNTHETIC_PASSWORD", "password_one")
	profile.User, profile.PasswordEnv = "scrunch_password", "SCRUNCH_SYNTHETIC_PASSWORD"
	path := filepath.Join(t.TempDir(), ".scrunch")
	m := New(Config{ProfilesPath: path})
	closeTestModel(t, m)
	finishCommands(t, m, m.connectProfile(profile, "", true))
	if m.conn() == nil || !m.conn().connected {
		t.Fatalf("first password authentication failed: %s", m.status)
	}
	before := m.conn().session
	if _, err := control.Execute(ctx, "ALTER ROLE scrunch_password PASSWORD 'password_two';", result.Limits{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRUNCH_SYNTHETIC_PASSWORD", "password_two")
	finishCommands(t, m, m.reconnect())
	if m.conn().session == before || !m.conn().connected || m.statusError {
		t.Fatalf("reconnect did not use changed environment value: %s", m.status)
	}
	t.Setenv("SCRUNCH_SYNTHETIC_PASSWORD", "")
	before = m.conn().session
	if cmd := m.reconnect(); cmd != nil || m.conn().session != before || !strings.Contains(m.status, "SCRUNCH_SYNTHETIC_PASSWORD") {
		t.Fatal("empty password environment did not fail before replacing the session")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "SCRUNCH_SYNTHETIC_PASSWORD") || strings.Contains(string(data), "password_one") || strings.Contains(string(data), "password_two") {
		t.Fatal("settings did not keep only the password variable name")
	}
	if strings.Contains(m.View().Content+m.status+m.lastMessage, "password_one") || strings.Contains(m.View().Content+m.status+m.lastMessage, "password_two") {
		t.Fatal("password appeared in connection output")
	}
}

func TestDatabaseDiscoveryPreservesCurrentTransaction(t *testing.T) {
	dsn, profile := testConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn})
	closeTestModel(t, m)
	runBuffer(t, m, "BEGIN; CREATE TEMP TABLE lookup_marker (n int);")
	m.openConnectionForm(profile)
	finishCommands(t, m, m.discoverDatabases(profile, ""))
	if m.conn().session != session || m.conn().txStatus != 'T' || m.modal.kind != "database-names" || len(m.databaseNames) == 0 {
		t.Fatalf("lookup changed session or failed: %s", m.modal.error)
	}
	batch, err := session.Execute(ctx, "SELECT to_regclass('pg_temp.lookup_marker') IS NOT NULL;", result.Limits{})
	if err != nil || batch.TxStatus != 'T' || batch.Results[0].Rows[0][0].Text != "t" {
		t.Fatal("lookup lost the current transaction")
	}
}

func TestShutdownPreventsUnstartedDatabaseDiscovery(t *testing.T) {
	m := New(Config{})
	profile := connections.Profile{Host: "must-not-be-contacted.invalid", Database: "postgres", User: "test", SSLMode: "disable"}
	cmd := m.discoverDatabases(profile, "")
	if cmd == nil {
		t.Fatal("missing lookup command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	msg := cmd().(databaseNamesMsg)
	if !errors.Is(msg.err, context.Canceled) || len(msg.names) > 0 {
		t.Fatal("shutdown did not prevent queued discovery")
	}
}
