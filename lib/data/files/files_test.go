package files

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestSaveReplacesSQLWithOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("old query"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "select '日本語';\n"); err != nil {
		t.Fatal(err)
	}
	sql, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "select '日本語';\n" {
		t.Fatalf("SQL changed: %q", sql)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("successful save left %d directory entries, want only destination", len(entries))
	}
}

func TestSaveFailurePreservesDestinationAndRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing-directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	err := Save(path, "select 42;")
	if err == nil || !strings.Contains(err.Error(), "recovery file") {
		t.Fatalf("save error = %v, want recovery-file error", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("original destination changed: %v", statErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".scrunch-save-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "select 42;" {
			t.Fatalf("recovery contents = %q", data)
		}
		return
	}
	t.Fatal("recovery file missing")
}

func TestOpenRejectsInvalidAndOversizedSQL(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"invalid UTF-8", []byte{0xff}},
		{"NUL", []byte("select 1;\x00select 2;")},
		{"size limit", bytes.Repeat([]byte{'a'}, maxSQLBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "query.sql")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("invalid SQL file was accepted")
			}
		})
	}
}

func TestJSONPreservesColumnIdentityNullsAndPrecision(t *testing.T) {
	set := result.Set{
		Columns:  []result.Column{{Name: "value", Type: "numeric", OID: 1700}, {Name: "value", Type: "text", OID: 25}, {Name: "binary", Type: "bytea", OID: 17}},
		Rows:     [][]result.Cell{{{Text: "900719925474099312345.00000000001"}, {Null: true}, {Bytes: []byte{0, 255, 10}}}, {{Text: "0"}, {Text: ""}, {Bytes: []byte{}}}},
		RowCount: 99, Truncated: true, Command: "SELECT 99",
	}
	var output bytes.Buffer
	if err := Write(&output, set, "json"); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Columns   []result.Column     `json:"columns"`
		Rows      [][]json.RawMessage `json:"rows"`
		RowCount  int64               `json:"row_count"`
		Truncated bool                `json:"truncated"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Columns) != 3 || decoded.Columns[0].Name != decoded.Columns[1].Name {
		t.Fatal("duplicate columns were lost")
	}
	if string(decoded.Rows[0][0]) != `"900719925474099312345.00000000001"` || string(decoded.Rows[0][1]) != "null" || string(decoded.Rows[1][1]) != `""` {
		t.Fatalf("numeric/NULL/empty data changed: %s", output.String())
	}
	var binary string
	if err := json.Unmarshal(decoded.Rows[0][2], &binary); err != nil {
		t.Fatal(err)
	}
	if binary != `\x00ff0a` || decoded.RowCount != 99 || !decoded.Truncated {
		t.Fatalf("binary or retention metadata changed: %s", output.String())
	}
}

func TestCSVPreservesNullEmptyAndLiteralNullMarker(t *testing.T) {
	set := result.Set{
		Columns: []result.Column{{Name: "value", OID: 25}},
		Rows:    [][]result.Cell{{{Null: true}}, {{Text: ""}}, {{Text: `\N`}}, {{Text: "a,\"b\"\nc"}}},
	}
	var output bytes.Buffer
	if err := Write(&output, set, "csv"); err != nil {
		t.Fatal(err)
	}
	want := "\"value\"\n\\N\n\"\"\n\"\\N\"\n\"a,\"\"b\"\"\nc\"\n"
	if output.String() != want {
		t.Fatalf("CSV = %q, want %q", output.String(), want)
	}
}

func TestExportRejectsUnknownFormatWithoutCreatingFile(t *testing.T) {
	dir := t.TempDir()
	if err := Export(filepath.Join(dir, "out.unknown"), result.Set{}, "unknown"); err == nil {
		t.Fatal("unknown export format was accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("invalid format created files")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWritePropagatesOutputFailure(t *testing.T) {
	for _, format := range []string{"csv", "json"} {
		err := Write(failingWriter{}, result.Set{Columns: []result.Column{{Name: "x"}}}, format)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("%s error = %v, want closed pipe", format, err)
		}
	}
}
