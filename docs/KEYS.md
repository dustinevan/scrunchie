# Keyboard, mouse, and focus

scrunch follows the current selection: text keys edit text, arrows move through
cells or tree branches, Enter activates, and Escape backs out of the current
layer. Printable navigation commands apply only to databases, results, the
structure view, or the value inspector. Letters in SQL, filenames, searches,
history, and the command palette always belong to that input.

## Moving around

- In SQL, arrows move the cursor. Left at absolute position zero, with no
  selection, closes any completion popup and selects the buffer's database in
  the left pane. Without a connection it selects Add. Left at a later line's
  beginning moves to the preceding line. Down at the end of the buffer enters
  results.
- In results, arrows move cells. Up from the first row returns to SQL; Left from
  the first column selects the buffer's database. These exits also work with no
  results. Right and Down stop at the last column or row.
- In the database pane, Right always enters SQL at its remembered cursor.
  Enter/Space expands or collapses a branch; Down selects its visible children.
  Left collapses a branch, then selects its parent. In the JSON inspector,
  Right expands a branch, then enters its first child; arrows stay in the value.
- Shift with movement extends a selection within its pane. Plain movement
  clears a SQL or result selection before a later boundary movement can leave
  that pane. Home, End, word movement, and Page Up/Down also stay local.
- Enter inserts a newline in text. Outside text it opens a value, toggles a
  branch, or activates the selected control. Space inserts text in a field and
  activates nontext controls. Merely selecting a saved database never connects.
- Escape dismisses completion before clearing SQL selection. In databases it
  clears a filter, then returns to SQL. In results it clears a rectangle, then a
  row filter, then returns to SQL. In the inspector it clears a filter, then
  returns to results. In a dialog it cancels that dialog and restores the
  underlying view.

Each pane remembers its cursor or selected item. Click a pane, use F2/F5/F6,
or follow these boundary arrows to change focus. Small terminals show one pane
at a time while preserving the same routes.

Selection, scrolling, and pane movement do not execute editor SQL, connect a
saved database, or save a file. Expanding catalog branches can fetch metadata
from that connection's PostgreSQL session. Database writes require explicit SQL.

## Mouse

| Target | Behavior |
| --- | --- |
| Pane title or content | Click to focus it |
| SQL text | Click places the cursor; drag selects; Shift+click extends selection |
| SQL word or line | Double-click selects a word; triple-click selects a line |
| Completion suggestion | Click accepts it |
| Result cell | Click selects; drag makes a rectangle; Shift+click extends it; double-click inspects |
| Result row number / column header | Select the row / column in the current view; the header corner selects all cells |
| SQL or result tab | Click switches the active buffer or statement result |
| Add database | Click opens the form |
| Saved database | Single-click selects; double-click explicitly connects |
| Catalog, structure, or JSON tree | Click a label to select; click a disclosure arrow or double-click to open/close a container |
| Inspector or structure view Close control | Return to the previous pane |
| Dialog field | Click places the cursor; drag or Shift+click selects text, including masked passwords |
| Dialog list | Click selects; double-click accepts the visible item |
| Button or checkbox | Click activates that control |

The wheel scrolls the workspace pane under the pointer without changing focus
or its selection. Horizontal wheel events, or Shift+wheel when forwarded by the
terminal, scroll SQL, results, and inspected text horizontally. Keyboard movement
then reveals the remembered selection. Dragging past the SQL or result viewport
scrolls while extending that selection. A dialog owns mouse input while open;
clicks and wheel events cannot reach the workspace underneath it.

Terminal mouse forwarding varies over SSH and multiplexers. Keyboard navigation
remains available. To select the terminal's own text instead of the app's SQL or
cells, use the terminal's mouse override, commonly Shift. Copy from scrunch
remains explicit through Ctrl+C or `y`; selecting or scrolling does not copy
anything.

## Input ownership

```text
Key press
  -> Ctrl+C while this buffer's connection runs a request, or while connecting
     or listing databases: cancel
  -> Ctrl+Q: begin the quit flow
  -> active prompt or confirmation
  -> main shortcuts
  -> value inspector, structure view, completion list, or focused pane
```

A handled key reaches only one state. `Esc` closes a prompt and restores the
previous pane or inspector. A result arriving while a prompt is open does not
change focus. An open value inspector remains a snapshot of its original cell.

## Main screen

| Key | Action |
| --- | --- |
| `Ctrl+P` | Search commands; all application actions have palette entries |
| `Ctrl+B` | Open saved databases; with no saved entries, open Add database |
| `F1`, `F7` | Keyboard reference; arrows and Page Up/Down scroll |
| `F2` | Focus the SQL editor |
| `F5` | Focus results |
| `F6` | Focus and show databases, keeping their selected row |
| `Ctrl+Tab`, `Ctrl+Shift+Tab` | Next/previous pane where the terminal forwards these keys |
| `Ctrl+J` | Run selected SQL, or the statement at the cursor, on the buffer's connection |
| `Ctrl+Enter` | Alias for Run where the terminal forwards it |
| `Ctrl+C` | Cancel this buffer's running query or a connection attempt; otherwise copy |
| `Ctrl+Q` | Quit with unsaved SQL, transaction, and running-query checks |
| `F8` | Query history across connections |
| `F9` | Show or hide databases |
| `F10` | Maximize the focused pane, or restore the split layout |
| `Ctrl+F` | Find or filter in the active pane/inspector |
| `F3` | Find the next matching text, cell, catalog object, or JSON key/path; wrap at the end |

In the database and result panes, `Tab` and `Shift+Tab` move between panes.
In the editor they indent and dedent. Ctrl+Tab and Ctrl+Shift+Tab also cycle
panes from SQL or the inspector, with F2/F5/F6 and palette fallbacks for SSH.
Dialogs keep traversal inside their own fields, buttons, or lists.

The palette also provides Run entire buffer, Change buffer connection, Disconnect
this connection, Reconnect this buffer's connection, Allow writes and Make this
connection read-only, all file and buffer commands, result switching, sorting,
filtering, Copy as, Fetch more rows, export, format changes, table structure and
DDL, and full query details. Transaction commands prepare `BEGIN`, `COMMIT`, or
`ROLLBACK` in a new buffer; the user then runs them. Explain prepares
`EXPLAIN (FORMAT JSON)` for one statement and never adds `ANALYZE`.

## Safety

Connections open read-only: scrunch sets `default_transaction_read_only=on` when
it connects. The header shows a muted `read-only` label. When writes are on for
the current buffer's connection, the header turns hot pink and reads `WRITES ON`.
It also turns pink inside a transaction started with `BEGIN READ WRITE` or
changed with `SET TRANSACTION READ WRITE`, because that transaction can write.

| How | Effect |
| --- | --- |
| Connection form: Allow writes | Saved as `"allow_writes": true`; that database opens writable |
| Ctrl+P → Allow writes on this connection… | Confirms (default Cancel), then allows writes for this session |
| Ctrl+P → Make this connection read-only | New transactions on this connection are read-only again |
| `--write` | Command-line and headless connections open writable |

Both palette commands are refused while a query runs or a transaction is open
or failed, and on a hot standby. Reconnect never turns writes on: it reopens a
connection read-only unless both its settings and its current mode allow
writes. A write refused because the connection is read-only explains how to
allow writes.

Before anything is sent, `UPDATE` or `DELETE` without a `WHERE` at their own
level, `TRUNCATE`, and `CREATE TABLE … AS` with such a data-modifying `WITH` open
a "Change every row?" dialog. It names the table and line, starts on Cancel,
and Esc or × cancels. This applies to the statement at the cursor, selected SQL,
and Run entire buffer, in every write mode. SQL that cannot be checked asks too.
Confirming re-checks that the buffer and connection are unchanged; otherwise
nothing runs. Headless runs refuse such SQL unless `--yes` is given. The check
does not see inside `DO` blocks, routine bodies, `EXECUTE`, `MERGE`, or `DROP`.

Ctrl+J chooses the statement like this:

- A cursor inside a statement, or right after its `;`, runs that statement.
- A cursor on a blank line after a statement runs that statement.
- A cursor on a comment line between statements runs the statement it
  introduces.
- A cursor before the first statement runs the first.
- `BEGIN ATOMIC … END` routine bodies and parenthesized rule actions stay part
  of their `CREATE` statement.

After a run, the status line starts with `Line N ·`.

## Databases and connection form

Plain `scrunch` starts with empty SQL and no database connection. The editor is
usable before connecting. The left pane has a pinned Add database item, each
open connection with its catalog, and saved connections that are not open.
Enter or Space on Add opens the form, and a click does the same. `Ctrl+B` opens
the database manager; the command palette also offers Databases and Add
database when SSH or a multiplexer intercepts the shortcut. Loading saved
entries and moving through them never contacts a server.

| State | Key | Action |
| --- | --- | --- |
| Database list | Up/Down, Tab/Shift+Tab | Select a saved entry or Add database |
| Database list | Home/End, Page Up/Down | Move through the list |
| Database list | Enter, Space | Open the entry as another connection, switch to it if `[open]`, or open Add database |
| Database list | `n`, `e` | Add an entry or edit the selected entry |
| Database list | `d`, Delete | Confirm removal of local saved settings |
| Database list | Esc, Ctrl+B | Return to the workspace |
| Connection form | Printable characters | Edit only the focused field |
| Connection form | Tab/Shift+Tab | Cycle to the next/previous field or button |
| Connection form | Down/Up | Next/previous control; stop at the first/last |
| Connection form | Enter | Advance from a field or checkbox, or activate the focused button |
| Connection form | Space | Type in a field, toggle Remember or Allow writes, or activate a focused button |
| Connection form | Left/Right on buttons | Select Connect, Save, Cancel, or List databases |
| Connection form | Esc | Discard form edits and return to the list or workspace |
| Connecting | Ctrl+C, Esc | Request connection cancellation |
| Database-name picker | Arrows, Enter/Space | Choose a name to fill Database; does not connect it |
| Database-name picker | Esc | Return to the connection form |
| Remove confirmation | Arrows/Tab, Enter/Space | Select and accept; defaults to Cancel |

Fields include name, host, port, database, user, password, password environment
variable, TLS mode, and optional certificate/key paths, followed by the Remember
and Allow writes checkboxes. The password environment field contains a variable
name, such as `REPORTING_DB_PASSWORD`; its value is read only when Connect,
Reconnect, or List databases is requested. Enter either a literal password or an
environment-variable name. Literal passwords are masked, cannot be copied or
cut, and never appear in saved settings. Native terminal paste works in the
focused field. Pane, Run, and palette shortcuts cannot escape from the form;
Ctrl+Q still starts the quit flow.

Remember is initially checked; Allow writes is not. Connect opens the specified
database as another connection and, on success with Remember checked, writes
only its nonsecret settings to `~/.scrunch`. Connecting a saved entry that is
already open switches to it instead of opening a duplicate. Save writes settings
without connecting; saving an open entry relabels it, and changed connection
settings apply at its next Reconnect. Removing an entry removes only local
settings, not the PostgreSQL database, and does not disconnect an open
connection. List databases explicitly opens a temporary read-only session to the
`postgres` database on the form's host with its user and authentication
settings. It closes that session after listing connectable databases. Choosing
a name only fills the Database field; Connect remains a separate action. Ctrl+C
or Esc cancels listing. A connection error leaves the form open and every open
connection as it was. One connection attempt runs at a time; queries on open
connections continue meanwhile. Settings saves finish before the app can quit.

An unreadable or invalid settings file is reported rather than overwritten.
Connections remain available with Remember off while that file is repaired.

## Connections and buffers

Each SQL buffer belongs to one open connection, or to none. Its tab reads
`SQL n · <connection>` and shows a spinner while that buffer's query runs. The
header shows the current buffer's connection: name, write mode, transaction
state, a lost connection, and a note when another connection is running a query.

- A newly opened connection takes the current buffer if that buffer is empty,
  clean, and has no results. Otherwise it gets a new buffer, which becomes
  current.
- Ctrl+N and Open file use the current buffer's connection. Preview, Count,
  structure DDL, and other catalog-prepared SQL use the selected row's
  connection. History reopens SQL on the connection that ran it, if still open.
- Ctrl+P → Change buffer connection… lists open connections and No connection.
  It is refused while that buffer's query runs.
- Each buffer keeps its own results, cursor, display formats, sort, and filter.
  Results from a connection that has since closed are labeled as such. Fetch
  more works only on the connection that produced the results.
- Each connection runs one query at a time. Different connections run at the
  same time; when another buffer's query finishes, the status line says so.
  Catalog, structure, and completion reads wait only for their own connection.
- Run on a buffer without a connection explains how to choose one.

## SQL editor

| Key | Action |
| --- | --- |
| Printable characters, `Enter` | Insert text or a newline |
| Arrows | Move the cursor; plain Left at buffer start enters databases, Down at buffer end enters results |
| `Shift` + movement | Extend selection |
| `Home`, `End` | Beginning/end of line |
| `Ctrl+Home`, `Ctrl+End` | Beginning/end of buffer |
| `Ctrl+Left/Right`, `Alt+Left/Right` | Move by word |
| `Page Up/Down` | Move by a page |
| `Tab`, `Shift+Tab` | Indent/dedent; selected lines move together |
| `Backspace`, `Delete` | Delete selected text or one character |
| `Ctrl+Backspace`, `Alt+Backspace` | Delete the preceding word |
| `Ctrl+K`, `Ctrl+U` | Delete to end/beginning of line |
| `Ctrl+A` | Select the whole buffer |
| `Ctrl+C`, `Ctrl+X` | Copy/cut selected text when no query is executing |
| `Ctrl+Z`, `Ctrl+Y` | Undo/redo; `Ctrl+Shift+Z` also redoes |
| `Ctrl+N` | New buffer on the current buffer's connection |
| `Ctrl+O` | Open a local SQL file in a new buffer |
| `Ctrl+S` | Save; request a path for an unnamed buffer |
| `Ctrl+W` | Close the buffer, checking unsaved changes; refused while its query runs |
| `Ctrl+Page Up/Down` | Previous/next buffer |
| `Ctrl+G` | Go to a line number |
| `F4` | Format selection or buffer; errors leave SQL intact |
| `Ctrl+/`, `Ctrl+_` | Toggle line comments |
| `Shift+Delete` | Delete the current line |
| `Ctrl+Space`, `Alt+/` | Request completion |
| `Esc` | Dismiss completion or clear SQL selection |

Completion reads the statement around the cursor:

- After `FROM`, `JOIN`, `UPDATE`, `INTO`, `TABLE`, `USING`, and `TRUNCATE` it
  offers tables, schemas, and `WITH` names.
- In `SELECT`, `WHERE`, `ON`, `SET`, `GROUP BY`, `ORDER BY`, `RETURNING`, and
  column lists it offers the columns of the statement's tables first.
- `alias.` offers that table's columns, `schema.` its tables, and
  `schema.table.` its columns. Tables named later in the statement count.
- Each suggestion shows a hint such as `column · numeric`, `table`, `view`,
  `schema`, `alias`, `cte`, or `keyword`. Keywords follow the case you type.

Typing a letter, `_`, or `.` after a name opens the popup automatically after two
characters, or right after a dot. It never opens inside strings or comments, or
when the word typed is the only exact match. Up/Down, Shift+Tab, and Page Up/Down
select; Tab or a click accepts; Esc dismisses. Enter accepts, except when the
popup opened by itself and the word typed already equals the selected
suggestion; then Enter closes it and inserts the newline. The first Esc leaves
an existing SQL selection intact. Names are inserted bare when simple lowercase
and not reserved, otherwise double-quoted; tables outside `search_path` are
schema-qualified. Completing `public.us` replaces only `us`.

Catalog names for completion load on first use while the connection is idle,
columns load as needed, and both wait behind running SQL. Names refresh after
DDL, `SET`, or `ROLLBACK`, or after five minutes. Inside a transaction only
Ctrl+Space reads the catalog, so typing cannot disturb it.

Paste uses the terminal's native bracketed-paste event. A literal `Ctrl+V`
does not read an operating-system clipboard. CRLF and bare CR line endings are
normalized to LF. Undo retains at most 100 snapshots and 32 MiB of text.

## Database pane and catalog

Up/Down visits Add, each open connection and its visible catalog objects, then
saved connections that are not open. An open connection reads `● name`, with
`[writes]` when writable, `[tx]` or `[tx failed]` during a transaction, and
"disconnected" when its session was lost. Saved entries read `○ name`. Home
selects Add; End selects the last visible item. Add remains pinned while the
tree scrolls. `+` or `n` opens Add from any row, and `e` edits a selected saved
connection. Removing saved entries is available in the Ctrl+B manager; Delete in
the pane does not delete databases or catalog objects.

Enter/Space on a saved connection explicitly connects it. On another open
connection they switch to its most recently used buffer, opening one if needed,
and keep focus in the pane. On the current connection they toggle its catalog;
if its connection was lost, they explicitly reconnect. Right from any row
enters SQL at its remembered cursor. Cached objects remain browsable after
connection loss, and unloaded metadata requests explain that reconnection is
needed.

Each schema lists tables and views first, with muted kind suffixes such as
`view`, `matview`, `foreign`, and `partitioned`. Then come `functions N` and
`sequences N` folders, shown only when not empty.

| Key | Action |
| --- | --- |
| Up/Down, Home/End, Page Up/Down | Navigate visible objects |
| Right | Move straight to SQL from any row, keeping its cursor and selection |
| Left | Collapse, then move to the parent |
| `Enter`, `Space` | Toggle containers, or show column, function, or sequence details |
| `d` | Structure of a table, view, or a column's table; DDL of a function or sequence |
| `x` | Disconnect the selected connection, asking first if that rolls back or cancels work |
| `i` | Insert the identifier and focus the editor |
| `p` | Prepare a `SELECT ... LIMIT 100` buffer |
| `.` | SELECT, structure, DDL, count, and identifier actions |
| `/`, `Ctrl+F` | Filter loaded names and column metadata across every connection |
| `Esc` | Clear the catalog filter first; otherwise return to SQL |
| `Ctrl+R` | Refresh the selected row's connection catalog |
| `Ctrl+C` | Copy the identifier when idle |

While a filter is active, matching ancestors remain open. Enter/Space
enters the first visible child, and Left selects the parent. Clearing the filter
keeps the selected object when it remains visible. A filter with no matches
still leaves the databases and Add reachable. A new filter selects its first
matching object; F3 advances between actual matches, skipping ancestor-only rows.

Schemas, relations, functions, and columns load asynchronously. Expansions
requested while SQL or a refresh runs wait with a spinner and load afterward.
Collapsing a loading branch keeps it collapsed when its metadata arrives.
Catalog work waits while its connection executes SQL. Starting SQL waits for an
existing catalog request without automatically canceling it; canceling a
PostgreSQL request inside a transaction can abort that transaction.

## Structure view

`d` opens a read-only view of the selected table: columns with types, nullability,
defaults, identity or generated expressions, and comments; primary key and
unique constraints; foreign keys and the tables that reference this one;
indexes with size; check constraints; triggers; and the table comment, owner,
estimated row count, and total size. Views also show their definition. Row
counts are PostgreSQL's estimate. Sizes are skipped inside a transaction because
reading them can wait on locks. The view needs PostgreSQL 12 or later.

| Key | Action |
| --- | --- |
| Up/Down, Home/End, Page Up/Down | Move between lines |
| Enter/Space | Toggle the selected section |
| Left | From an item, select its section; on a section, collapse it |
| Right | Expand a section, then enter its first item |
| Alt+Left/Right | Scroll horizontally |
| `/`, `Ctrl+F`, `F3` | Find text, including inside collapsed sections |
| `Ctrl+C` | Copy the line, or a whole section from its header |
| `D` | Open generated DDL in a new buffer |
| `Ctrl+R` | Reload |
| `Esc` | Return to the database pane at the same row |

Generated DDL opens in a new buffer that is never run automatically and is not
marked unsaved. It covers columns, constraints, indexes, triggers, comments,
partitioning, and storage options. It omits data, ownership, privileges, and
row-security policies. If you typed or clicked while it loaded, it opens behind
the current buffer.

## Results

| Key | Action |
| --- | --- |
| Arrows | Move one cell; plain Up at the first row enters SQL, Left at the first column enters databases |
| `Shift` + movement | Extend a rectangular selection |
| `Ctrl+A` | Select all cells in the current view |
| `Esc` | Clear the rectangle, then the row filter; otherwise return to SQL |
| Home/End, Ctrl+Left/Right | First/last column in the row |
| Ctrl+Up/Down | First/last row |
| Ctrl+Home/End | First/last cell |
| Page Up/Down | Page through rows |
| `[`, `]`, Ctrl+Page Up/Down | Previous/next statement result |
| `Enter`, `Space` | Inspect the selected value |
| `s` | Sort by the column: ascending, descending, original order |
| `f` | Filter rows by displayed text; `column:text` matches one column |
| `y` | Copy as TSV, CSV, Markdown, JSON, SQL `INSERT`, or SQL `IN` list |
| `m` | Fetch more rows by re-running a paged query |
| `v` | Cycle auto, text, JSON, hex, base64, UUID for the column |
| `+`, `-` | Widen/narrow the column; `=` also widens |
| `Ctrl+C` | Copy the cell, or the selection as tab-separated values |
| `Ctrl+E` | Export the current view to an explicit `.csv` or `.json` path |
| `/`, `Ctrl+F`, `F3` | Search bounded cell previews, then find next |

A single `SELECT`, `VALUES`, `TABLE`, or read-only `WITH` query fetches its first
1,000 rows, and PostgreSQL stops there. The tab line says when more are
available. `m` re-runs it from the start for ten times as many rows, up to
`--max-rows`, in the current transaction state. Functions in its select list run
only for the rows fetched, again on each re-run. Multi-statement runs, data-modifying
`WITH`, `SELECT … INTO`, `FOR UPDATE`/`FOR SHARE`, and failed transactions are
not paged.

Sorting is client-side, stable, and type-aware: numbers compare exactly, dates
and timestamps by time, and NULLs come last ascending and first descending. The
header shows ↑ or ↓. On a partial result the status says only fetched rows were
sorted. Filtering is a case-insensitive substring match on what each cell
displays, so it honors `v` formats. An empty filter clears it. Row numbers show
positions in the view; the inspector title also gives the original row. The
view resets when a new result arrives or you switch statement results, but
fetching more rows keeps it.

Copy as uses the rectangle when there is one, otherwise the current row; the
`IN` list uses the cursor's column and skips NULLs. Copied values are
PostgreSQL text, not the display format. `INSERT` names the table only for a
simple `SELECT … FROM table` or `TABLE name`; otherwise it writes `table_name`.
Ctrl+C on a single SQL NULL cell copies empty text and says so; rectangles write
`\N`, and quote a text value that is literally `\N`.

Display formats preserve original values. Invalid interpretations show an
error label. SQL NULL, empty text, empty bytea, and JSON null remain distinct.
Large values have bounded grid previews; the inspector and explicit copy work
from their original values. Changing result sets clears rectangular selection.

## Value inspector

JSON objects and arrays display as a tree. Other values display as a scrollable
text preview. The title identifies the value as a snapshot.

| Key | Action |
| --- | --- |
| Up/Down, Home/End, Page Up/Down | Navigate visible rows |
| Right | Expand a JSON container, then enter its first child |
| Left | Collapse a JSON container, then move to its parent |
| Enter/Space | Toggle the selected JSON container |
| `e`, `c` | Expand/collapse all JSON containers |
| `/`, `Ctrl+F` | Filter JSON keys and paths, keeping ancestors visible |
| `F3` | Select the next matching key or path, wrapping at the end |
| `Ctrl+C` | Copy the selected JSON subtree or displayed value format |
| `p` | Copy the selected JSON path |
| `v` | Change the inspector's display format |
| Alt+Left/Right | Scroll horizontally; plain Left/Right also scroll text, stopping at the longest line |
| `Esc` | Clear a filter, then return to results at the originating cell |

Filtering selects the first actual matching key or path and keeps its ancestors
visible. While filtered, Left selects a parent; Right, Enter, or Space enters
the first visible child. Expand/collapse-all leaves the saved expansion intact
and explains how to clear the filter. Escape restores ordinary tree navigation,
keeping the selected node or its nearest visible ancestor.

Text previews show up to 64 KiB. JSON parsing has explicit size, depth, and node
limits. Copy has a 4 MiB limit and does not silently truncate; export is the
alternative for larger retained values.

If another query finishes while an inspector is open, its value stays a
snapshot and the results pane receives the new table. Leaving the inspector
then shows that new table.

## Prompts, execution, and leaving

Palette, history, file paths, search, filter, and go-to-line prompts own
printable characters. Up/Down select list items; Page Up/Down moves by the
visible list height. Tab/Shift+Tab stays within that prompt. Enter accepts and
Esc cancels. History covers every connection and labels each entry; its filter
also matches connection names. History Enter opens SQL in a new buffer and
never executes it. History remains in memory, capped at 100 queries and 32 MiB.

Help and full-message views scroll with arrows, Home/End, and Page Up/Down.
Space pages forward, Shift+Space pages back, and Enter closes. A dialog's visible
× close control has Escape's behavior. Pending file or settings writes finish
before a dialog can close; connection and discovery operations can be canceled.

Confirmation dialogs use arrows/Tab and Enter/Space, start on Cancel/Stay, and
have no single-letter destructive shortcuts. New save paths and exports confirm
replacement of existing files. Ctrl+S on an existing buffer path saves normally.
A failed save leaves the buffer dirty and reports the file error.

While a buffer's query runs, repeated Run on that connection does nothing.
Ctrl+C requests cancellation and never quits. If Run is waiting on catalog work,
Ctrl+C cancels only the queued SQL. Success selects results only if the editor
still has focus and no key, click, wheel, or paste interaction occurred after
Run. A failed run that produced no results replaces earlier results with the
error, its SQLSTATE, line and column, detail, and hint; a failed batch still
shows the results that completed before the error. PostgreSQL error positions
select the corresponding cursor location when the buffer has not changed and
no such interaction intervened. The status line ends with `…` when it is too
long; Ctrl+P → Read last status or error shows the full text and any notices.

Quit checks each dirty buffer; prepared SQL such as previews and DDL starts
clean and needs no answer. Save, Discard, and Cancel are explicit. Then one
dialog lists every connection with an open or failed transaction or a running
query; confirming rolls back those transactions, cancels those queries, and
closes every connection. Reconnect is explicit, applies to the current buffer's
connection, creates a new session, and never replays SQL.

Clipboard copy emits terminal OSC52 only when requested. Support depends on
the terminal and SSH multiplexer; scrunch cannot verify delivery without reading
the clipboard, which it never does. No clipboard executable or external editor
is required. Query history and results are never written automatically.
