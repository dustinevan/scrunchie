// Package result contains PostgreSQL result values without driver dependencies.
package result

import "time"

// Cell preserves SQL NULL separately from empty text and empty bytes. Text is
// PostgreSQL's text representation. Bytes holds decoded bytea when OID is 17.
type Cell struct {
	Text  string
	Bytes []byte
	Null  bool
}

// Column describes one result field, including its PostgreSQL type OID.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	OID  uint32 `json:"oid"`
}

// Set is one statement's result. Rows contains only the retained display rows;
// RowCount counts all rows received. Truncated means retention limits were hit.
// HasMore means the server has more rows that were never fetched: the query
// stopped after a page, so RowCount is not the query's total.
type Set struct {
	Columns   []Column
	Rows      [][]Cell
	Command   string
	RowCount  int64
	Truncated bool
	HasMore   bool
}

// Batch contains the results of executing SQL in one persistent session.
// TxStatus is PostgreSQL's I (idle), T (transaction), or E (failed transaction).
// ReadOnly reports whether writes are refused after the batch: inside a
// transaction, by that transaction's mode, which BEGIN READ WRITE or SET
// TRANSACTION can change; otherwise by the mode of new transactions. It is
// meaningful only when Connected.
type Batch struct {
	Results   []Set
	Duration  time.Duration
	TxStatus  byte
	Notices   []string
	Connected bool
	Truncated bool
	ReadOnly  bool
}

// Limits bounds retained output. Without PageRows, the driver still drains
// every response so reaching a display limit does not change transaction
// behavior. A positive PageRows asks for at most that many rows (and at most
// MaxRows) of a single plain read; the server stops computing after them.
type Limits struct {
	MaxRows  int
	MaxBytes int64
	PageRows int
}
