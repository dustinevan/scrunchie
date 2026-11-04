// Package postgres owns one PostgreSQL session and its catalog queries.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	defaultMaxRows  = 10000
	defaultMaxBytes = 32 << 20
	maxResultSets   = 1000
	maxNotices      = 100
	maxNoticeLength = 4096
	cancelGrace     = 5 * time.Second
)

var (
	ErrBusy   = errors.New("session is busy")
	ErrClosed = errors.New("session is closed")
)

// Error retains PostgreSQL error fields without retaining connection settings.
// Position is the server's one-based character position in the submitted SQL.
type Error struct {
	Message  string
	Detail   string
	Hint     string
	Code     string
	Position int
}

func (e Error) Error() string {
	if e.Code != "" {
		return e.Message + " (SQLSTATE " + e.Code + ")"
	}
	return e.Message
}

// Session keeps temporary tables, SET commands, and transactions on one
// connection. Callers cancel an Execute through its context. No query is retried.
type Session struct {
	conn        *pgconn.PgConn
	available   chan struct{}
	description string
	database    string
	types       *pgtype.Map
	notices     []string
	// The UI needs the lexical mode without reading the active connection.
	backslashEscapes atomic.Bool
	// readOnly and standby describe new transactions after the last completed
	// operation. transactionReadOnly is the mode Execute last reported in
	// Batch.ReadOnly: the open transaction's, or else the next one's. Only the
	// connection's owner uses it.
	readOnly            atomic.Bool
	standby             atomic.Bool
	transactionReadOnly bool
	// pageCursor names Execute's page cursor. A random name per session keeps
	// it from colliding with the user's cursors.
	pageCursor string
}

// Options selects session behavior. The zero value opens a read-only session.
type Options struct {
	// AllowWrites leaves default_transaction_read_only to the server and the
	// connection string. Otherwise the startup packet sets it on, overriding
	// connection-string options; RESET ALL and DISCARD ALL restore that value.
	AllowWrites bool
}

// Open connects using PostgreSQL URL or keyword syntax. pgx reads PostgreSQL
// environment variables, service files, and password files when resolving it.
func Open(ctx context.Context, connectionString string, options Options) (*Session, error) {
	config, err := pgconn.ParseConfig(connectionString)
	if err != nil {
		// Driver parse errors can include the entire DSN, even when malformed.
		return nil, fmt.Errorf("opening session: %w", Error{Message: "invalid PostgreSQL connection settings; check the connection string, PG environment, and service file"})
	}
	replication := strings.ToLower(strings.TrimSpace(config.RuntimeParams["replication"]))
	if replication != "" && replication != "0" && replication != "false" && replication != "f" && replication != "off" && replication != "no" {
		return nil, fmt.Errorf("opening session: %w", Error{Message: "replication connections are not supported; connect to PostgreSQL in normal SQL mode"})
	}
	if config.RuntimeParams["application_name"] == "" {
		config.RuntimeParams["application_name"] = "scrunchie"
	}
	config.RuntimeParams["client_encoding"] = "UTF8"
	if !options.AllowWrites {
		for key := range config.RuntimeParams {
			// Setting names are case-insensitive; send exactly one value.
			if strings.EqualFold(key, "default_transaction_read_only") {
				delete(config.RuntimeParams, key)
			}
		}
		config.RuntimeParams["default_transaction_read_only"] = "on"
	}
	config.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn:               conn,
			CancelRequestDelay: 0,
			DeadlineDelay:      cancelGrace,
		}
	}
	var name [8]byte
	if _, err := rand.Read(name[:]); err != nil {
		return nil, fmt.Errorf("opening session: naming the page cursor: %w", err)
	}
	session := &Session{
		available: make(chan struct{}, 1),
		types:     pgtype.NewMap(),
		// The requested mode stands until the server reports or shows one.
		transactionReadOnly: !options.AllowWrites,
		pageCursor:          "scrunch_page_" + hex.EncodeToString(name[:]),
	}
	session.readOnly.Store(!options.AllowWrites)
	config.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		if len(session.notices) >= maxNotices {
			return
		}
		message := notice.Severity + ": " + notice.Message
		if len(message) > maxNoticeLength {
			message = message[:maxNoticeLength] + "…"
		}
		session.notices = append(session.notices, message)
	}
	conn, err := pgconn.ConnectConfig(ctx, config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("opening session: %w", ctx.Err())
		}
		message := "could not connect to PostgreSQL; check the endpoint, credentials, and TLS settings"
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			// Connection error messages can reflect supplied user and database names.
			return nil, fmt.Errorf("opening session: %w", Error{Message: "PostgreSQL rejected the connection", Code: pgErr.Code})
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			message = "could not resolve the PostgreSQL host"
		}
		if pgconn.Timeout(err) {
			message = "PostgreSQL connection timed out"
		}
		return nil, fmt.Errorf("opening session: %w", Error{Message: message})
	}
	session.conn = conn
	session.backslashEscapes.Store(conn.ParameterStatus("standard_conforming_strings") == "off")
	session.refreshWriteMode(conn.ParameterStatus("default_transaction_read_only"))
	label := config.User + "@" + config.Host + ":" + strconv.Itoa(int(config.Port)) + "/" + config.Database
	var description strings.Builder
	for _, r := range label {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			description.WriteString(strconv.QuoteRune(r))
		} else {
			description.WriteRune(r)
		}
	}
	session.description = description.String()
	session.database = config.Database
	session.available <- struct{}{}
	return session, nil
}

// Description identifies the session without a password or connection string.
func (s *Session) Description() string {
	if s == nil {
		return "PostgreSQL"
	}
	return s.description
}

// Database names the connected database as given to PostgreSQL. It may hold
// control characters; display it through termtext.
func (s *Session) Database() string {
	if s == nil {
		return ""
	}
	return s.database
}

// BackslashEscapes reports the last completed operation's string escape mode.
// It can be read while a catalog request or query owns the connection.
func (s *Session) BackslashEscapes() bool {
	return s != nil && s.backslashEscapes.Load()
}

// ReadOnly reports whether new transactions are read-only after the last
// completed operation. It prefers the server's reported
// default_transaction_read_only (PostgreSQL 14 and later), treats a hot
// standby as read-only, and on older servers uses SHOW transaction_read_only
// from the last operation that ended outside a transaction. It can be read
// while another operation owns the connection. BEGIN READ WRITE still starts
// a writable transaction in a read-only session; Batch.ReadOnly reports that
// transaction's mode.
func (s *Session) ReadOnly() bool {
	return s == nil || s.conn == nil || s.readOnly.Load()
}

// Standby reports a hot standby server, which refuses writes in any mode.
func (s *Session) Standby() bool {
	return s != nil && s.standby.Load()
}

// SetReadOnly changes default_transaction_read_only for later transactions.
// It waits for a catalog request that owns the connection, and refuses inside
// a transaction, where the change would not apply to the current transaction
// and a rollback would undo it.
func (s *Session) SetReadOnly(ctx context.Context, readOnly bool) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("changing write mode: %w", ErrClosed)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	case <-ctx.Done():
		return fmt.Errorf("changing write mode: %w", ctx.Err())
	}
	if s.conn.IsClosed() {
		return fmt.Errorf("changing write mode: %w", ErrClosed)
	}
	if s.conn.TxStatus() != 'I' {
		return fmt.Errorf("changing write mode: %w", Error{Message: "commit or roll back the open transaction first"})
	}
	value := "off"
	if readOnly {
		value = "on"
	}
	_, err := s.conn.Exec(ctx, "SET default_transaction_read_only = "+value).ReadAll()
	if err == nil {
		// The fallback if the server neither reports nor shows the new value.
		s.readOnly.Store(readOnly || s.standby.Load())
	}
	if !s.conn.IsClosed() {
		// A canceled request may still have changed the setting.
		s.refreshWriteMode(s.conn.ParameterStatus("default_transaction_read_only"))
	}
	if err != nil {
		return fmt.Errorf("changing write mode: %w", queryError(err))
	}
	return nil
}

// refreshWriteMode records the write mode after an operation and returns
// whether writes are refused now: in the open transaction, or else in the
// next one. reported is the server's default_transaction_read_only report,
// which PostgreSQL 14 and later send, or "". The caller owns the open
// connection.
func (s *Session) refreshWriteMode(reported string) bool {
	standby := s.conn.ParameterStatus("in_hot_standby") == "on"
	s.standby.Store(standby)
	if reported != "" {
		s.readOnly.Store(reported == "on" || standby)
	}
	status := s.conn.TxStatus()
	if status == 'E' {
		// A failed transaction refuses every statement until it ends, and
		// would refuse SHOW too.
		return s.transactionReadOnly
	}
	if status == 'T' || reported == "" {
		// BEGIN READ WRITE, SET TRANSACTION, and SET transaction_read_only
		// change the open transaction without a report, and older servers
		// report no default; outside a transaction, transaction_read_only is
		// the next transaction's mode. The caller's context is not used: a
		// late cancel would fail this statement and abort the transaction.
		// The SHOW's notices are not the user's.
		notices := len(s.notices)
		results, err := s.conn.Exec(context.Background(), "SHOW transaction_read_only").ReadAll()
		s.notices = s.notices[:notices]
		if err == nil && len(results) == 1 && results[0].Err == nil && len(results[0].Rows) == 1 && len(results[0].Rows[0]) == 1 {
			s.transactionReadOnly = string(results[0].Rows[0][0]) == "on" || standby
			if status == 'I' {
				s.readOnly.Store(s.transactionReadOnly)
			}
			return s.transactionReadOnly
		}
	}
	if status == 'I' {
		s.transactionReadOnly = s.readOnly.Load()
	}
	return s.transactionReadOnly
}

// Execute returns every retained statement result. Reaching a limit stops
// retention, then drains the remaining server output. Zero or negative limits
// use defaults: 10,000 rows and 32 MiB across the batch.
//
// SQL is sent unchanged unless limits.PageRows is positive and sql is one plain
// read (sqledit.PagedQuery) outside a failed transaction. That query is then
// declared as the session's NO SCROLL cursor scrunch_page_<random hex>, fetched,
// and closed in one round trip, so PostgreSQL computes only the page plus one
// row that sets HasMore. Outside a transaction the three statements share an
// implicit transaction; inside one, the cursor is closed before Execute
// returns. The caller sees one result named like the query's own SELECT, and
// error positions refer to sql. If an idle session rejects the cursor itself,
// sql then runs unchanged.
//
// Inside a transaction, and on servers before PostgreSQL 14, Execute then
// sends SHOW transaction_read_only so Batch.ReadOnly matches the server.
func (s *Session) Execute(ctx context.Context, sql string, limits result.Limits) (result.Batch, error) {
	var batch result.Batch
	if s == nil || s.conn == nil {
		return batch, fmt.Errorf("executing SQL: %w", ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return batch, fmt.Errorf("executing SQL: %w", err)
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	default:
		return batch, fmt.Errorf("executing SQL: %w", ErrBusy)
	}
	batch.Connected = !s.conn.IsClosed()
	batch.TxStatus = s.conn.TxStatus()
	batch.ReadOnly = s.transactionReadOnly
	if !batch.Connected {
		batch.TxStatus = 0
		return batch, fmt.Errorf("executing SQL: %w", ErrClosed)
	}
	if strings.IndexByte(sql, 0) >= 0 {
		return batch, fmt.Errorf("executing SQL: %w", Error{Message: "SQL contains a NUL byte"})
	}
	if sqltext.HasCopy(sql, s.conn.ParameterStatus("standard_conforming_strings") == "off") {
		return batch, fmt.Errorf("executing SQL: %w", Error{Message: "COPY is not supported in the editor; use SQL queries and explicit result export"})
	}
	if limits.MaxRows <= 0 {
		limits.MaxRows = defaultMaxRows
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaultMaxBytes
	}
	started := time.Now()
	query, body, page := sql, "", 0
	// The prefix's length is subtracted from PostgreSQL error positions; it is
	// ASCII, so bytes equal characters.
	prefix := "DECLARE " + s.pageCursor + " NO SCROLL CURSOR FOR "
	if limits.PageRows > 0 && batch.TxStatus != 'E' && s.conn.ParameterStatus("standard_conforming_strings") != "off" {
		if text, ok := sqledit.PagedQuery(sql); ok {
			body, page = text, min(limits.PageRows, limits.MaxRows)
			// The newline ends a trailing line comment before the semicolon.
			query = prefix + body + "\n; FETCH FORWARD " + strconv.Itoa(page+1) + " FROM " + s.pageCursor + "; CLOSE " + s.pageCursor
		}
	}
	idle := batch.TxStatus == 'I'
	begun, err := s.exchange(ctx, &batch, query, limits, page)
	var pgErr *pgconn.PgError
	if page > 0 && idle && begun == 0 && ctx.Err() == nil && !s.conn.IsClosed() && errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "0A000", "24000", "25P01", "34000", "42P03", "42P11":
			// PostgreSQL rejected the cursor before the query began, and the
			// implicit transaction ended with it, so the query has not run.
			page = 0
			_, err = s.exchange(ctx, &batch, sql, limits, 0)
		}
	}
	if page > 0 && errors.As(err, &pgErr) && pgErr.Position > 0 {
		position, length := int(pgErr.Position)-len(prefix), utf8.RuneCountInString(body)
		switch {
		case position < 1:
			pgErr.Position = 0
		case position > length:
			// The wrapper's semicolon stands where the submitted SQL ended.
			pgErr.Position = int32(length + 1)
			if pgErr.Code == "42601" && pgErr.Message == `syntax error at or near ";"` {
				pgErr.Message = "syntax error at end of input"
			}
		default:
			pgErr.Position = int32(position)
		}
	}
	batch.Duration = time.Since(started)
	if !s.conn.IsClosed() {
		s.backslashEscapes.Store(s.conn.ParameterStatus("standard_conforming_strings") == "off")
		batch.ReadOnly = s.refreshWriteMode(s.conn.ParameterStatus("default_transaction_read_only"))
	}
	// Read after the write-mode check, so even a check that failed leaves
	// the batch describing the connection as it is.
	batch.Connected = !s.conn.IsClosed()
	batch.TxStatus = 0
	if batch.Connected {
		batch.TxStatus = s.conn.TxStatus()
	}
	batch.Notices = s.notices
	s.notices = nil
	if err != nil {
		return batch, fmt.Errorf("executing SQL: %w", queryError(err))
	}
	return batch, nil
}

// exchange sends one simple-protocol query and replaces batch.Results and
// batch.Truncated with the output retained within limits. A positive page means
// query is Execute's DECLARE, FETCH, CLOSE wrapper: only the FETCH result is
// retained, and the row after the page sets HasMore instead of being kept.
// It returns how many statement results PostgreSQL began, and the first error.
func (s *Session) exchange(ctx context.Context, batch *result.Batch, query string, limits result.Limits, page int) (int, error) {
	batch.Results, batch.Truncated = nil, false
	s.notices = nil
	var retainedBytes int64
	var retainedRows int
	var valueErr error
	begun := 0
	multiple := s.conn.Exec(ctx, query)
	for multiple.NextResult() {
		begun++
		reader := multiple.ResultReader()
		if page > 0 && begun != 2 {
			// DECLARE and CLOSE belong to the wrapper, not to the submitted SQL.
			if _, err := reader.Close(); err != nil {
				break
			}
			continue
		}
		fields := reader.FieldDescriptions()
		set := result.Set{Columns: make([]result.Column, len(fields))}
		// Account for retained headers and Go slice/string storage, not only text.
		setBytes := int64(80 + len(fields)*40)
		for i, field := range fields {
			typeName := "oid:" + strconv.FormatUint(uint64(field.DataTypeOID), 10)
			if dataType, ok := s.types.TypeForOID(field.DataTypeOID); ok {
				typeName = dataType.Name
			}
			set.Columns[i] = result.Column{Name: field.Name, Type: typeName, OID: field.DataTypeOID}
			setBytes += int64(len(field.Name) + len(typeName))
		}
		retainSet := len(batch.Results) < maxResultSets && setBytes <= limits.MaxBytes-retainedBytes
		if retainSet {
			retainedBytes += setBytes
		} else {
			batch.Truncated = true
		}
		unreadableSet := false
		for reader.NextRow() {
			if page > 0 && set.RowCount == int64(page) {
				// FETCH asked for one extra row only to learn that more exist.
				set.HasMore = true
				continue
			}
			set.RowCount++
			values := reader.Values()
			rowBytes := int64(24 + len(values)*48)
			for i, value := range values {
				rowBytes += int64(len(value))
				if fields[i].DataTypeOID == pgtype.ByteaOID {
					// Escape-format bytea cannot decode to more bytes than its text.
					rowBytes += int64(len(value))
				}
			}
			if !retainSet || retainedRows >= limits.MaxRows || rowBytes > limits.MaxBytes-retainedBytes || unreadableSet {
				set.Truncated = true
				batch.Truncated = true
				continue
			}
			row := make([]result.Cell, len(values))
			var decodeErr error
			for i, value := range values {
				if value == nil {
					row[i].Null = true
					continue
				}
				switch fields[i].Format {
				case pgtype.TextFormatCode:
					row[i].Text = string(value)
					if fields[i].DataTypeOID == pgtype.ByteaOID {
						row[i].Bytes, decodeErr = decodeBytea(value)
					}
				case pgtype.BinaryFormatCode:
					if fields[i].DataTypeOID == pgtype.ByteaOID {
						row[i].Bytes = append([]byte{}, value...)
						row[i].Text = `\x` + hex.EncodeToString(value)
					} else if _, known := s.types.TypeForOID(fields[i].DataTypeOID); !known {
						// Map.Scan assumes string data for unknown OIDs; that would
						// silently misrepresent an extension's binary representation.
						decodeErr = fmt.Errorf("decoding result: unsupported binary type OID %d; declare the cursor without BINARY", fields[i].DataTypeOID)
					} else if err := s.types.Scan(fields[i].DataTypeOID, fields[i].Format, value, &row[i].Text); err != nil {
						decodeErr = fmt.Errorf("decoding binary result (OID %d): %w", fields[i].DataTypeOID, err)
					}
				default:
					decodeErr = fmt.Errorf("decoding result: unsupported PostgreSQL format %d", fields[i].Format)
				}
				if decodeErr != nil {
					break
				}
			}
			if decodeErr != nil {
				if valueErr == nil {
					valueErr = decodeErr
				}
				unreadableSet = true
				set.Truncated = true
				batch.Truncated = true
				continue
			}
			// Binary values can expand when converted to their textual forms.
			rowBytes = int64(24 + len(row)*48)
			for _, value := range row {
				rowBytes += int64(len(value.Text) + len(value.Bytes))
			}
			if rowBytes > limits.MaxBytes-retainedBytes {
				set.Truncated = true
				batch.Truncated = true
				continue
			}
			set.Rows = append(set.Rows, row)
			retainedRows++
			retainedBytes += rowBytes
		}
		command, err := reader.Close()
		set.Command = command.String()
		if page > 0 && err == nil {
			set.Command = "SELECT " + strconv.FormatInt(set.RowCount, 10)
		}
		if retainSet {
			batch.Results = append(batch.Results, set)
		}
		if err != nil {
			// Close below still consumes ReadyForQuery before releasing the session.
			break
		}
	}
	if err := multiple.Close(); err != nil {
		return begun, err
	}
	return begun, valueErr
}

// Close waits for the current operation to release the connection. The caller
// should cancel that operation first. Closing never commits an open transaction.
func (s *Session) Close(ctx context.Context) error {
	if s == nil || s.conn == nil {
		return nil
	}
	select {
	case <-s.available:
		defer func() { s.available <- struct{}{} }()
	case <-ctx.Done():
		return fmt.Errorf("closing session: %w", ctx.Err())
	}
	if err := s.conn.Close(ctx); err != nil {
		return fmt.Errorf("closing session: %w", queryError(err))
	}
	return nil
}

func queryError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return Error{
			Message:  pgErr.Message,
			Detail:   pgErr.Detail,
			Hint:     pgErr.Hint,
			Code:     pgErr.Code,
			Position: int(pgErr.Position),
		}
	}
	return err
}

func decodeBytea(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == '\\' && raw[1] == 'x' {
		decoded := make([]byte, hex.DecodedLen(len(raw)-2))
		n, err := hex.Decode(decoded, raw[2:])
		if err != nil {
			return nil, fmt.Errorf("decoding bytea: %w", err)
		}
		return decoded[:n], nil
	}
	decoded := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			decoded = append(decoded, raw[i])
			continue
		}
		if i+1 < len(raw) && raw[i+1] == '\\' {
			decoded = append(decoded, '\\')
			i++
			continue
		}
		if i+3 >= len(raw) || raw[i+1] < '0' || raw[i+1] > '3' || raw[i+2] < '0' || raw[i+2] > '7' || raw[i+3] < '0' || raw[i+3] > '7' {
			return nil, fmt.Errorf("decoding bytea: %w", errors.New("invalid PostgreSQL byte escape"))
		}
		decoded = append(decoded, (raw[i+1]-'0')*64+(raw[i+2]-'0')*8+raw[i+3]-'0')
		i += 3
	}
	return decoded, nil
}
