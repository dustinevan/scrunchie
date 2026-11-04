package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const catalogLimit = 10000

const databasesSQL = `
SELECT d.datname
FROM pg_catalog.pg_database AS d
WHERE NOT d.datistemplate AND d.datallowconn
  AND pg_catalog.has_database_privilege(d.oid, 'CONNECT')
ORDER BY d.datname
LIMIT 10001`

const schemasSQL = `
SELECT n.nspname
FROM pg_catalog.pg_namespace AS n
WHERE n.nspname NOT LIKE 'pg_toast%'
  AND (n.nspname NOT LIKE 'pg_temp_%' OR n.oid = pg_catalog.pg_my_temp_schema())
  AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
ORDER BY n.nspname
LIMIT 10001`

const relationsSQL = `
SELECT c.oid::text, n.nspname, c.relname,
       CASE c.relkind
         WHEN 'r' THEN 'table'
         WHEN 'p' THEN 'partitioned table'
         WHEN 'v' THEN 'view'
         WHEN 'm' THEN 'materialized view'
         WHEN 'f' THEN 'foreign table'
         WHEN 'S' THEN 'sequence'
       END
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
ORDER BY c.relname
LIMIT 10001`

const columnsSQL = `
SELECT a.attnum::text, a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod),
       a.atttypid::text, (NOT a.attnotnull)::text,
       COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), ''),
       EXISTS (
         SELECT 1 FROM pg_catalog.pg_constraint AS p
         WHERE p.conrelid = a.attrelid AND p.contype = 'p'
           AND a.attnum = ANY(p.conkey)
       )::text
FROM pg_catalog.pg_attribute AS a
LEFT JOIN pg_catalog.pg_attrdef AS d
  ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1::oid AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum
LIMIT 10001`

// Schema is an accessible PostgreSQL namespace.
type Schema struct {
	Name string
}

// Relation is a table, view, materialized view, foreign table, or sequence.
type Relation struct {
	OID    uint32
	Schema string
	Name   string
	Kind   string
}

// QualifiedName returns a SQL identifier with each name quoted independently.
func (r Relation) QualifiedName() string {
	return `"` + strings.ReplaceAll(r.Schema, `"`, `""`) + `"."` + strings.ReplaceAll(r.Name, `"`, `""`) + `"`
}

// Column describes one catalog column. Position is the PostgreSQL attribute
// number; it may have gaps where columns were dropped.
type Column struct {
	Name       string
	Type       string
	OID        uint32
	Nullable   bool
	Default    string
	PrimaryKey bool
	Position   int
}

// Databases lists non-template databases to which the current role may connect.
// This is an explicit catalog request; it never opens another connection.
func (s *Session) Databases(ctx context.Context) ([]string, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("reading databases: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading databases: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return nil, fmt.Errorf("reading databases: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return nil, fmt.Errorf("reading databases: %w", ErrClosed)
	}
	var databases []string
	var readErr error
	reader := s.conn.ExecParams(ctx, databasesSQL, nil, nil, nil, nil)
	for reader.NextRow() {
		if len(databases) >= catalogLimit {
			readErr = errors.New("database catalog exceeds 10,000 entries")
			break
		}
		values := reader.Values()
		if len(values) != 1 || values[0] == nil {
			readErr = errors.New("unexpected database catalog columns")
			break
		}
		databases = append(databases, string(values[0]))
	}
	_, err := reader.Close()
	if err != nil {
		return databases, fmt.Errorf("reading databases: %w", queryError(err))
	}
	if readErr != nil {
		return databases, fmt.Errorf("reading databases: %w", readErr)
	}
	return databases, nil
}

// Schemas returns accessible namespaces, including system namespaces and this
// session's temporary namespace. Catalog work returns ErrBusy during execution.
func (s *Session) Schemas(ctx context.Context) ([]Schema, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("reading schemas: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading schemas: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return nil, fmt.Errorf("reading schemas: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return nil, fmt.Errorf("reading schemas: %w", ErrClosed)
	}
	var schemas []Schema
	var readErr error
	reader := s.conn.ExecParams(ctx, schemasSQL, nil, nil, nil, nil)
	for reader.NextRow() {
		if len(schemas) >= catalogLimit {
			readErr = errors.New("schema catalog exceeds 10,000 entries")
			break
		}
		values := reader.Values()
		if len(values) != 1 {
			readErr = errors.New("unexpected schema catalog columns")
			break
		}
		schemas = append(schemas, Schema{Name: string(values[0])})
	}
	_, err := reader.Close()
	if err != nil {
		return schemas, fmt.Errorf("reading schemas: %w", queryError(err))
	}
	if readErr != nil {
		return schemas, fmt.Errorf("reading schemas: %w", readErr)
	}
	return schemas, nil
}

// Relations reads one schema lazily. Schema names are bound parameters.
func (s *Session) Relations(ctx context.Context, schema string) ([]Relation, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("reading relations: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading relations: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return nil, fmt.Errorf("reading relations: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return nil, fmt.Errorf("reading relations: %w", ErrClosed)
	}
	var relations []Relation
	var readErr error
	reader := s.conn.ExecParams(ctx, relationsSQL, [][]byte{[]byte(schema)}, nil, nil, nil)
	for reader.NextRow() {
		if len(relations) >= catalogLimit {
			readErr = errors.New("relation catalog exceeds 10,000 entries in this schema")
			break
		}
		values := reader.Values()
		if len(values) != 4 {
			readErr = errors.New("unexpected relation catalog columns")
			break
		}
		oid, err := strconv.ParseUint(string(values[0]), 10, 32)
		if err != nil {
			readErr = err
			break
		}
		relations = append(relations, Relation{
			OID:    uint32(oid),
			Schema: string(values[1]),
			Name:   string(values[2]),
			Kind:   string(values[3]),
		})
	}
	_, err := reader.Close()
	if err != nil {
		return relations, fmt.Errorf("reading relations: %w", queryError(err))
	}
	if readErr != nil {
		return relations, fmt.Errorf("reading relations: %w", readErr)
	}
	return relations, nil
}

// Columns reads types, defaults, nullability, and primary-key membership for
// one relation. The relation OID is bound as a parameter.
func (s *Session) Columns(ctx context.Context, relationOID uint32) ([]Column, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("reading columns: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return nil, fmt.Errorf("reading columns: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return nil, fmt.Errorf("reading columns: %w", ErrClosed)
	}
	var columns []Column
	var readErr error
	parameter := []byte(strconv.FormatUint(uint64(relationOID), 10))
	reader := s.conn.ExecParams(ctx, columnsSQL, [][]byte{parameter}, nil, nil, nil)
	for reader.NextRow() {
		if len(columns) >= catalogLimit {
			readErr = errors.New("column catalog exceeds 10,000 entries")
			break
		}
		values := reader.Values()
		if len(values) != 7 {
			readErr = errors.New("unexpected column catalog columns")
			break
		}
		position, err := strconv.Atoi(string(values[0]))
		if err != nil {
			readErr = err
			break
		}
		oid, err := strconv.ParseUint(string(values[3]), 10, 32)
		if err != nil {
			readErr = err
			break
		}
		nullable, err := strconv.ParseBool(string(values[4]))
		if err != nil {
			readErr = err
			break
		}
		primaryKey, err := strconv.ParseBool(string(values[6]))
		if err != nil {
			readErr = err
			break
		}
		columns = append(columns, Column{
			Name:       string(values[1]),
			Type:       string(values[2]),
			OID:        uint32(oid),
			Nullable:   nullable,
			Default:    string(values[5]),
			PrimaryKey: primaryKey,
			Position:   position,
		})
	}
	_, err := reader.Close()
	if err != nil {
		return columns, fmt.Errorf("reading columns: %w", queryError(err))
	}
	if readErr != nil {
		return columns, fmt.Errorf("reading columns: %w", readErr)
	}
	return columns, nil
}

// Completion catalogs are bounded so a database with an enormous catalog
// cannot grow memory without limit; capped schemas load lazily when named.
const (
	completionSchemaRelations = 2000
	completionRelations       = 20000
)

const completionSearchPathSQL = `
SELECT s FROM pg_catalog.unnest(pg_catalog.current_schemas(true)) AS s`

const completionRelationsSQL = `
SELECT r.oid::text, r.nspname, r.relname, r.kind
FROM (
  SELECT c.oid, n.nspname, c.relname,
         CASE c.relkind
           WHEN 'r' THEN 'table'
           WHEN 'p' THEN 'partitioned table'
           WHEN 'v' THEN 'view'
           WHEN 'm' THEN 'materialized view'
           WHEN 'f' THEN 'foreign table'
         END AS kind,
         pg_catalog.row_number() OVER (PARTITION BY c.relnamespace ORDER BY c.relname) AS position
  FROM pg_catalog.pg_class AS c
  JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
    AND NOT c.relispartition
    AND n.nspname NOT IN ('pg_catalog', 'information_schema')
    AND n.nspname NOT LIKE 'pg_toast%'
    AND (n.nspname NOT LIKE 'pg_temp_%' OR n.oid = pg_catalog.pg_my_temp_schema())
    AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
) AS r
WHERE r.position <= 2001
ORDER BY r.nspname, r.relname
LIMIT 20001`

// CompletionNames holds the names SQL completion offers before any table is
// expanded. Listed names the schemas whose relation lists are complete;
// system schemas and capped schemas are absent and load on request.
type CompletionNames struct {
	SearchPath []string
	Schemas    []string
	Relations  []Relation
	Listed     []string
}

// CompletionNames reads search_path, accessible schemas, and relation names
// in non-system schemas (tables, views, materialized views, and foreign tables;
// partitions are omitted). Each schema contributes at most 2,000 relations and
// the whole list at most 20,000. Catalog work returns ErrBusy during execution.
func (s *Session) CompletionNames(ctx context.Context) (CompletionNames, error) {
	var names CompletionNames
	if s == nil || s.conn == nil {
		return names, fmt.Errorf("reading completion names: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return names, fmt.Errorf("reading completion names: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return names, fmt.Errorf("reading completion names: %w", ErrBusy)
	}
	if s.conn.IsClosed() {
		return names, fmt.Errorf("reading completion names: %w", ErrClosed)
	}
	reader := s.conn.ExecParams(ctx, completionSearchPathSQL, nil, nil, nil, nil)
	for reader.NextRow() {
		if values := reader.Values(); len(values) == 1 && len(names.SearchPath) < catalogLimit {
			names.SearchPath = append(names.SearchPath, string(values[0]))
		}
	}
	if _, err := reader.Close(); err != nil {
		return names, fmt.Errorf("reading search_path: %w", queryError(err))
	}
	var readErr error
	reader = s.conn.ExecParams(ctx, schemasSQL, nil, nil, nil, nil)
	for reader.NextRow() {
		values := reader.Values()
		if len(values) != 1 {
			readErr = errors.New("unexpected schema catalog columns")
			break
		}
		if len(names.Schemas) < catalogLimit {
			names.Schemas = append(names.Schemas, string(values[0]))
		}
	}
	if _, err := reader.Close(); err != nil {
		return names, fmt.Errorf("reading completion schemas: %w", queryError(err))
	}
	if readErr != nil {
		return names, fmt.Errorf("reading completion schemas: %w", readErr)
	}
	capped := make(map[string]bool)
	rows, current, count := 0, "", 0
	reader = s.conn.ExecParams(ctx, completionRelationsSQL, nil, nil, nil, nil)
	for reader.NextRow() {
		values := reader.Values()
		if len(values) != 4 {
			readErr = errors.New("unexpected relation catalog columns")
			break
		}
		oid, err := strconv.ParseUint(string(values[0]), 10, 32)
		if err != nil {
			readErr = err
			break
		}
		relation := Relation{OID: uint32(oid), Schema: string(values[1]), Name: string(values[2]), Kind: string(values[3])}
		rows++
		if relation.Schema != current {
			current, count = relation.Schema, 0
		}
		count++
		if rows > completionRelations || count > completionSchemaRelations {
			capped[relation.Schema] = true
			continue
		}
		names.Relations = append(names.Relations, relation)
	}
	if _, err := reader.Close(); err != nil {
		return names, fmt.Errorf("reading completion relations: %w", queryError(err))
	}
	if readErr != nil {
		return names, fmt.Errorf("reading completion relations: %w", readErr)
	}
	last := ""
	if rows > completionRelations {
		last = current
	}
	for _, schema := range names.Schemas {
		system := schema == "pg_catalog" || schema == "information_schema" || strings.HasPrefix(schema, "pg_toast")
		if system || capped[schema] || last != "" && schema >= last {
			continue
		}
		names.Listed = append(names.Listed, schema)
	}
	return names, nil
}
