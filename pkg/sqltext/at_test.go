package sqltext

import (
	"strings"
	"testing"
)

// The cursor marker "|" is removed before splitting; its byte offset is the cursor.
func TestAt_SelectsStatementTouchingCursorOrOwningGapLine(t *testing.T) {
	const procedure = "CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t WHERE id = 1;\nEND;"
	cases := []struct {
		name    string
		buffer  string
		want    string
		escapes bool
	}{
		{"right after semicolon", "select * from patients where id = 1;|\n\ndelete from audit_log;", "select * from patients where id = 1;", false},
		{"blank line after statement", "select 1;\n|\ndelete from audit_log;", "select 1;", false},
		{"typing flow newline", "select 1;\n|", "select 1;", false},
		{"inside first", "sel|ect 1;\nselect 2;", "select 1;", false},
		{"start of second", "select 1;\n|select 2;", "select 2;", false},
		{"inside second", "select 1;\nselect| 2;", "select 2;", false},
		{"end of buffer", "select 1;\nselect 2;|", "select 2;", false},
		{"trailing space at end", "select 1;\nselect 2;\n\n  |", "select 2;", false},
		{"offset zero", "|select 1;\nselect 2;", "select 1;", false},
		{"before first statement", "\n\n|  \nselect 1;\nselect 2;", "select 1;", false},
		{"leading comment before first", "-- header|\nselect 1;\nselect 2;", "-- header\nselect 1;", false},
		{"comment between statements", "select 1;\n-- next one|\nselect 2;", "-- next one\nselect 2;", false},
		{"comment after blank line", "select 1;\n\n-- next| one\nselect 2;", "-- next one\nselect 2;", false},
		{"blank line below introducing comment", "select 1;\n-- about two\n|\nselect 2;", "select 1;", false},
		{"comment introduces third", "select 1;\nselect 2;\n|-- about three\nselect 3;", "-- about three\nselect 3;", false},
		{"before body on its line", "select 1;\n  |  select 2;", "select 2;", false},
		{"block comment line", "select 1;\n/* introduces\n  the next| one */\nselect 2;", "/* introduces\n  the next one */\nselect 2;", false},
		{"block comment from statement line", "select 1; /* trailing\n  continues| */\nselect 2;", "/* trailing\n  continues */\nselect 2;", false},
		{"blank line inside block comment", "select 1;\n/*\n|\n*/\nselect 2;", "select 1;", false},
		{"comment-only statement in gap", "select 1;\n/* only| */;\nselect 2;", "select 2;", false},
		{"comment after comment-only statement", "select 1;\n/* only */;\n-- c|\nselect 2;", "-- c\nselect 2;", false},
		{"trailing comment then blank line", "select 1; -- done\n|", "select 1;", false},
		{"crlf comment line", "select 1;\r\n-- next|\r\nselect 2;", "-- next\r\nselect 2;", false},
		{"crlf blank line", "select 1;\r\n|\r\nselect 2;", "select 1;", false},
		{"crlf trailing comment", "select 1; -- same line|\r\nselect 2;", "select 1;", false},
		{"inside crlf", "select 1;\r|\nselect 2;", "select 1;", false},
		{"carriage return line", "select 1;\r-- next|\rselect 2;", "-- next\rselect 2;", false},
		{"unicode trailing comment", "select '雪'; -- 名前 🙂|\nselect 2;", "select '雪';", false},
		{"unicode space line", "select '雪';\n　|\nselect 2;", "select '雪';", false},
		{"block comment between statements", "select 1; /* gap| */ select 2;", "select 1;", false},
		{"trailing line comment", "select 1; -- done|\nselect 2;", "select 1;", false},
		{"comment only after last", "select 1;\n-- select 2;|", "select 1;", false},
		{"multiline statement", "select 1;\nselect a,\n  b|\nfrom t;", "select a,\n  b\nfrom t;", false},
		{"multiline right after semicolon", "select a,\n  b\nfrom t;|\nselect 2;", "select a,\n  b\nfrom t;", false},
		{"semicolon in string", "select ';|';\nselect 2;", "select ';';", false},
		{"semicolon in dollar quote", "do $x$ begin perform 1;| end $x$;\nselect 2;", "do $x$ begin perform 1; end $x$;", false},
		{"after dollar body", "do $x$ begin perform 1; end $x$;|\nselect 2;", "do $x$ begin perform 1; end $x$;", false},
		{"backslash escapes", `select 'a\';| b';` + "\nselect 2;", `select 'a\'; b';`, true},
		{"no terminating semicolon", "select 1;\nselect 2|", "select 2", false},
		{"adjacent statements", "select 1;|select 2;", "select 1;", false},
		{"unicode before cursor", "select '雪🙂';|\n\ndelete from audit_log;", "select '雪🙂';", false},
		{"unicode in gap", "select '雪';\n-- 名前 🙂|\nselect 2;", "-- 名前 🙂\nselect 2;", false},
		{"routine body statement", "CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t |WHERE id = 1;\nEND;\nSELECT 2;", procedure, false},
		{"routine body end", "CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t WHERE id = 1;\nEN|D;\nSELECT 2;", procedure, false},
		{"after routine body", "CREATE PROCEDURE p() LANGUAGE sql\nBEGIN ATOMIC\n  INSERT INTO t VALUES (9);\n  DELETE FROM t WHERE id = 1;\nEND;|\nSELECT 2;", procedure, false},
		{"rule actions", "CREATE RULE r AS ON INSERT TO t DO ALSO (\n  INSERT INTO t2 VALUES (1);\n  DELETE FROM t2 |WHERE id = 5;\n  NOTIFY t\n);", "CREATE RULE r AS ON INSERT TO t DO ALSO (\n  INSERT INTO t2 VALUES (1);\n  DELETE FROM t2 WHERE id = 5;\n  NOTIFY t\n);", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cursor := strings.Index(c.buffer, "|")
			sql := strings.Replace(c.buffer, "|", "", 1)
			statements := SplitMode(sql, c.escapes)
			index := At(statements, cursor)
			if index < 0 {
				t.Fatalf("no statement selected in %q", c.buffer)
			}
			if got := statements[index].Text; got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAt_CommentsAndBlankTextSelectNothing(t *testing.T) {
	for _, sql := range []string{"", "  \n ", "-- only a comment", "/* block */ ; -- more", " ; ; "} {
		statements := Split(sql)
		if index := At(statements, 0); index != -1 {
			t.Errorf("%q selected statement %d", sql, index)
		}
		if got := Current(sql, len(sql)); got != "" {
			t.Errorf("%q current = %q", sql, got)
		}
	}
}

func TestSplit_BodySkipsLeadingComments(t *testing.T) {
	sql := "  -- one\n/* two /* nested */ */ SELECT 1; /* only */"
	statements := Split(sql)
	if len(statements) != 2 {
		t.Fatalf("got %d statements: %#v", len(statements), statements)
	}
	if got := sql[statements[0].Body:statements[0].End]; got != "SELECT 1;" {
		t.Errorf("first body = %q", got)
	}
	if statements[1].Body != statements[1].End {
		t.Errorf("comment-only body = %d, end %d", statements[1].Body, statements[1].End)
	}
}
