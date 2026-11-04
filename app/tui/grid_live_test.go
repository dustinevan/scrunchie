package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// TestLiveSortAndCopyRoundTripThroughPostgreSQL sorts real numeric, date, and
// bytea values, then executes the copied INSERTs and IN list to prove their
// literals reproduce the original rows exactly.
func TestLiveSortAndCopyRoundTripThroughPostgreSQL(t *testing.T) {
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN must name an isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	sql := "SELECT id, name, birth_date, state, attrs, photo, balance, created_at FROM claims.beneficiary WHERE id <= 300 ORDER BY id;"
	m := New(Config{Session: session, DSN: dsn, InitialSQL: sql})
	closeTestModel(t, m)
	m.buffers[0].editor = newEditor(sql)
	finishCommands(t, m, press(m, 'j', tea.ModCtrl))
	if m.focus != resultsPane || len(m.results().batch.Results) != 1 || len(m.gridRows()) != 300 {
		t.Fatalf("query did not produce 300 rows in focused results: %s", m.status)
	}
	set := m.results().batch.Results[0]
	cellAt := func(position, column int) result.Cell {
		return set.Rows[m.gridRows()[position]][column]
	}

	m.results().column = 6
	press(m, 's')
	press(m, 's')
	for position := 1; position < 300; position++ {
		previous, _ := new(big.Rat).SetString(cellAt(position-1, 6).Text)
		current, _ := new(big.Rat).SetString(cellAt(position, 6).Text)
		if previous.Cmp(current) < 0 {
			t.Fatalf("balance descending broke at %d: %s then %s", position, cellAt(position-1, 6).Text, cellAt(position, 6).Text)
		}
	}
	m.results().column = 2
	press(m, 's')
	for position := 1; position < 300; position++ {
		if cellAt(position-1, 2).Text > cellAt(position, 2).Text {
			t.Fatalf("birth_date ascending broke at %d", position)
		}
	}
	m.results().column = 5
	press(m, 's')
	nulls := 0
	for position := 0; position < 300; position++ {
		if cellAt(position, 5).Null {
			nulls++
		} else if nulls > 0 {
			t.Fatal("a bytea value sorted after NULL in ascending order")
		} else if position > 0 && bytes.Compare(cellAt(position-1, 5).Bytes, cellAt(position, 5).Bytes) > 0 {
			t.Fatalf("bytea ascending broke at %d", position)
		}
	}
	if nulls != 100 {
		t.Fatalf("expected 100 NULL photos, got %d", nulls)
	}

	press(m, 'a', tea.ModCtrl)
	press(m, 'y')
	m.modal.index = 4
	inserts := clipboard(t, press(m, tea.KeyEnter))
	if m.status != "Copied 300 rows as SQL INSERT statements" || !strings.HasPrefix(inserts, "INSERT INTO claims.beneficiary (id, name, birth_date, state, attrs, photo, balance, created_at) VALUES (") {
		t.Fatalf("INSERT copy: %q · %.120s", m.status, inserts)
	}
	columns := "id, name, birth_date, state, attrs, photo, balance, created_at"
	check := "BEGIN; CREATE TEMP TABLE copy_check ON COMMIT DROP AS SELECT " + columns + " FROM claims.beneficiary WITH NO DATA;\n" +
		strings.ReplaceAll(inserts, "INSERT INTO claims.beneficiary ", "INSERT INTO copy_check ") +
		"\nSELECT count(*) FROM ((TABLE copy_check EXCEPT ALL SELECT " + columns + " FROM claims.beneficiary WHERE id <= 300)" +
		" UNION ALL (SELECT " + columns + " FROM claims.beneficiary WHERE id <= 300 EXCEPT ALL TABLE copy_check)) difference; ROLLBACK;"
	batch, err := session.Execute(ctx, check, result.Limits{})
	if err != nil {
		t.Fatalf("copied INSERT statements failed: %v", err)
	}
	var difference string
	for _, set := range batch.Results {
		if len(set.Columns) == 1 && set.Columns[0].Name == "count" {
			difference = set.Rows[0][0].Text
		}
	}
	if difference != "0" {
		t.Fatalf("copied INSERTs differ from the source rows in %s rows", difference)
	}

	press(m, 'y')
	m.modal.index = 3
	var objects []map[string]*string
	if err := json.Unmarshal([]byte(clipboard(t, press(m, tea.KeyEnter))), &objects); err != nil || len(objects) != 300 || objects[299]["photo"] != nil {
		t.Fatalf("JSON copy did not decode as 300 objects ending in NULL photos: %v", err)
	}

	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = 0, 0, 9, 0
	press(m, 'y')
	m.modal.index = 5
	list := clipboard(t, press(m, tea.KeyEnter))
	batch, err = session.Execute(ctx, "SELECT count(*) FROM claims.beneficiary WHERE id IN "+list+";", result.Limits{})
	if err != nil || batch.Results[0].Rows[0][0].Text != "10" {
		t.Fatalf("IN list %s did not select 10 rows: %v", list, err)
	}
}
