package cell

import (
	"encoding/json"
	"testing"

	"github.com/dustinevan/scrunchie/pkg/result"
)

func trickyTable() Table {
	return Table{
		Columns: []result.Column{{Name: "id", Type: "int8", OID: 20}, {Name: "note", Type: "text", OID: 25}, {Name: "raw", Type: "bytea", OID: 17}},
		Rows: [][]result.Cell{
			{{Text: "1"}, {Text: "tab\there"}, {Bytes: []byte{0, 0xff}}},
			{{Text: "2"}, {Text: "line\nbreak \"quoted\""}, {Null: true}},
			{{Text: "3"}, {Text: ""}, {Bytes: []byte{}}},
			{{Text: "4"}, {Text: `\N`}, {Bytes: []byte("a")}},
			{{Null: true}, {Null: true}},
			{{Text: "6"}, {Text: "pipe | back\\slash 雪"}, {Bytes: []byte("|")}},
		},
	}
}

func TestTSVKeepsNullEmptyAndLiteralBackslashNDistinct(t *testing.T) {
	want := "id\tnote\traw\n" +
		"1\t\"tab\there\"\t\\x00ff\n" +
		"2\t\"line\nbreak \"\"quoted\"\"\"\t\\N\n" +
		"3\t\t\\x\n" +
		"4\t\"\\N\"\t\\x61\n" +
		"\\N\t\\N\t\\N\n" +
		"6\tpipe | back\\slash 雪\t\\x7c"
	if got := TSV(trickyTable()); got != want {
		t.Fatalf("TSV:\n%q\nwant\n%q", got, want)
	}
}

func TestMarkdownEscapesTableSyntax(t *testing.T) {
	want := "| id | note | raw |\n" +
		"| ---: | --- | --- |\n" +
		"| 1 | tab\there | \\\\x00ff |\n" +
		"| 2 | line<br>break \"quoted\" | NULL |\n" +
		"| 3 |  | \\\\x |\n" +
		"| 4 | \\\\N | \\\\x61 |\n" +
		"| NULL | NULL | NULL |\n" +
		"| 6 | pipe \\| back\\\\slash 雪 | \\\\x7c |"
	if got := Markdown(trickyTable()); got != want {
		t.Fatalf("Markdown:\n%s\nwant\n%s", got, want)
	}
	crlf := Markdown(Table{Columns: []result.Column{{Name: "a|b", OID: 25}}, Rows: [][]result.Cell{{{Text: "x\r\ny\rz"}}}})
	if crlf != "| a\\|b |\n| --- |\n| x<br>y<br>z |" {
		t.Fatalf("line endings: %q", crlf)
	}
}

func TestJSONObjectsKeepsOrderTextAndUniqueKeys(t *testing.T) {
	table := Table{
		Columns: []result.Column{{Name: "id", OID: 20}, {Name: "id", OID: 25}, {Name: "id_2", OID: 25}, {Name: "doc", OID: 3802}, {Name: "raw", OID: 17}, {Name: "n", OID: 1700}},
		Rows: [][]result.Cell{
			{{Text: "1"}, {Text: "<a & b>"}, {Text: "\x1b[31m\"q\"\\"}, {Text: `{"z": 1, "a": null}`}, {Bytes: []byte{1, 2}}, {Text: "123456789012345678901234567890.123"}},
			{{Null: true}, {Text: ""}, {Null: true}, {Null: true}, {Null: true}, {Text: "NaN"}},
		},
	}
	got := JSONObjects(table)
	want := "[\n" +
		`  {"id": "1", "id_3": "<a & b>", "id_2": "\u001b[31m\"q\"\\", "doc": "{\"z\": 1, \"a\": null}", "raw": "\\x0102", "n": "123456789012345678901234567890.123"},` + "\n" +
		`  {"id": null, "id_3": "", "id_2": null, "doc": null, "raw": null, "n": "NaN"}` + "\n" +
		"]"
	if got != want {
		t.Fatalf("JSON:\n%s\nwant\n%s", got, want)
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil || len(decoded) != 2 || decoded[0]["id_3"] != "<a & b>" || decoded[1]["id"] != nil {
		t.Fatalf("JSON did not round-trip: %v %v", err, decoded)
	}
	if JSONObjects(Table{Columns: table.Columns}) != "[]" {
		t.Fatal("empty selection was not an empty array")
	}
	invalid := JSONObjects(Table{Columns: []result.Column{{Name: "bad\xff", OID: 25}}, Rows: [][]result.Cell{{{Text: "\xfe"}}}})
	if !json.Valid([]byte(invalid)) {
		t.Fatalf("invalid UTF-8 produced invalid JSON: %q", invalid)
	}
}

func TestLiteralWritesPostgreSQLConstants(t *testing.T) {
	for _, tc := range []struct {
		value     result.Cell
		column    result.Column
		backslash bool
		want      string
	}{
		{result.Cell{Null: true}, result.Column{OID: 25}, false, "NULL"},
		{result.Cell{Text: "NULL"}, result.Column{OID: 25}, false, "'NULL'"},
		{result.Cell{Text: ""}, result.Column{OID: 25}, false, "''"},
		{result.Cell{Text: "O'Brien"}, result.Column{OID: 25}, false, "'O''Brien'"},
		{result.Cell{Text: `C:\temp\'x`}, result.Column{OID: 25}, false, `E'C:\\temp\\''x'`},
		{result.Cell{Text: `C:\temp\'x`}, result.Column{OID: 25}, true, `E'C:\\temp\\''x'`},
		{result.Cell{Text: "it's"}, result.Column{OID: 25}, true, "'it''s'"},
		{result.Cell{Text: `{"a\\b"}`}, result.Column{OID: 1009}, false, `E'{"a\\\\b"}'`},
		{result.Cell{Text: "line\nnext\ttab 雪"}, result.Column{OID: 25}, false, "'line\nnext\ttab 雪'"},
		{result.Cell{Text: "-9223372036854775808"}, result.Column{OID: 20}, false, "-9223372036854775808"},
		{result.Cell{Text: "123456789012345678901234567890.000001"}, result.Column{OID: 1700}, false, "123456789012345678901234567890.000001"},
		{result.Cell{Text: "NaN"}, result.Column{OID: 1700}, false, "'NaN'"},
		{result.Cell{Text: "1.5e-07"}, result.Column{OID: 701}, false, "1.5e-07"},
		{result.Cell{Text: "-0"}, result.Column{OID: 701}, false, "'-0'"},
		{result.Cell{Text: "-0"}, result.Column{OID: 700}, false, "'-0'"},
		{result.Cell{Text: "-0.0"}, result.Column{Type: "float8"}, false, "'-0.0'"},
		{result.Cell{Text: "0"}, result.Column{OID: 701}, false, "0"},
		{result.Cell{Text: "-1e-320"}, result.Column{OID: 701}, false, "-1e-320"},
		{result.Cell{Text: "-0"}, result.Column{OID: 1700}, false, "-0"},
		{result.Cell{Text: "-Infinity"}, result.Column{OID: 701}, false, "'-Infinity'"},
		{result.Cell{Text: "Infinity"}, result.Column{OID: 700}, false, "'Infinity'"},
		{result.Cell{Text: "NaN"}, result.Column{OID: 701}, false, "'NaN'"},
		{result.Cell{Text: "t"}, result.Column{OID: 16}, false, "TRUE"},
		{result.Cell{Text: "false"}, result.Column{Type: "bool"}, false, "FALSE"},
		{result.Cell{Bytes: []byte{0xde, 0xad}}, result.Column{OID: 17}, false, `E'\\xdead'::bytea`},
		{result.Cell{Bytes: []byte{}}, result.Column{OID: 17}, true, `E'\\x'::bytea`},
		{result.Cell{Text: `{"a": "it's"}`}, result.Column{OID: 3802}, false, `'{"a": "it''s"}'`},
		{result.Cell{Text: "2024-01-01 00:00:00+00"}, result.Column{OID: 1184}, false, "'2024-01-01 00:00:00+00'"},
		{result.Cell{Text: "$1,000.00"}, result.Column{OID: 790}, false, "'$1,000.00'"},
	} {
		if got := Literal(tc.value, tc.column, tc.backslash); got != tc.want {
			t.Errorf("Literal(%q, %d, %t) = %s, want %s", tc.value.Text, tc.column.OID, tc.backslash, got, tc.want)
		}
	}
}

func TestIdentifierQuotesOnlyWhenNeeded(t *testing.T) {
	for name, want := range map[string]string{
		"id": "id", "_private": "_private", "a1$": "a1$", "Id": `"Id"`, "Mixed Case": `"Mixed Case"`,
		"select": `"select"`, "user": `"user"`, "left": `"left"`, "name": "name", "1st": `"1st"`,
		`say "hi"`: `"say ""hi"""`, "雪": `"雪"`, "?column?": `"?column?"`, "": `""`,
	} {
		if got := Identifier(name); got != want {
			t.Errorf("Identifier(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestInsertsAndInList(t *testing.T) {
	table := Table{
		Columns: []result.Column{{Name: "Id", OID: 23}, {Name: "select", OID: 25}, {Name: "ok", OID: 16}},
		Rows: [][]result.Cell{
			{{Text: "1"}, {Text: "it's"}, {Text: "t"}},
			{{Text: "2"}, {Null: true}},
		},
	}
	want := `INSERT INTO public."Mixed Case" ("Id", "select", ok) VALUES (1, 'it''s', TRUE);` + "\n" +
		`INSERT INTO public."Mixed Case" ("Id", "select", ok) VALUES (2, NULL, NULL);`
	if got := Inserts(table, []string{"public", "Mixed Case"}, false); got != want {
		t.Fatalf("Inserts:\n%s\nwant\n%s", got, want)
	}
	if got := Inserts(Table{Columns: table.Columns[:1], Rows: table.Rows[:1]}, nil, false); got != `INSERT INTO table_name ("Id") VALUES (1);` {
		t.Fatalf("placeholder insert = %s", got)
	}
	if got := InList([]result.Cell{{Text: "a'b"}, {Text: ""}, {Text: "雪"}}, result.Column{OID: 25}, false); got != `('a''b', '', '雪')` {
		t.Fatalf("text IN list = %s", got)
	}
	if got := InList([]result.Cell{{Text: "10"}, {Text: "-2.5"}}, result.Column{OID: 1700}, false); got != "(10, -2.5)" {
		t.Fatalf("numeric IN list = %s", got)
	}
}
