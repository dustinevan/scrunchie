package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sizes take a lock on each relation; a lock wait canceled by the catalog
// timeout would abort the user's open transaction, so $2 skips them there.
const structureRelationSQL = `
SELECT c.relkind::text, n.nspname, c.relname,
       pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(c.relname),
       pg_catalog.pg_get_userbyid(c.relowner),
       COALESCE(pg_catalog.obj_description(c.oid, 'pg_class'), ''),
       c.reltuples::text, c.relpersistence::text,
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(o.option_name) || '=' || pg_catalog.quote_literal(o.option_value), ', ')
                 FROM pg_catalog.pg_options_to_table(c.reloptions) AS o), ''),
       CASE WHEN c.relkind = 'p' THEN COALESCE(pg_catalog.pg_get_partkeydef(c.oid), '') ELSE '' END,
       COALESCE((SELECT pg_catalog.quote_ident(pn.nspname) || '.' || pg_catalog.quote_ident(pc.relname)
                 FROM pg_catalog.pg_inherits AS i
                 JOIN pg_catalog.pg_class AS pc ON pc.oid = i.inhparent
                 JOIN pg_catalog.pg_namespace AS pn ON pn.oid = pc.relnamespace
                 WHERE i.inhrelid = c.oid AND c.relispartition), ''),
       CASE WHEN c.relispartition THEN COALESCE(pg_catalog.pg_get_expr(c.relpartbound, c.oid), '') ELSE '' END,
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(pn.nspname) || '.' || pg_catalog.quote_ident(pc.relname), ', ' ORDER BY i.inhseqno)
                 FROM pg_catalog.pg_inherits AS i
                 JOIN pg_catalog.pg_class AS pc ON pc.oid = i.inhparent
                 JOIN pg_catalog.pg_namespace AS pn ON pn.oid = pc.relnamespace
                 WHERE i.inhrelid = c.oid AND NOT c.relispartition), ''),
       CASE WHEN c.relkind IN ('v', 'm') THEN COALESCE(pg_catalog.pg_get_viewdef(c.oid, true), '') ELSE '' END,
       c.relispopulated::text, c.relrowsecurity::text, c.relforcerowsecurity::text,
       COALESCE((SELECT pg_catalog.quote_ident(s.srvname)
                 FROM pg_catalog.pg_foreign_table AS f
                 JOIN pg_catalog.pg_foreign_server AS s ON s.oid = f.ftserver
                 WHERE f.ftrelid = c.oid), ''),
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(o.option_name) || ' ' || pg_catalog.quote_literal(o.option_value), ', ')
                 FROM pg_catalog.pg_foreign_table AS f, pg_catalog.pg_options_to_table(f.ftoptions) AS o
                 WHERE f.ftrelid = c.oid), ''),
       CASE WHEN NOT $2::bool THEN ''
            WHEN c.relkind = 'p' THEN COALESCE((SELECT pg_catalog.pg_size_pretty(pg_catalog.sum(pg_catalog.pg_total_relation_size(t.relid)))
                                                FROM pg_catalog.pg_partition_tree(c.oid) AS t), '')
            WHEN c.relkind IN ('r', 'm', 'S') THEN COALESCE(pg_catalog.pg_size_pretty(pg_catalog.pg_total_relation_size(c.oid)), '')
            ELSE '' END
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE c.oid = $1::oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')`

const structureColumnsSQL = `
SELECT a.attname, pg_catalog.quote_ident(a.attname),
       pg_catalog.format_type(a.atttypid, a.atttypmod),
       a.attnotnull::text,
       COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), ''),
       a.attidentity::text, a.attgenerated::text,
       COALESCE(CASE WHEN a.attcollation <> t.typcollation
                     THEN pg_catalog.quote_ident(cn.nspname) || '.' || pg_catalog.quote_ident(co.collname) END, ''),
       COALESCE(pg_catalog.col_description(a.attrelid, a.attnum), ''),
       a.attislocal::text,
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(o.option_name) || ' ' || pg_catalog.quote_literal(o.option_value), ', ')
                 FROM pg_catalog.pg_options_to_table(a.attfdwoptions) AS o), ''),
       COALESCE(i.seqstart::text, ''), COALESCE(i.seqincrement::text, ''), COALESCE(i.seqmin::text, ''),
       COALESCE(i.seqmax::text, ''), COALESCE(i.seqcache::text, ''), COALESCE(i.seqcycle::text, '')
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
LEFT JOIN pg_catalog.pg_attrdef AS d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_catalog.pg_collation AS co ON co.oid = a.attcollation
LEFT JOIN pg_catalog.pg_namespace AS cn ON cn.oid = co.collnamespace
LEFT JOIN LATERAL (
  SELECT s.seqstart, s.seqincrement, s.seqmin, s.seqmax, s.seqcache, s.seqcycle
  FROM pg_catalog.pg_depend AS dep
  JOIN pg_catalog.pg_sequence AS s ON s.seqrelid = dep.objid
  WHERE dep.classid = 'pg_catalog.pg_class'::pg_catalog.regclass AND dep.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
    AND dep.refobjid = a.attrelid AND dep.refobjsubid = a.attnum AND dep.deptype = 'i'
  LIMIT 1
) AS i ON a.attidentity <> ''
WHERE a.attrelid = $1::oid AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

// Outgoing constraints and incoming foreign keys share one column layout.
// The table column is the other side of a foreign key.
const structureConstraintsSQL = `
SELECT c.conname, pg_catalog.quote_ident(c.conname), c.contype::text,
       pg_catalog.pg_get_constraintdef(c.oid), c.conislocal::text,
       COALESCE(pg_catalog.quote_ident(fn.nspname) || '.' || pg_catalog.quote_ident(fc.relname), ''),
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(a.attname), ', ' ORDER BY k.n)
                 FROM pg_catalog.unnest(c.conkey) WITH ORDINALITY AS k(attnum, n)
                 JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.conrelid AND a.attnum = k.attnum), ''),
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(a.attname), ', ' ORDER BY k.n)
                 FROM pg_catalog.unnest(c.confkey) WITH ORDINALITY AS k(attnum, n)
                 JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.confrelid AND a.attnum = k.attnum), ''),
       c.confupdtype::text, c.confdeltype::text, c.confmatchtype::text,
       c.condeferrable::text, c.condeferred::text, c.convalidated::text
FROM pg_catalog.pg_constraint AS c
LEFT JOIN pg_catalog.pg_class AS fc ON fc.oid = c.confrelid
LEFT JOIN pg_catalog.pg_namespace AS fn ON fn.oid = fc.relnamespace
WHERE c.conrelid = $1::oid AND c.contype IN ('p', 'u', 'x', 'c', 'f')
ORDER BY CASE c.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'x' THEN 2 WHEN 'c' THEN 3 ELSE 4 END, c.conname`

const structureReferencedBySQL = `
SELECT c.conname, pg_catalog.quote_ident(c.conname), c.contype::text,
       pg_catalog.pg_get_constraintdef(c.oid), c.conislocal::text,
       pg_catalog.quote_ident(rn.nspname) || '.' || pg_catalog.quote_ident(rc.relname),
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(a.attname), ', ' ORDER BY k.n)
                 FROM pg_catalog.unnest(c.conkey) WITH ORDINALITY AS k(attnum, n)
                 JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.conrelid AND a.attnum = k.attnum), ''),
       COALESCE((SELECT pg_catalog.string_agg(pg_catalog.quote_ident(a.attname), ', ' ORDER BY k.n)
                 FROM pg_catalog.unnest(c.confkey) WITH ORDINALITY AS k(attnum, n)
                 JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.confrelid AND a.attnum = k.attnum), ''),
       c.confupdtype::text, c.confdeltype::text, c.confmatchtype::text,
       c.condeferrable::text, c.condeferred::text, c.convalidated::text
FROM pg_catalog.pg_constraint AS c
JOIN pg_catalog.pg_class AS rc ON rc.oid = c.conrelid
JOIN pg_catalog.pg_namespace AS rn ON rn.oid = rc.relnamespace
WHERE c.confrelid = $1::oid AND c.contype = 'f' AND c.conparentid = 0
ORDER BY 6, 1`

const structureIndexesSQL = `
SELECT ic.relname, pg_catalog.quote_ident(ic.relname), pg_catalog.pg_get_indexdef(i.indexrelid),
       i.indisprimary::text, i.indisunique::text, i.indisvalid::text,
       COALESCE(pg_catalog.pg_get_expr(i.indpred, i.indrelid), ''),
       CASE WHEN $2::bool THEN COALESCE(pg_catalog.pg_size_pretty(pg_catalog.pg_relation_size(i.indexrelid)), '') ELSE '' END,
       EXISTS (SELECT 1 FROM pg_catalog.pg_constraint AS c
               WHERE c.conindid = i.indexrelid AND c.conrelid = i.indrelid AND c.contype IN ('p', 'u', 'x'))::text,
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits AS h WHERE h.inhrelid = i.indexrelid)::text
FROM pg_catalog.pg_index AS i
JOIN pg_catalog.pg_class AS ic ON ic.oid = i.indexrelid
WHERE i.indrelid = $1::oid
ORDER BY i.indisprimary DESC, ic.relname`

// %s is a constant: PostgreSQL 13 added tgparentid for partition clones.
const structureTriggersSQL = `
SELECT t.tgname, pg_catalog.quote_ident(t.tgname), pg_catalog.pg_get_triggerdef(t.oid),
       t.tgenabled::text, %s
FROM pg_catalog.pg_trigger AS t
WHERE t.tgrelid = $1::oid AND NOT t.tgisinternal
ORDER BY t.tgname`

const structureSequenceSQL = `
SELECT pg_catalog.format_type(s.seqtypid, NULL), s.seqstart::text, s.seqincrement::text,
       s.seqmin::text, s.seqmax::text, s.seqcache::text, s.seqcycle::text,
       COALESCE(o.owner, ''), COALESCE(o.deptype, '')
FROM pg_catalog.pg_sequence AS s
LEFT JOIN LATERAL (
  SELECT pg_catalog.quote_ident(tn.nspname) || '.' || pg_catalog.quote_ident(tc.relname) || '.' || pg_catalog.quote_ident(a.attname) AS owner,
         d.deptype::text AS deptype
  FROM pg_catalog.pg_depend AS d
  JOIN pg_catalog.pg_class AS tc ON tc.oid = d.refobjid
  JOIN pg_catalog.pg_namespace AS tn ON tn.oid = tc.relnamespace
  JOIN pg_catalog.pg_attribute AS a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
  WHERE d.classid = 'pg_catalog.pg_class'::pg_catalog.regclass AND d.objid = s.seqrelid
    AND d.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass AND d.deptype IN ('a', 'i')
  LIMIT 1
) AS o ON true
WHERE s.seqrelid = $1::oid`

const routinesSQL = `
SELECT p.oid::text, n.nspname, p.proname,
       pg_catalog.pg_get_function_identity_arguments(p.oid),
       CASE p.prokind WHEN 'p' THEN 'procedure' WHEN 'a' THEN 'aggregate' WHEN 'w' THEN 'window' ELSE 'function' END,
       COALESCE(pg_catalog.pg_get_function_result(p.oid), '')
FROM pg_catalog.pg_proc AS p
JOIN pg_catalog.pg_namespace AS n ON n.oid = p.pronamespace
WHERE n.nspname = $1
ORDER BY p.proname, 4
LIMIT 10001`

// pg_get_functiondef raises an error for aggregates; CASE never calls it there.
const routineDDLSQL = `
SELECT p.prokind::text,
       CASE WHEN p.prokind <> 'a' THEN pg_catalog.pg_get_functiondef(p.oid) ELSE '' END,
       pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(p.proname)
         || '(' || pg_catalog.pg_get_function_identity_arguments(p.oid) || ')',
       COALESCE(pg_catalog.obj_description(p.oid, 'pg_proc'), '')
FROM pg_catalog.pg_proc AS p
JOIN pg_catalog.pg_namespace AS n ON n.oid = p.pronamespace
WHERE p.oid = $1::oid`

// Catalog codes as the structure view and DDL name them.
var (
	relationKinds     = map[string]string{"r": "table", "p": "partitioned table", "v": "view", "m": "materialized view", "f": "foreign table", "S": "sequence"}
	persistenceNames  = map[string]string{"p": "permanent", "u": "unlogged", "t": "temporary"}
	constraintKinds   = map[string]string{"p": "primary key", "u": "unique", "x": "exclusion", "c": "check", "f": "foreign key"}
	foreignKeyActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}
	triggerStates     = map[string]string{"O": "enabled", "D": "disabled", "R": "replica", "A": "always"}
	integerBounds     = map[string][2]string{
		"smallint": {"-32768", "32767"},
		"integer":  {"-2147483648", "2147483647"},
		"bigint":   {"-9223372036854775808", "9223372036854775807"},
	}
)

// Routine is a function, procedure, aggregate, or window function.
type Routine struct {
	OID       uint32
	Schema    string
	Name      string
	Arguments string // identity arguments, which distinguish overloads
	Kind      string // "function", "procedure", "aggregate", or "window"
	Result    string
}

// QualifiedName returns the routine's schema and name, each quoted
// independently, without its argument list.
func (r Routine) QualifiedName() string {
	return `"` + strings.ReplaceAll(r.Schema, `"`, `""`) + `"."` + strings.ReplaceAll(r.Name, `"`, `""`) + `"`
}

// Structure is a read-only snapshot of one relation's catalog definition.
// Quoted fields follow quote_ident; definitions come from PostgreSQL's own
// deparsers, which qualify referenced objects relative to the search_path.
type Structure struct {
	Relation  Relation
	Qualified string
	Owner     string
	Comment   string
	// RowEstimate is pg_class.reltuples; negative means never analyzed.
	RowEstimate    float64
	Persistence    string // "permanent", "unlogged", or "temporary"
	Options        string // reloptions as name='value' pairs
	PartitionKey   string
	PartitionOf    string
	PartitionBound string
	Inherits       string
	Definition     string // views and materialized views
	Populated      bool
	RowSecurity    bool
	ForceSecurity  bool
	Server         string
	ServerOptions  string
	// TotalSize is empty when sizes were skipped inside a transaction.
	TotalSize    string
	SizesSkipped bool
	Columns      []ColumnDefinition
	Constraints  []Constraint
	ReferencedBy []Constraint
	Indexes      []Index
	Triggers     []Trigger
	Sequence     Sequence
}

// ColumnDefinition is one attribute as CREATE TABLE declares it.
type ColumnDefinition struct {
	Name     string
	Quoted   string
	Type     string
	NotNull  bool
	Default  string
	Identity string // "ALWAYS", "BY DEFAULT", or ""
	// IdentityOptions lists sequence options that differ from the defaults.
	IdentityOptions string
	Generated       string // generation expression
	Virtual         bool   // PostgreSQL 18 virtual generated column
	Collation       string
	Options         string // foreign-table column options
	Comment         string
	Inherited       bool // declared only by a parent table
}

// Constraint is a table constraint. For foreign keys, Table is the other
// table: the referenced one for outgoing keys, the referencing one for
// incoming keys. Columns belong to the referencing table.
type Constraint struct {
	Name       string
	Quoted     string
	Kind       string // "primary key", "unique", "exclusion", "check", or "foreign key"
	Definition string
	Table      string
	Columns    string
	References string
	OnUpdate   string
	OnDelete   string
	MatchFull  bool
	Deferrable string // "", "DEFERRABLE", or "DEFERRABLE INITIALLY DEFERRED"
	Validated  bool
	Inherited  bool
}

// Index is one index. Constraint indexes are created by their constraint.
type Index struct {
	Name       string
	Quoted     string
	Definition string
	Primary    bool
	Unique     bool
	Valid      bool
	Predicate  string
	Size       string
	Constraint bool
	Inherited  bool // attached to a partitioned parent's index
}

// Trigger is one user-visible trigger.
type Trigger struct {
	Name       string
	Quoted     string
	Definition string
	Enabled    string // "enabled", "disabled", "replica", or "always"
	Inherited  bool   // cloned from a partitioned parent's trigger
}

// Sequence holds a sequence's parameters, not its current value.
type Sequence struct {
	Type      string
	Start     string
	Increment string
	Min       string
	Max       string
	Cache     string
	Cycle     bool
	OwnedBy   string // table.column
	Identity  bool   // owned by an identity column
}

// Routines lists one schema's functions and procedures. PostgreSQL 10 lacks
// prokind; it returns no routines rather than guessing their kinds.
func (s *Session) Routines(ctx context.Context, schema string) ([]Routine, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("reading routines: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading routines: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return nil, fmt.Errorf("reading routines: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return nil, fmt.Errorf("reading routines: %w", ErrClosed)
	}
	if s.serverMajor() < 11 {
		return nil, nil
	}
	rows, err := s.catalogRows(ctx, routinesSQL, 6, schema)
	if err != nil {
		return nil, fmt.Errorf("reading routines: %w", err)
	}
	routines := make([]Routine, 0, len(rows))
	for _, row := range rows {
		oid, err := strconv.ParseUint(row[0], 10, 32)
		if err != nil {
			return routines, fmt.Errorf("reading routines: %w", err)
		}
		routines = append(routines, Routine{OID: uint32(oid), Schema: row[1], Name: row[2], Arguments: row[3], Kind: row[4], Result: row[5]})
	}
	return routines, nil
}

// Structure reads a relation's columns, keys, indexes, triggers, and size.
// It issues only catalog reads; it never begins or ends a transaction.
func (s *Session) Structure(ctx context.Context, relationOID uint32) (Structure, error) {
	var structure Structure
	if s == nil || s.conn == nil {
		return structure, fmt.Errorf("reading structure: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return structure, fmt.Errorf("reading structure: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return structure, fmt.Errorf("reading structure: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return structure, fmt.Errorf("reading structure: %w", ErrClosed)
	}
	major := s.serverMajor()
	if major < 12 {
		return structure, fmt.Errorf("reading structure: %w", Error{Message: "table structure requires PostgreSQL 12 or later"})
	}
	oid := strconv.FormatUint(uint64(relationOID), 10)
	sizes := strconv.FormatBool(s.conn.TxStatus() == 'I')
	structure.SizesSkipped = sizes == "false"
	rows, err := s.catalogRows(ctx, structureRelationSQL, 20, oid, sizes)
	if err != nil {
		return structure, fmt.Errorf("reading structure: %w", err)
	}
	if len(rows) != 1 {
		return structure, fmt.Errorf("reading structure: %w", Error{Message: "the relation no longer exists; refresh the catalog"})
	}
	row := rows[0]
	structure.Relation = Relation{OID: relationOID, Schema: row[1], Name: row[2], Kind: relationKinds[row[0]]}
	structure.Qualified, structure.Owner, structure.Comment = row[3], row[4], row[5]
	structure.RowEstimate, err = strconv.ParseFloat(row[6], 64)
	if err != nil {
		return structure, fmt.Errorf("reading structure: %w", err)
	}
	structure.Persistence, structure.Options, structure.PartitionKey = persistenceNames[row[7]], row[8], row[9]
	structure.PartitionOf, structure.PartitionBound, structure.Inherits, structure.Definition = row[10], row[11], row[12], row[13]
	structure.Populated, structure.RowSecurity, structure.ForceSecurity = row[14] == "true", row[15] == "true", row[16] == "true"
	structure.Server, structure.ServerOptions, structure.TotalSize = row[17], row[18], row[19]

	if structure.Relation.Kind == "sequence" {
		rows, err = s.catalogRows(ctx, structureSequenceSQL, 9, oid)
		if err != nil {
			return structure, fmt.Errorf("reading sequence: %w", err)
		}
		if len(rows) != 1 {
			return structure, fmt.Errorf("reading sequence: %w", Error{Message: "the sequence no longer exists; refresh the catalog"})
		}
		row = rows[0]
		structure.Sequence = Sequence{Type: row[0], Start: row[1], Increment: row[2], Min: row[3], Max: row[4], Cache: row[5], Cycle: row[6] == "true", OwnedBy: row[7], Identity: row[8] == "i"}
		return structure, nil
	}

	rows, err = s.catalogRows(ctx, structureColumnsSQL, 17, oid)
	if err != nil {
		return structure, fmt.Errorf("reading columns: %w", err)
	}
	for _, row := range rows {
		column := ColumnDefinition{Name: row[0], Quoted: row[1], Type: row[2], NotNull: row[3] == "true", Collation: row[7], Comment: row[8], Inherited: row[9] != "true", Options: row[10]}
		switch row[5] {
		case "a":
			column.Identity = "ALWAYS"
		case "d":
			column.Identity = "BY DEFAULT"
		}
		// Name only the identity sequence options that differ from the
		// defaults for the column's type: START WITH defaults to the bound
		// the sequence counts away from.
		if column.Identity != "" && row[11] != "" {
			start, increment, minimum, maximum, cache := row[11], row[12], row[13], row[14], row[15]
			descending := strings.HasPrefix(increment, "-")
			lowest, highest, first := "1", integerBounds[column.Type][1], minimum
			if descending {
				lowest, highest, first = integerBounds[column.Type][0], "-1", maximum
			}
			var options []string
			if increment != "1" {
				options = append(options, "INCREMENT BY "+increment)
			}
			if minimum != lowest {
				options = append(options, "MINVALUE "+minimum)
			}
			if maximum != highest {
				options = append(options, "MAXVALUE "+maximum)
			}
			if start != first {
				options = append(options, "START WITH "+start)
			}
			if cache != "1" {
				options = append(options, "CACHE "+cache)
			}
			if row[16] == "true" {
				options = append(options, "CYCLE")
			}
			column.IdentityOptions = strings.Join(options, " ")
		}
		if row[6] == "" {
			column.Default = row[4]
		} else {
			column.Generated, column.Virtual = row[4], row[6] == "v"
		}
		structure.Columns = append(structure.Columns, column)
	}

	for _, query := range []string{structureConstraintsSQL, structureReferencedBySQL} {
		rows, err = s.catalogRows(ctx, query, 14, oid)
		if err != nil {
			return structure, fmt.Errorf("reading constraints: %w", err)
		}
		for _, row := range rows {
			constraint := Constraint{
				Name: row[0], Quoted: row[1], Kind: constraintKinds[row[2]], Definition: row[3], Inherited: row[4] != "true",
				Table: row[5], Columns: row[6], References: row[7], OnUpdate: foreignKeyActions[row[8]], OnDelete: foreignKeyActions[row[9]],
				MatchFull: row[10] == "f", Validated: row[13] == "true",
			}
			if row[11] == "true" {
				constraint.Deferrable = "DEFERRABLE"
				if row[12] == "true" {
					constraint.Deferrable += " INITIALLY DEFERRED"
				}
			}
			if query == structureConstraintsSQL {
				structure.Constraints = append(structure.Constraints, constraint)
			} else {
				structure.ReferencedBy = append(structure.ReferencedBy, constraint)
			}
		}
	}

	rows, err = s.catalogRows(ctx, structureIndexesSQL, 10, oid, sizes)
	if err != nil {
		return structure, fmt.Errorf("reading indexes: %w", err)
	}
	for _, row := range rows {
		structure.Indexes = append(structure.Indexes, Index{
			Name: row[0], Quoted: row[1], Definition: row[2], Primary: row[3] == "true", Unique: row[4] == "true",
			Valid: row[5] == "true", Predicate: row[6], Size: row[7], Constraint: row[8] == "true", Inherited: row[9] == "true",
		})
	}

	inherited := "'false'"
	if major >= 13 {
		inherited = "(t.tgparentid <> 0)::text"
	}
	rows, err = s.catalogRows(ctx, fmt.Sprintf(structureTriggersSQL, inherited), 5, oid)
	if err != nil {
		return structure, fmt.Errorf("reading triggers: %w", err)
	}
	for _, row := range rows {
		structure.Triggers = append(structure.Triggers, Trigger{Name: row[0], Quoted: row[1], Definition: row[2], Enabled: triggerStates[row[3]], Inherited: row[4] == "true"})
	}
	return structure, nil
}

// RoutineDDL returns pg_get_functiondef for one function or procedure, with
// its comment. PostgreSQL cannot deparse aggregates this way.
func (s *Session) RoutineDDL(ctx context.Context, routineOID uint32) (string, error) {
	if s == nil || s.conn == nil {
		return "", fmt.Errorf("reading routine DDL: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("reading routine DDL: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return "", fmt.Errorf("reading routine DDL: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return "", fmt.Errorf("reading routine DDL: %w", ErrClosed)
	}
	if s.serverMajor() < 11 {
		return "", fmt.Errorf("reading routine DDL: %w", Error{Message: "routine DDL requires PostgreSQL 11 or later"})
	}
	rows, err := s.catalogRows(ctx, routineDDLSQL, 4, strconv.FormatUint(uint64(routineOID), 10))
	if err != nil {
		return "", fmt.Errorf("reading routine DDL: %w", err)
	}
	if len(rows) != 1 {
		return "", fmt.Errorf("reading routine DDL: %w", Error{Message: "the routine no longer exists; refresh the catalog"})
	}
	kind, definition, signature, comment := rows[0][0], rows[0][1], rows[0][2], rows[0][3]
	if kind == "a" {
		return "", fmt.Errorf("reading routine DDL: %w", Error{Message: "PostgreSQL cannot generate DDL for aggregate functions"})
	}
	ddl := strings.TrimRight(definition, "\n") + ";\n"
	if comment != "" {
		object := "FUNCTION"
		if kind == "p" {
			object = "PROCEDURE"
		}
		ddl += "\nCOMMENT ON " + object + " " + signature + " IS " + quoteLiteral(comment) + ";\n"
	}
	return ddl, nil
}

// DDL renders SQL that recreates a structure's relation. It omits data,
// ownership, privileges, and row-security policies, and is never executed.
func DDL(s Structure) string {
	var ddl strings.Builder
	kind := strings.ToUpper(s.Relation.Kind)
	if kind == "PARTITIONED TABLE" {
		kind = "TABLE"
	}
	persistence := ""
	switch s.Persistence {
	case "unlogged":
		persistence = "UNLOGGED "
	case "temporary":
		persistence = "TEMPORARY "
	}
	switch s.Relation.Kind {
	case "sequence":
		q := s.Sequence
		if q.Identity {
			ddl.WriteString("-- Identity column " + q.OwnedBy + " owns this sequence; PostgreSQL creates it with that column.\n")
		}
		ddl.WriteString("CREATE " + persistence + "SEQUENCE " + s.Qualified + "\n    AS " + q.Type + "\n    START WITH " + q.Start + "\n    INCREMENT BY " + q.Increment)
		ddl.WriteString("\n    MINVALUE " + q.Min + "\n    MAXVALUE " + q.Max + "\n    CACHE " + q.Cache)
		if q.Cycle {
			ddl.WriteString("\n    CYCLE")
		}
		ddl.WriteString(";\n")
		if q.OwnedBy != "" && !q.Identity {
			ddl.WriteString("ALTER SEQUENCE " + s.Qualified + " OWNED BY " + q.OwnedBy + ";\n")
		}
	case "view", "materialized view":
		ddl.WriteString("CREATE ")
		if s.Relation.Kind == "view" {
			ddl.WriteString("OR REPLACE ")
		}
		ddl.WriteString(kind + " " + s.Qualified)
		if s.Options != "" {
			ddl.WriteString(" WITH (" + s.Options + ")")
		}
		ddl.WriteString(" AS\n" + strings.TrimRight(strings.TrimSpace(s.Definition), ";"))
		if s.Relation.Kind == "materialized view" {
			if s.Populated {
				ddl.WriteString("\nWITH DATA")
			} else {
				ddl.WriteString("\nWITH NO DATA")
			}
		}
		ddl.WriteString(";\n")
	default:
		ddl.WriteString("CREATE " + persistence + kind + " " + s.Qualified)
		var elements []string
		if s.PartitionOf == "" {
			for _, column := range s.Columns {
				if column.Inherited {
					continue
				}
				element := column.Quoted + " " + column.Type
				if column.Options != "" {
					element += " OPTIONS (" + column.Options + ")"
				}
				if column.Collation != "" {
					element += " COLLATE " + column.Collation
				}
				switch {
				case column.Generated != "" && column.Virtual:
					element += " GENERATED ALWAYS AS (" + column.Generated + ") VIRTUAL"
				case column.Generated != "":
					element += " GENERATED ALWAYS AS (" + column.Generated + ") STORED"
				case column.Identity != "" && column.IdentityOptions != "":
					element += " GENERATED " + column.Identity + " AS IDENTITY (" + column.IdentityOptions + ")"
				case column.Identity != "":
					element += " GENERATED " + column.Identity + " AS IDENTITY"
				case column.Default != "":
					element += " DEFAULT " + column.Default
				}
				if column.NotNull {
					element += " NOT NULL"
				}
				elements = append(elements, element)
			}
		}
		for _, constraint := range s.Constraints {
			if !constraint.Inherited && constraint.Validated {
				elements = append(elements, "CONSTRAINT "+constraint.Quoted+" "+constraint.Definition)
			}
		}
		if s.PartitionOf != "" {
			ddl.WriteString(" PARTITION OF " + s.PartitionOf)
			if len(elements) > 0 {
				ddl.WriteString(" (\n    " + strings.Join(elements, ",\n    ") + "\n)")
			}
			ddl.WriteString("\n" + s.PartitionBound)
		} else {
			ddl.WriteString(" (\n")
			if len(elements) > 0 {
				ddl.WriteString("    " + strings.Join(elements, ",\n    ") + "\n")
			}
			ddl.WriteString(")")
			if s.Inherits != "" {
				ddl.WriteString("\nINHERITS (" + s.Inherits + ")")
			}
		}
		if s.PartitionKey != "" {
			ddl.WriteString("\nPARTITION BY " + s.PartitionKey)
		}
		if s.Options != "" {
			ddl.WriteString("\nWITH (" + s.Options + ")")
		}
		if s.Server != "" {
			ddl.WriteString("\nSERVER " + s.Server)
			if s.ServerOptions != "" {
				ddl.WriteString("\nOPTIONS (" + s.ServerOptions + ")")
			}
		}
		ddl.WriteString(";\n")
	}
	var after []string
	// CREATE TABLE validates every inline constraint, even one marked NOT VALID.
	for _, constraint := range s.Constraints {
		if !constraint.Inherited && !constraint.Validated {
			after = append(after, "ALTER TABLE "+s.Qualified+" ADD CONSTRAINT "+constraint.Quoted+" "+constraint.Definition+";")
		}
	}
	for _, index := range s.Indexes {
		if !index.Constraint && !index.Inherited {
			after = append(after, index.Definition+";")
		}
	}
	for _, trigger := range s.Triggers {
		if trigger.Inherited {
			continue
		}
		after = append(after, trigger.Definition+";")
		switch trigger.Enabled {
		case "disabled":
			after = append(after, "ALTER TABLE "+s.Qualified+" DISABLE TRIGGER "+trigger.Quoted+";")
		case "replica":
			after = append(after, "ALTER TABLE "+s.Qualified+" ENABLE REPLICA TRIGGER "+trigger.Quoted+";")
		case "always":
			after = append(after, "ALTER TABLE "+s.Qualified+" ENABLE ALWAYS TRIGGER "+trigger.Quoted+";")
		}
	}
	if s.RowSecurity {
		after = append(after, "ALTER TABLE "+s.Qualified+" ENABLE ROW LEVEL SECURITY;")
	}
	if s.ForceSecurity {
		after = append(after, "ALTER TABLE "+s.Qualified+" FORCE ROW LEVEL SECURITY;")
	}
	if len(after) > 0 {
		ddl.WriteString("\n" + strings.Join(after, "\n") + "\n")
	}
	var comments []string
	if s.Comment != "" {
		comments = append(comments, "COMMENT ON "+kind+" "+s.Qualified+" IS "+quoteLiteral(s.Comment)+";")
	}
	for _, column := range s.Columns {
		if column.Comment != "" {
			comments = append(comments, "COMMENT ON COLUMN "+s.Qualified+"."+column.Quoted+" IS "+quoteLiteral(column.Comment)+";")
		}
	}
	if len(comments) > 0 {
		ddl.WriteString("\n" + strings.Join(comments, "\n") + "\n")
	}
	return ddl.String()
}

// quoteLiteral matches PostgreSQL's quote_literal: quotes are doubled, and
// text containing a backslash becomes an escape string (E prefix) with
// doubled backslashes, which reads the same under either setting of
// standard_conforming_strings.
func quoteLiteral(text string) string {
	quoted := strings.ReplaceAll(text, `'`, `''`)
	if strings.Contains(text, `\`) {
		return `E'` + strings.ReplaceAll(quoted, `\`, `\\`) + `'`
	}
	return `'` + quoted + `'`
}

// serverMajor reads the version reported when the session started. Callers
// own the connection, so ParameterStatus is not read during another query.
func (s *Session) serverMajor() int {
	major := 0
	for _, r := range s.conn.ParameterStatus("server_version") {
		if r < '0' || r > '9' {
			break
		}
		major = major*10 + int(r-'0')
	}
	return major
}

// catalogRows reads one bounded catalog query while the caller owns the
// connection. Parameters are bound, never interpolated; NULL reads as "".
func (s *Session) catalogRows(ctx context.Context, sql string, columns int, parameters ...string) ([][]string, error) {
	values := make([][]byte, len(parameters))
	for i, parameter := range parameters {
		values[i] = []byte(parameter)
	}
	var rows [][]string
	var readErr error
	reader := s.conn.ExecParams(ctx, sql, values, nil, nil, nil)
	for reader.NextRow() {
		if len(rows) >= catalogLimit {
			readErr = errors.New("catalog result exceeds 10,000 rows")
			break
		}
		raw := reader.Values()
		if len(raw) != columns {
			readErr = errors.New("unexpected catalog columns")
			break
		}
		row := make([]string, columns)
		for i, value := range raw {
			row[i] = string(value)
		}
		rows = append(rows, row)
	}
	_, err := reader.Close()
	if err != nil {
		return rows, queryError(err)
	}
	return rows, readErr
}
