# Architecture

```text
app/scrunch        flags, process lifetime, headless SQL, startup
app/tui            keyboard states, connections, editor, catalog, grid, inspector
    ↓
lib/data/postgres  one PostgreSQL session: execution, paging, catalog, structure
lib/data/connections named settings, password environment references, ~/.scrunch
lib/data/files     explicit SQL files and result exports
lib/logic/cell     reversible display formats, type-aware sorting, copy formats
lib/logic/jsontree JSON structure, expansion, paths, precision
lib/logic/sqledit  SQL tokens, formatting, completion context, write checks
    ↓
pkg/result         independent result structs
pkg/sqltext        independent SQL statement scanner
pkg/termtext       independent terminal text escaping
```

A `postgres.Session` owns one connection and serializes access to it. The TUI
holds any number of open connections, each with its own session, catalog tree,
completion cache, write mode, transaction state, and at most one running
request. Requests on different connections run concurrently. Every request
carries a model-wide serial and its connection id; a reply for a closed
connection or a superseded request is dropped. Each SQL buffer is bound to one
connection or none and owns the results of its last run, including cursor,
display formats, and the sorted or filtered row view.

Sessions open with `default_transaction_read_only=on` as a startup parameter
unless writes are allowed, so `RESET ALL` and `DISCARD ALL` return to read-only.
After each operation the session reads the server-reported setting and the hot
standby flag. Inside a transaction, or on servers that do not report the
setting, it asks the server with `SHOW transaction_read_only`. The UI shows
writes as on whenever the current transaction could write. Reconnect never
turns writes on.

Before the TUI sends SQL, `sqledit.Unfiltered` checks exactly that text for
`UPDATE`/`DELETE` without `WHERE` at their own depth and for `TRUNCATE`, and the
UI asks for confirmation. Tokenizer failures count as findings. Statement
boundaries for Ctrl+J follow psql's lexer, including parentheses and
`BEGIN ATOMIC … END` routine bodies.

A single plain read run from the TUI is paged: the session sends
`DECLARE <cursor> NO SCROLL CURSOR FOR <stmt>; FETCH FORWARD n+1; CLOSE` in one
simple-protocol round trip, with a per-session random cursor name. PostgreSQL
computes only the page, and outside a transaction nothing stays open. Error
positions are mapped back to the submitted text. All other SQL, and all headless
SQL, is sent unchanged with the simple protocol, which preserves multiple result
sets, PostgreSQL textual types, and transaction boundaries. Reaching a display
limit then only stops retaining rows, while execution drains. Query
cancellation is explicit. Connection loss never causes an automatic replay or
reconnect.

Catalog, structure, and completion reads run on their connection's session
only while it is idle. They are written not to raise errors, and they skip
lock-sensitive reads inside a transaction, so they cannot abort the user's
transaction. Completion analysis in `sqledit` is UI-free: it finds the word,
its qualifier, the clause, and the statement's table references, including
ones after the cursor.

Plain startup loads saved connection settings without opening a session.
The connection form owns all input until dismissed; Connect and List databases
are explicit network actions. Password environment references are resolved for
each connection attempt. Save writes only typed, nonsecret settings to an
owner-only JSON file. An invalid existing file is reported and left intact.
Opening a connection never touches the others; a failed attempt changes
nothing. Explicit database-name discovery uses a separate, temporary read-only
connection to `postgres`.

The UI owns context cancellation and only applies asynchronous results on its
event loop. SQL NULL is distinct from empty text, JSON null, and an empty bytea.
Raw values remain unchanged when display formats change. JSON numbers retain
their original decimal text. JSON object order follows the server's text.
Terminal control characters in database data, errors, names, and paths are
rendered visibly, never executed as terminal commands.

The binary makes no network requests except explicitly selected PostgreSQL
connections and PostgreSQL cancel connections. DNS resolution and authentication
belong to that connection. No query history or result data is written without
an explicit save/export action. No external editor or clipboard executable is
required. `--demo` runs without a database.

Vendored changes remove Charm's inherited trace/panic logging and external
tmux color probe. `make vendor` reapplies `docs/vendor-privacy.patch`, and all
Makefile builds explicitly select the vendored dependencies. Connection
attempts retain ownership of a newly opened session until the event loop
accepts it; shutdown also closes sessions whose completion message was lost.
