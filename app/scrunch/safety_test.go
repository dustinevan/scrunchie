package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestWriteAndYesFlagsNeedTheirModes(t *testing.T) {
	t.Setenv("PGHOST", "must-not-be-contacted.invalid")
	accepted := []struct {
		args       []string
		write, yes bool
	}{
		{[]string{"--write", "--dsn", "host=selected.invalid"}, true, false},
		{[]string{"--write", "host=selected.invalid"}, true, false},
		{[]string{"--write", "--connect"}, true, false},
		{[]string{"--write", "-c", "select 1"}, true, false},
		{[]string{"--yes", "-c", "delete from t"}, false, true},
		{[]string{"--write", "--yes", "--execute", "-f", "query.sql"}, true, true},
		{[]string{"--dsn", "host=selected.invalid"}, false, false},
	}
	for _, test := range accepted {
		var stdout, stderr bytes.Buffer
		cfg, earlyExit, err := parseConfig(test.args, &stdout, &stderr)
		if err != nil || earlyExit || cfg.write != test.write || cfg.yes != test.yes {
			t.Fatalf("%q: write=%t yes=%t err=%v", test.args, cfg.write, cfg.yes, err)
		}
	}
	for _, args := range [][]string{{"--write"}, {"--yes"}, {"--yes", "--connect"}, {"--write", "--demo"}, {"--yes", "--file", "query.sql"}} {
		var stdout, stderr bytes.Buffer
		if _, _, err := parseConfig(args, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "parsing arguments") {
			t.Fatalf("%q was accepted: %v", args, err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--write", "Allow writes; connections are read-only by default", "--yes", "Confirm UPDATE/DELETE without WHERE and TRUNCATE"} {
		if !strings.Contains(stdout.String(), text) {
			t.Errorf("help is missing %q", text)
		}
	}
}

func TestHeadlessIsReadOnlyAndRefusesUnfilteredChanges(t *testing.T) {
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN must name an isolated test database")
	}
	const marker = "headless-sql-marker"
	cases := []struct {
		name    string
		args    []string
		wantErr string
		output  string
	}{
		{"refuses delete", []string{"--dsn", dsn, "-c", `DELETE FROM public."Mixed Case" -- ` + marker}, `refusing to run SQL without --yes; nothing was run: DELETE without WHERE affects every row in public."Mixed Case".`, ""},
		{"refuses truncate even with write", []string{"--write", "--dsn", dsn, "-c", "TRUNCATE claims.claim_line; SELECT '" + marker + "'"}, "TRUNCATE removes every row from claims.claim_line.", ""},
		{"read-only hint", []string{"--dsn", dsn, "-c", `INSERT INTO public."Mixed Case" VALUES (-1, '` + marker + `')`}, "(SQLSTATE 25006); connections are read-only by default, rerun with --write", ""},
		{"yes still read-only", []string{"--yes", "--dsn", dsn, "-c", `DELETE FROM public."Mixed Case"`}, "rerun with --write", ""},
		{"filtered read", []string{"--dsn", dsn, "-c", "SELECT count(*) AS n FROM claims.beneficiary WHERE id <= 3"}, "", "3"},
		{"write allowed", []string{"--write", "--dsn", dsn, "-c", `BEGIN; INSERT INTO public."Mixed Case" VALUES (-1, 'probe'); ROLLBACK`}, "", "INSERT 0 1"},
		{"confirmed write", []string{"--write", "--yes", "--dsn", dsn, "-c", `BEGIN; DELETE FROM public."Mixed Case"; ROLLBACK`}, "", "DELETE"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(test.args, &stdout, &stderr)
			if test.wantErr == "" && err != nil || test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("got error %v, want %q", err, test.wantErr)
			}
			if err != nil && strings.Contains(err.Error()+stdout.String()+stderr.String(), marker) {
				t.Fatal("refusal echoed SQL text")
			}
			if !strings.Contains(stdout.String(), test.output) {
				t.Fatalf("output %q is missing %q", stdout.String(), test.output)
			}
		})
	}
}
