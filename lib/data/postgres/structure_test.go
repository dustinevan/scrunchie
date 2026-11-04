package postgres

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
)

func TestQuoteLiteral_MatchesPostgreSQL(t *testing.T) {
	for text, want := range map[string]string{
		"plain":       `'plain'`,
		"it's":        `'it''s'`,
		`back\slash`:  `E'back\\slash'`,
		`it's \ both`: `E'it''s \\ both'`,
		"line\nbreak": "'line\nbreak'",
	} {
		if got := quoteLiteral(text); got != want {
			t.Errorf("quoteLiteral(%q) = %s, want %s", text, got, want)
		}
	}
}

func relationOID(t *testing.T, session *Session, name string) uint32 {
	t.Helper()
	batch := executeSQL(t, session, "SELECT "+quoteLiteral(name)+"::regclass::oid")
	oid, err := strconv.ParseUint(batch.Results[0].Rows[0][0].Text, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(oid)
}

func structureOf(t *testing.T, session *Session, name string) Structure {
	t.Helper()
	structure, err := session.Structure(context.Background(), relationOID(t, session, name))
	if err != nil {
		t.Fatalf("structure of %s: %v", name, err)
	}
	return structure
}

func TestIntegration_StructureReportsKeysIndexesAndDetails(t *testing.T) {
	session := testSession(t)
	beneficiary := structureOf(t, session, "claims.beneficiary")
	if beneficiary.Relation.Kind != "table" || beneficiary.Qualified != "claims.beneficiary" || beneficiary.Comment != "Synthetic beneficiaries" || beneficiary.Owner == "" {
		t.Fatalf("unexpected relation details: %+v", beneficiary)
	}
	if beneficiary.TotalSize == "" || beneficiary.SizesSkipped || beneficiary.RowEstimate == 0 {
		t.Errorf("size or estimate missing outside a transaction: %q %v", beneficiary.TotalSize, beneficiary.RowEstimate)
	}
	if len(beneficiary.Columns) != 9 || beneficiary.Columns[0].Name != "id" || !beneficiary.Columns[0].NotNull || !strings.HasPrefix(beneficiary.Columns[0].Default, "nextval(") || beneficiary.Columns[4].Type != "character(2)" {
		t.Errorf("unexpected columns: %+v", beneficiary.Columns)
	}
	var kinds []string
	for _, constraint := range beneficiary.Constraints {
		kinds = append(kinds, constraint.Kind+" "+constraint.Name+" "+constraint.Definition)
	}
	if want := []string{"primary key beneficiary_pkey PRIMARY KEY (id)", "unique beneficiary_mbi_key UNIQUE (mbi)", "check beneficiary_state_check CHECK ((state ~ '^[A-Z]{2}$'::text))"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("constraints got %q, want %q", kinds, want)
	}
	if len(beneficiary.ReferencedBy) != 1 {
		t.Fatalf("referenced by got %+v", beneficiary.ReferencedBy)
	}
	incoming := beneficiary.ReferencedBy[0]
	if incoming.Table != "claims.claim_line" || incoming.Columns != "beneficiary_id" || incoming.References != "id" || incoming.OnDelete != "CASCADE" || incoming.OnUpdate != "NO ACTION" {
		t.Errorf("unexpected incoming key: %+v", incoming)
	}

	line := structureOf(t, session, "claims.claim_line")
	if len(line.Constraints) != 2 || line.Constraints[1].Kind != "foreign key" || line.Constraints[1].Table != "claims.beneficiary" || line.Constraints[1].OnDelete != "CASCADE" || !line.Constraints[1].Validated {
		t.Fatalf("unexpected outgoing key: %+v", line.Constraints)
	}
	if len(line.Indexes) != 2 || !line.Indexes[0].Primary || !line.Indexes[0].Constraint || line.Indexes[1].Constraint || line.Indexes[1].Size == "" ||
		line.Indexes[1].Definition != "CREATE INDEX claim_line_beneficiary_id_idx ON claims.claim_line USING btree (beneficiary_id)" {
		t.Errorf("unexpected indexes: %+v", line.Indexes)
	}

	view := structureOf(t, session, "claims.md_beneficiaries")
	if view.Relation.Kind != "view" || !strings.Contains(view.Definition, "'MD'") || len(view.Columns) != 9 || len(view.Indexes) != 0 {
		t.Errorf("unexpected view structure: %+v", view)
	}
	mixed := structureOf(t, session, `public."Mixed Case"`)
	if mixed.Qualified != `public."Mixed Case"` || mixed.Columns[0].Quoted != `"Id"` || mixed.Columns[1].Quoted != `"select"` {
		t.Errorf("identifiers were not quoted like quote_ident: %+v", mixed)
	}

	routines, err := session.Routines(context.Background(), "claims")
	if err != nil || len(routines) != 1 || routines[0].Name != "paid_total" || routines[0].Arguments != "b bigint" || routines[0].Kind != "function" || routines[0].Result != "numeric" {
		t.Fatalf("routines got %+v, %v", routines, err)
	}
	ddl, err := session.RoutineDDL(context.Background(), routines[0].OID)
	if err != nil || !strings.HasPrefix(ddl, "CREATE OR REPLACE FUNCTION claims.paid_total(b bigint)") || !strings.HasSuffix(ddl, "$function$;\n") {
		t.Errorf("routine DDL got %q, %v", ddl, err)
	}
}

func TestIntegration_StructureInsideTransactionSkipsLocksAndKeepsItHealthy(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "BEGIN")
	structure := structureOf(t, session, "claims.beneficiary")
	if !structure.SizesSkipped || structure.TotalSize != "" || structure.Indexes[0].Size != "" {
		t.Errorf("sizes were read inside a transaction: %+v", structure)
	}
	if _, err := session.Structure(context.Background(), 4294967295); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("missing relation got %v", err)
	}
	sum := executeSQL(t, session, "SELECT oid FROM pg_catalog.pg_proc WHERE proname = 'sum' AND proargtypes = '20'::oidvector")
	oid, err := strconv.ParseUint(sum.Results[0].Rows[0][0].Text, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RoutineDDL(context.Background(), uint32(oid)); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Errorf("aggregate DDL got %v", err)
	}
	if batch := executeSQL(t, session, "SELECT 1"); batch.TxStatus != 'T' {
		t.Fatalf("catalog reads changed the transaction to %q", batch.TxStatus)
	}
	executeSQL(t, session, "ROLLBACK")
}

// Generated DDL is executed into a scratch schema. The copy's catalog must
// match the original's, apart from the schema name.
func TestIntegration_DDLRecreatesClaimLineAndMixedCaseInScratchSchema(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "DROP SCHEMA IF EXISTS scrunch_ddl_scratch CASCADE; CREATE SCHEMA scrunch_ddl_scratch")
	t.Cleanup(func() {
		if _, err := session.Execute(context.Background(), "DROP SCHEMA IF EXISTS scrunch_ddl_scratch CASCADE", result.Limits{}); err != nil {
			t.Error(err)
		}
	})
	for _, test := range []struct{ original, copy string }{
		{"claims.claim_line", "scrunch_ddl_scratch.claim_line"},
		{`public."Mixed Case"`, `scrunch_ddl_scratch."Mixed Case"`},
	} {
		original := structureOf(t, session, test.original)
		ddl := DDL(original)
		if !strings.HasPrefix(ddl, "CREATE TABLE "+test.original+" (\n") {
			t.Fatalf("unexpected DDL:\n%s", ddl)
		}
		executeSQL(t, session, strings.ReplaceAll(ddl, test.original, test.copy))
		copied := structureOf(t, session, test.copy)
		if !reflect.DeepEqual(copied.Columns, original.Columns) {
			t.Errorf("%s columns differ:\n%+v\n%+v", test.original, copied.Columns, original.Columns)
		}
		if !reflect.DeepEqual(copied.Constraints, original.Constraints) {
			t.Errorf("%s constraints differ:\n%+v\n%+v", test.original, copied.Constraints, original.Constraints)
		}
		if len(copied.Indexes) != len(original.Indexes) {
			t.Fatalf("%s indexes differ: %+v", test.original, copied.Indexes)
		}
		for i := range original.Indexes {
			want := original.Indexes[i]
			want.Definition = strings.ReplaceAll(want.Definition, test.original, test.copy)
			got := copied.Indexes[i]
			got.Size, want.Size = "", ""
			if got != want {
				t.Errorf("%s index differs:\n%+v\n%+v", test.original, got, want)
			}
		}
	}
}

const ddlFixtureSQL = `
DROP SCHEMA IF EXISTS scrunch_ddl CASCADE;
DROP SERVER IF EXISTS scrunch_nowhere CASCADE;
DROP FOREIGN DATA WRAPPER IF EXISTS scrunch_dummy CASCADE;
CREATE FOREIGN DATA WRAPPER scrunch_dummy;
CREATE SERVER scrunch_nowhere FOREIGN DATA WRAPPER scrunch_dummy OPTIONS (host 'must-not-connect.invalid');
CREATE SCHEMA scrunch_ddl;
CREATE FUNCTION scrunch_ddl.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.updated_at := now(); RETURN NEW; END $$;
COMMENT ON FUNCTION scrunch_ddl.touch() IS 'Sets updated_at';
CREATE PROCEDURE scrunch_ddl."Record"(IN n integer) LANGUAGE sql AS $$ SELECT n $$;
CREATE SEQUENCE scrunch_ddl.ticket_seq AS integer START WITH 100 INCREMENT BY 5 MINVALUE 10 MAXVALUE 100000 CACHE 3 CYCLE;
CREATE TABLE scrunch_ddl.parent (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code text COLLATE "C" NOT NULL UNIQUE,
  "Select" integer DEFAULT 7,
  doubled integer GENERATED ALWAYS AS ("Select" * 2) STORED,
  ticket integer DEFAULT nextval('scrunch_ddl.ticket_seq'),
  updated_at timestamptz
) WITH (fillfactor = 70);
ALTER TABLE scrunch_ddl.parent ADD CONSTRAINT positive CHECK ("Select" > 0) NOT VALID;
ALTER SEQUENCE scrunch_ddl.ticket_seq OWNED BY scrunch_ddl.parent.ticket;
CREATE INDEX parent_partial ON scrunch_ddl.parent (lower(code)) WHERE "Select" > 1;
CREATE TRIGGER parent_touch BEFORE UPDATE ON scrunch_ddl.parent FOR EACH ROW EXECUTE FUNCTION scrunch_ddl.touch();
ALTER TABLE scrunch_ddl.parent DISABLE TRIGGER parent_touch;
ALTER TABLE scrunch_ddl.parent ENABLE ROW LEVEL SECURITY;
COMMENT ON TABLE scrunch_ddl.parent IS 'It''s a \ test';
COMMENT ON COLUMN scrunch_ddl.parent.code IS 'Code';
CREATE TABLE scrunch_ddl.child (
  parent_id bigint REFERENCES scrunch_ddl.parent ON UPDATE CASCADE ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,
  note text
);
CREATE TABLE scrunch_ddl.events (at date NOT NULL, kind text) PARTITION BY RANGE (at);
CREATE INDEX events_kind ON scrunch_ddl.events (kind);
CREATE TABLE scrunch_ddl.events_2026 PARTITION OF scrunch_ddl.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE scrunch_ddl.base (a integer);
CREATE UNLOGGED TABLE scrunch_ddl.derived (b integer) INHERITS (scrunch_ddl.base);
CREATE VIEW scrunch_ddl.codes WITH (security_barrier = true) AS SELECT id, code FROM scrunch_ddl.parent WHERE "Select" > 0;
COMMENT ON COLUMN scrunch_ddl.codes.code IS 'View column';
CREATE MATERIALIZED VIEW scrunch_ddl.code_counts AS SELECT code, count(*) AS n FROM scrunch_ddl.parent GROUP BY code WITH NO DATA;
CREATE UNIQUE INDEX code_counts_code ON scrunch_ddl.code_counts (code);
CREATE TABLE scrunch_ddl.counters (
  up integer GENERATED BY DEFAULT AS IDENTITY (START WITH 1000 INCREMENT BY 10),
  down smallint GENERATED ALWAYS AS IDENTITY (INCREMENT BY -1),
  low bigint GENERATED ALWAYS AS IDENTITY (MINVALUE 5 CACHE 20 CYCLE)
);
CREATE FOREIGN TABLE scrunch_ddl.remote (id integer NOT NULL, label text OPTIONS (column_name 'lbl') CHECK (label <> ''))
  SERVER scrunch_nowhere OPTIONS (table_name 'x''y');
COMMENT ON FOREIGN TABLE scrunch_ddl.remote IS 'Never queried';`

// Every object in a fixture schema is dropped and recreated only from the
// generated DDL; reading the catalog again must produce the same structures.
func TestIntegration_DDLRoundTripPreservesStructure(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, ddlFixtureSQL)
	t.Cleanup(func() {
		if _, err := session.Execute(context.Background(), "DROP SCHEMA IF EXISTS scrunch_ddl CASCADE; DROP SERVER IF EXISTS scrunch_nowhere CASCADE; DROP FOREIGN DATA WRAPPER IF EXISTS scrunch_dummy CASCADE", result.Limits{}); err != nil {
			t.Error(err)
		}
	})
	relations := []string{"scrunch_ddl.ticket_seq", "scrunch_ddl.parent", "scrunch_ddl.child", "scrunch_ddl.events", "scrunch_ddl.events_2026", "scrunch_ddl.base", "scrunch_ddl.derived", "scrunch_ddl.codes", "scrunch_ddl.code_counts", "scrunch_ddl.counters", "scrunch_ddl.remote"}
	before := make(map[string]Structure)
	ddl := make(map[string]string)
	for _, name := range relations {
		structure := structureOf(t, session, name)
		structure.Relation.OID, structure.TotalSize, structure.RowEstimate = 0, "", 0
		for i := range structure.Indexes {
			structure.Indexes[i].Size = ""
		}
		before[name], ddl[name] = structure, DDL(structure)
	}
	routines, err := session.Routines(context.Background(), "scrunch_ddl")
	if err != nil || len(routines) != 2 || routines[0].Name != "Record" || routines[0].Kind != "procedure" || routines[1].Name != "touch" {
		t.Fatalf("routines got %+v, %v", routines, err)
	}
	var routineDDL []string
	for _, routine := range routines {
		text, err := session.RoutineDDL(context.Background(), routine.OID)
		if err != nil {
			t.Fatal(err)
		}
		routineDDL = append(routineDDL, text)
	}
	if !strings.Contains(routineDDL[0], `PROCEDURE scrunch_ddl."Record"(IN n integer)`) || !strings.Contains(routineDDL[1], "COMMENT ON FUNCTION scrunch_ddl.touch() IS 'Sets updated_at';") {
		t.Fatalf("unexpected routine DDL:\n%s", strings.Join(routineDDL, "\n"))
	}
	for _, want := range []string{
		"code text COLLATE pg_catalog.\"C\" NOT NULL",
		"id bigint GENERATED ALWAYS AS IDENTITY NOT NULL",
		`doubled integer GENERATED ALWAYS AS (("Select" * 2)) STORED`,
		"ALTER TABLE scrunch_ddl.parent ADD CONSTRAINT positive CHECK ((\"Select\" > 0)) NOT VALID;",
		"ALTER TABLE scrunch_ddl.parent DISABLE TRIGGER parent_touch;",
		`COMMENT ON TABLE scrunch_ddl.parent IS E'It''s a \\ test';`,
		"\nWITH (fillfactor='70');",
	} {
		if !strings.Contains(ddl["scrunch_ddl.parent"], want) {
			t.Errorf("parent DDL lacks %q:\n%s", want, ddl["scrunch_ddl.parent"])
		}
	}
	for name, want := range map[string]string{
		"scrunch_ddl.counters":    "    up integer GENERATED BY DEFAULT AS IDENTITY (INCREMENT BY 10 START WITH 1000) NOT NULL,\n    down smallint GENERATED ALWAYS AS IDENTITY (INCREMENT BY -1) NOT NULL,\n    low bigint GENERATED ALWAYS AS IDENTITY (MINVALUE 5 CACHE 20 CYCLE) NOT NULL\n",
		"scrunch_ddl.remote":      "CREATE FOREIGN TABLE scrunch_ddl.remote (\n    id integer NOT NULL,\n    label text OPTIONS (column_name 'lbl'),\n    CONSTRAINT remote_label_check CHECK ((label <> ''::text))\n)\nSERVER scrunch_nowhere\nOPTIONS (table_name 'x''y');\n\nCOMMENT ON FOREIGN TABLE scrunch_ddl.remote IS 'Never queried';\n",
		"scrunch_ddl.events_2026": "CREATE TABLE scrunch_ddl.events_2026 PARTITION OF scrunch_ddl.events\nFOR VALUES FROM ('2026-01-01') TO ('2027-01-01');\n",
		"scrunch_ddl.derived":     "CREATE UNLOGGED TABLE scrunch_ddl.derived (\n    b integer\n)\nINHERITS (scrunch_ddl.base);\n",
		"scrunch_ddl.code_counts": "\nWITH NO DATA;\n\nCREATE UNIQUE INDEX code_counts_code ON scrunch_ddl.code_counts USING btree (code);\n",
	} {
		if !strings.Contains(ddl[name], want) {
			t.Errorf("%s DDL lacks %q:\n%s", name, want, ddl[name])
		}
	}
	sequence := sqltext.SplitMode(ddl["scrunch_ddl.ticket_seq"], false)
	if len(sequence) != 2 || !strings.HasPrefix(sequence[1].Text, "ALTER SEQUENCE scrunch_ddl.ticket_seq OWNED BY scrunch_ddl.parent.ticket") {
		t.Fatalf("unexpected sequence DDL:\n%s", ddl["scrunch_ddl.ticket_seq"])
	}

	// A sequence default and OWNED BY refer to each other, as in pg_dump.
	executeSQL(t, session, "DROP SCHEMA scrunch_ddl CASCADE; CREATE SCHEMA scrunch_ddl")
	executeSQL(t, session, routineDDL[0]+routineDDL[1]+sequence[0].Text)
	executeSQL(t, session, ddl["scrunch_ddl.parent"]+sequence[1].Text)
	for _, name := range relations[2:] {
		executeSQL(t, session, ddl[name])
	}
	for _, name := range relations {
		after := structureOf(t, session, name)
		after.Relation.OID, after.TotalSize, after.RowEstimate = 0, "", 0
		for i := range after.Indexes {
			after.Indexes[i].Size = ""
		}
		if !reflect.DeepEqual(after, before[name]) {
			t.Errorf("%s changed after its DDL round trip:\nbefore %+v\nafter  %+v\nDDL:\n%s", name, before[name], after, ddl[name])
		}
	}
	for i, routine := range routines {
		routines, err := session.Routines(context.Background(), "scrunch_ddl")
		if err != nil {
			t.Fatal(err)
		}
		text, err := session.RoutineDDL(context.Background(), routines[i].OID)
		if err != nil || text != routineDDL[i] {
			t.Errorf("%s routine DDL changed: %q, %v", routine.Name, text, err)
		}
	}
}
