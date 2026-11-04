package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestStartupRequiresExplicitConnectionIntent(t *testing.T) {
	t.Setenv("PGHOST", "environment-host-that-must-not-be-contacted.invalid")
	t.Setenv("PGPASSWORD", "environment-password-sentinel")
	t.Setenv("PGSERVICE", "environment-service-that-must-not-be-loaded")
	cases := []struct {
		name     string
		args     []string
		headless bool
		explicit bool
		dsn      string
	}{
		{name: "bare workspace"},
		{name: "open file", args: []string{"--file", "query.sql"}},
		{name: "disabled startup connect", args: []string{"--connect=false"}},
		{name: "demo", args: []string{"--demo"}},
		{name: "explicit environment", args: []string{"--connect"}, explicit: true},
		{name: "explicit empty DSN", args: []string{"--dsn", ""}, explicit: true},
		{name: "named DSN", args: []string{"--dsn", "host=selected.invalid"}, explicit: true, dsn: "host=selected.invalid"},
		{name: "positional DSN", args: []string{"host=selected.invalid"}, explicit: true, dsn: "host=selected.invalid"},
		{name: "command environment", args: []string{"-c", "select 1"}, headless: true},
		{name: "file execution environment", args: []string{"--execute", "-f", "query.sql"}, headless: true},
		{name: "command selected DSN", args: []string{"--command", "select 1", "host=selected.invalid"}, headless: true, explicit: true, dsn: "host=selected.invalid"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cfg, earlyExit, err := parseConfig(test.args, &stdout, &stderr)
			if err != nil || earlyExit {
				t.Fatalf("unexpected parse result: early exit %t, error %v", earlyExit, err)
			}
			if cfg.headless != test.headless || cfg.explicitConnection != test.explicit || cfg.dsn != test.dsn {
				t.Fatalf("connection intent differs: headless=%t, explicit=%t, selected DSN matches=%t", cfg.headless, cfg.explicitConnection, cfg.dsn == test.dsn)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("parsing startup intent unexpectedly wrote output")
			}
		})
	}
}

func TestStartupRejectsConflictingSourcesWithoutPrintingSecrets(t *testing.T) {
	const secret = "startup-password-sentinel"
	cases := [][]string{
		{"--demo", "--connect"},
		{"--demo", "--dsn", ""},
		{"--demo", "host=selected.invalid password=" + secret},
		{"--dsn", "", "host=selected.invalid password=" + secret},
		{"--dsn", "password=" + secret, "host=selected.invalid"},
		{"--execute", "--command", "select '" + secret + "'"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		_, earlyExit, err := parseConfig(args, &stdout, &stderr)
		if err == nil || earlyExit {
			t.Fatal("conflicting startup sources were not rejected")
		}
		if strings.Contains(err.Error()+stdout.String()+stderr.String(), secret) {
			t.Fatal("startup error disclosed a credential or SQL literal")
		}
	}
}

func TestStartupHelpAndVersionExitBeforeWorkspace(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"--version"}, {"--connect", "--help"}} {
		var stdout, stderr bytes.Buffer
		_, earlyExit, err := parseConfig(args, &stdout, &stderr)
		if err != nil || !earlyExit {
			t.Fatalf("help/version did not exit early: %v", err)
		}
		if !strings.Contains(stdout.String()+stderr.String(), "scrunch") {
			t.Fatal("help/version output is missing")
		}
	}
}
