package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestHelpAndVersionNeverNeedAConnection(t *testing.T) {
	t.Setenv("PGHOST", "must-not-be-contacted.invalid")
	for _, args := range [][]string{{"--help"}, {"help"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "scrunch") || stderr.Len() != 0 {
			t.Fatalf("unexpected output: %q / %q", stdout.String(), stderr.String())
		}
	}
}

func TestInvalidFlagsAreRejectedBeforeConnecting(t *testing.T) {
	cases := [][]string{
		{"--command", ""}, {"-c", " \n "}, {"--execute"},
		{"--command", "select 1", "--file", "secret.sql"},
		{"--max-rows", "0"}, {"--max-bytes", "1"},
		{"--timeout", "-1s"}, {"--connect-timeout", "0"},
		{"--format", "xml"}, {"--demo", "--command", "select 1"},
		{"--dsn", "password=sentinel-secret", "another-connection"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		err := run(args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "parsing arguments") {
			t.Fatalf("args %q returned %v", args, err)
		}
		if strings.Contains(err.Error()+stderr.String()+stdout.String(), "sentinel-secret") {
			t.Fatal("credential was printed")
		}
	}
}

func TestJSONOutputContainsAllStatementResults(t *testing.T) {
	batch := result.Batch{Results: []result.Set{
		{Command: "BEGIN"},
		{Columns: []result.Column{{Name: "x", OID: 23}}, Rows: [][]result.Cell{{{Text: "42"}}}, Command: "SELECT 1", RowCount: 1},
	}}
	var output bytes.Buffer
	if err := printBatch(&output, batch, "json"); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(document.Results))
	}
}

func TestTerminalOutputEscapesDatabaseControlSequences(t *testing.T) {
	batch := result.Batch{Results: []result.Set{{
		Columns: []result.Column{{Name: "name\x1b]52;c;malicious\x07", OID: 25}},
		Rows:    [][]result.Cell{{{Text: "\x1b[2J\rhidden"}}}, Command: "SELECT 1",
	}}}
	var output bytes.Buffer
	if err := printBatch(&output, batch, "table"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(output.String(), "\x1b\x07\r") {
		t.Fatalf("terminal controls escaped into output: %q", output.String())
	}
	if !strings.Contains(output.String(), `\x1b`) {
		t.Fatal("escaped data was not visible")
	}
}
