package postgres

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestDecodeBytea_PreservesAllBytes(t *testing.T) {
	cases := []struct {
		text string
		want []byte
	}{
		{`\x`, []byte{}},
		{"", []byte{}},
		{`\x00ff5c410a`, []byte{0, 255, '\\', 'A', '\n'}},
		{`\000\377\\A\012`, []byte{0, 255, '\\', 'A', '\n'}},
		{"plain text", []byte("plain text")},
	}
	for _, c := range cases {
		got, err := decodeBytea([]byte(c.text))
		if err != nil {
			t.Errorf("%q: %v", c.text, err)
			continue
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%q got %x, want %x", c.text, got, c.want)
		}
	}
}

func TestDecodeBytea_RejectsMalformedEscapes(t *testing.T) {
	for _, text := range []string{`\x0`, `\xz0`, `\`, `\00`, `\400`, `\089`, `\a`} {
		if _, err := decodeBytea([]byte(text)); err == nil {
			t.Errorf("accepted malformed bytea %q", text)
		}
	}
}

func TestOpen_RedactsMalformedConnectionSecrets(t *testing.T) {
	for _, dsn := range []string{
		`postgres://user:secret%zz@localhost/example`,
		`host=localhost password='secret-unclosed`,
		`postgres://user:secret@localhost:badport/example`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := Open(ctx, dsn, Options{})
		cancel()
		if err == nil {
			t.Fatal("accepted malformed connection settings")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), dsn) {
			t.Errorf("connection error contains a credential: %v", err)
		}
	}
}

func TestOpen_RejectsReplicationBeforeConnecting(t *testing.T) {
	for _, mode := range []string{"database", "true", "1"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := Open(ctx, "host=/private/tmp/scrunch-no-server user=fixture dbname=fixture replication="+mode, Options{})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "replication connections are not supported") {
			t.Errorf("mode %s got %v, want replication rejection", mode, err)
		}
	}
}

func TestRelation_QuotesEachIdentifier(t *testing.T) {
	relation := Relation{Schema: `a"; DROP TABLE x; --`, Name: `b".c`}
	want := `"a""; DROP TABLE x; --"."b"".c"`
	if got := relation.QualifiedName(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
