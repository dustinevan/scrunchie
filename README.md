<p align="center">
  <img src="docs/assets/scrunchie-mascot.png" width="260" alt="The scrunchie mascot: a puffy hot-pink scrunchie with a smiling face in its hole">
</p>

<h1 align="center">scrunchie</h1>

<p align="center">
  <b>A rad PostgreSQL IDE for your terminal that totally gets you.</b><br>
  Just start typing. There's no keymap to learn: press what you'd press in vim,<br>
  psql, your editor, or a spreadsheet, and scrunchie does that thing, right where you are.
</p>

<p align="center">
  <img src="docs/assets/demo.gif" alt="scrunchie running its offline demo: SQL with neon syntax colors, a results grid, a JSON tree inspector, and bytea display formats">
</p>

scrunchie browses catalogs, edits and runs SQL, and navigates typed results,
including bytea and JSON. It's one Go binary, `scrunch`, that runs on your
laptop or over SSH and talks only to the databases you connect it to. It's built
with [Charm](https://charm.sh)'s Bubble Tea v2 and Lip Gloss v2, and
[pgx](https://github.com/jackc/pgx) for the PostgreSQL wire protocol.

## Get it

```sh
git clone https://github.com/dustinevan/scrunchie
cd scrunchie
make demo      # offline sample data, no database needed
make run       # the real workspace
make install   # put scrunch on your PATH
```

Build from the repository rather than `go install …@latest`. The Makefile
builds from the vendored dependencies, which carry
[a small patch](docs/vendor-privacy.patch) to Charm that `go install` would
skip. `make list` shows every command.

## Just start typing

You already know how to use scrunchie. Every place in the app (the SQL editor,
results, the database tree, the value inspector, help) knows what you can do
there and the keys people reach for from other tools. Press the one your hands
expect:

| You press | Because you know | scrunchie |
| --- | --- | --- |
| A table's name, in the database tree | Every list UI | Filters the tree as you type |
| `j` `k` `h` `l`, `g` `G` | vim, less | Moves through results, JSON, and help |
| `q` | vim, less, man | Backs out one level, and never quits |
| `y` in the inspector | vim | Copies the value |
| `F5`, `Cmd+Enter` | SSMS, pgAdmin, DataGrip | Runs the SQL at the cursor |
| `Shift+Space`, `Ctrl+Space` | Spreadsheets | Selects the row or column |
| `Ctrl+R` in SQL | Your shell | Searches query history |
| `\d users`, `\dt`, `\l`, `\q` | psql | Opens the table's structure, filters the tree, lists databases, quits |
| `?` | Most TUIs | Shows what you can do right here |

When it's ambiguous, scrunchie doesn't guess. Ctrl+D means half-page in vim,
exit in psql, and delete in editors, so it does nothing and the status line
tells you the key for each meaning: "page down is PgDn · quit is Ctrl+Q". Outside
of text you're typing, the same goes for any key that means nothing where you
are: you get a one-line hint instead of silence.

Guessing is safe:

- Repeating a guess is harmless. `g` goes to the top, so vim's `gg` does too.
  Mashing `q` backs you out and never types a `q` into your SQL.
- Nothing costly (running SQL, quitting, connecting, disconnecting, writing a
  file) ever happens on a key that could mean something else.
- psql commands never reach the server, where they'd only be a syntax error.

Press `?` in any pane outside the SQL editor, or F1 anywhere, to see everything
you can do there, including the keys that deliberately do nothing. A single
table drives the keys, that help screen, and a test that checks each habit
against the key it stands for, so the help always matches what the keys do.
The full table and rules are in [keyboard and mouse behavior](docs/KEYS.md).

## First launch

`make run` builds `bin/scrunch` and opens the workspace. `make demo` opens
sample data without a database.

The first launch opens a blank, disconnected workspace. Click **+ Add database**
in the left pane, or select it with the arrows and press Enter or Space. **Ctrl+B**
also opens the connection manager. Named connections are remembered in
`~/.scrunch`; activate one inside the app when you want to connect. Opening the
app never automatically connects to saved databases or to a target from your
environment.

`~/.scrunch` is an owner-only JSON file managed by the app. It stores connection
names, host, port, database, user, TLS settings, whether writes are allowed, and
an optional password environment-variable name. It never stores passwords, SQL,
history, or results. The file is created only by Save or by Connect with
Remember checked.

For example, the form's **Password env** field can contain `WORK_DB_PASSWORD`.
scrunch reads it from its process environment when you connect or reconnect and
reports a missing variable without connecting. Leave the ordinary Password field
empty when using an environment reference. A password typed into the form stays
in memory and is masked. PostgreSQL's usual password-file behavior is also
supported.

**List databases** uses the form's host and credentials to open a temporary,
read-only connection to `postgres` and list databases you can access. Choosing a
name fills the Database field; Connect then opens that database.

Command-line connections remain available:

```sh
bin/scrunch --demo
PGHOST=localhost PGDATABASE=app bin/scrunch --connect
bin/scrunch 'postgres://user@localhost/app?sslmode=disable'
bin/scrunch --write 'postgres://user@localhost/app?sslmode=disable'
```

Put command options before a positional connection string. Headless commands
continue to use PostgreSQL environment settings when no connection string is
provided.

## Safe by default

- **Connections open read-only.** scrunch starts every session with
  `default_transaction_read_only=on`. The header looks normal and shows a small
  `read-only` label. Allow writes per saved connection with the form's **Allow
  writes** checkbox, for one session with Ctrl+P → **Allow writes on this
  connection…**, or on the command line with `--write`. While writes are on,
  the whole header turns hot pink and reads **WRITES ON**. Reconnect never turns
  writes back on.
- **Read-only is a guard against accidents, not a permission boundary.** A
  `BEGIN READ WRITE` or `SET` can still allow writes. The header follows what
  the server reports, so it turns pink inside such a transaction too. Use
  database roles and grants to restrict what an account may do.
- **Changes to every row ask first.** `UPDATE` or `DELETE` without a `WHERE` at
  their own level, and `TRUNCATE`, open a "Change every row?" dialog before
  anything is sent, whatever the write mode. Cancel is the default. This also
  covers data-modifying `WITH` queries and `CREATE TABLE … AS` with one. SQL that
  cannot be checked asks too. The check does not look inside `DO` blocks,
  functions, procedures, `EXECUTE` of a prepared statement, `MERGE`, or `DROP`.
- **Ctrl+J runs the statement you are on.** A cursor inside a statement or
  right after its `;` runs that statement. A blank line below it also runs it;
  a comment line introducing the next statement runs the next one. Function and
  procedure bodies written with `BEGIN ATOMIC … END` stay whole. The status line
  starts with the line number of what ran.

## Data stays where you put it

- Runtime network activity is limited to the PostgreSQL endpoints you select,
  their DNS resolution, and PostgreSQL cancellation requests.
- There is no telemetry, update check, HTTP client, AI integration, or remote
  asset download in the application.
- Query buffers, query history, and results stay in memory. These are never
  automatically restored, logged, or saved to disk.
- Saving connections, SQL, and result exports requires an explicit action.
  New saved files use owner-only permissions (`0600`). Failed writes leave the
  original intact and report the path of any recovery file.
- Copy is explicit and uses the terminal's OSC52 clipboard protocol. Paste is
  the terminal's native bracketed paste. No clipboard executable is required.
  Over SSH, OSC52 places copied values on your local machine's clipboard.
- Database text and errors cannot inject terminal escape sequences into the UI.

Your terminal receives what you display; its scrollback, clipboard, and the host
OS's memory handling remain under your terminal and OS policies. TLS behavior
comes from your PostgreSQL connection settings; use `sslmode=verify-full` and
your approved CA when the database connection requires certificate verification.

## Workspace

```text
┌ databases ─────────────┬ SQL 1 · app   SQL 2 · reporting ─────────┐
│ + Add database         │ select * from public.events;             │
│ ▾ ● app                │                                          │
│   ▾ public             ├ results ─────────────────────────────────┤
│     ▸ events           │ id │ payload (jsonb) │ data (bytea)       │
│ ▸ ● reporting [writes] │                                          │
│ ○ saved database       │                                          │
└────────────────────────┴──────────────────────────────────────────┘
```

Arrows follow the thing you selected: characters in SQL, cells in results, and
parents or children in a tree. Left at the very beginning of SQL moves to the
buffer's database in the left pane. Right from any database-pane row returns
to SQL at its remembered cursor. Enter/Space expands or collapses catalog
branches, and Down selects their visible children. Down at the end of SQL enters
results; Up from the first result row returns to SQL. Left from the first result
column returns to databases. Shift with movement extends selection within its
pane.

Enter inserts a newline in SQL, opens a selected result value, or activates a
selected database. Space types a space in text and activates buttons, checkboxes,
and tree items. Escape clears the current selection, filter, or popup before
leaving its view. A saved database is connected only when activated; arrow
movement and a single click simply select it.

Click to focus and place the SQL cursor or select a cell. Drag to select SQL or
a rectangle of results; Shift+click extends a selection. Double-click a SQL word
to select it, triple-click for its line, and double-click a result to inspect it.
Result row numbers and column headers select whole rows or columns.
SQL and result tabs are clickable. The wheel scrolls the pane under the pointer
without moving its selection or taking focus. Dialog fields, list items,
buttons, and checkboxes also accept mouse input.

| Action | Key |
|---|---|
| Open the database manager | `Ctrl+B` |
| Run SQL at the cursor or selected SQL | `Ctrl+J` or `F5` (`Ctrl+Enter`, `Cmd+Enter` when the terminal forwards them) |
| Editor / databases | `F2` / `F6` |
| Commands / what you can do here / history | `Ctrl+P` / `?` or `F1` / `F8` or `Ctrl+R` |
| Complete a name | `Ctrl+Space` (also automatic while typing) |
| Inspect a result cell | `Enter` |
| Sort / filter / copy results as… | `s` / `f` / `y` in results |
| Fetch more rows | `m` in results |
| Filter the database tree | Just type a name |
| Row actions: structure, DDL, SELECT, edit, disconnect | `.` in databases |
| Change bytea column format | `v` in results |
| Cancel a running query | `Ctrl+C` |
| Copy when idle | `Ctrl+C` |
| Open / save SQL | `Ctrl+O` / `Ctrl+S` |
| Export results | `Ctrl+E` |
| Quit | `Ctrl+Q` |

The command palette supplies alternatives to terminal-dependent shortcuts.
Tab indents SQL; in the database and result panes it moves between panes.
Ctrl+Tab cycles panes when the terminal forwards it. Native terminal text
selection usually needs your terminal's override modifier, often Shift, because
scrunch handles ordinary mouse selection. See
[keyboard and mouse behavior](docs/KEYS.md).

### Several databases at once

Activating a saved database opens it as another connection; the ones already
open keep their sessions, transactions, catalogs, and results. Each SQL buffer
belongs to one connection, shown in its tab and in the header, and Ctrl+P →
**Change buffer connection…** moves it. Queries on different connections run at
the same time, one per connection, and each buffer keeps its own results.
Ctrl+C cancels the current buffer's query. `.` → Disconnect on a connection in
the database pane disconnects it, asking first when that would roll back a
transaction or cancel a query. Quit lists every connection with open work
before closing.

### Completion

Completion reads the statement around the cursor. After `FROM`, `JOIN`,
`UPDATE`, and similar words it offers tables, schemas, and `WITH` names. In
`SELECT`, `WHERE`, `ON`, `SET`, and the like it offers the columns of the
statement's tables first. `alias.` offers that table's columns, and tables named
later in the statement count. Names are inserted bare when PostgreSQL allows
it and quoted otherwise. Tab accepts; Enter accepts unless the word you typed
already matches, so Enter at the end of a word still starts a new line.
Catalog names load on first use while the connection is idle, and inside a
transaction only an explicit Ctrl+Space reads the catalog.

### Results

A single `SELECT`, `VALUES`, `TABLE`, or read-only `WITH` query run from the
workspace fetches its first 1,000 rows, and PostgreSQL stops there. Press `m` to
re-run it for ten times as many rows, up to `--max-rows`. Functions in the select
list run only for the rows fetched. Other SQL runs as written and is read to the
end. scrunch keeps up to `--max-rows` rows and `--max-bytes` per run, 10,000
rows and 32 MiB by default.

`s` sorts by the current column, cycling ascending, descending, and original
order. Sorting is type-aware and puts NULLs where PostgreSQL does, but it only
reorders the rows already fetched. `f` filters rows by their displayed text;
`column:text` limits the match to one column. `y` copies the selection, or the
current row, as TSV, CSV, Markdown, JSON, SQL `INSERT` statements, or an SQL
`IN` list. Copy, inspection, and export follow the sorted and filtered view.

Bytea values retain their original bytes. Automatic display chooses JSON,
printable UTF-8, or hex; you can choose text, JSON, hex, base64, or UUID per
column. UUID is explicit because arbitrary 16-byte values are not necessarily
UUIDs. SQL NULL, empty strings, empty bytes, and JSON null remain distinct.

The JSON inspector keeps number precision and lets you open or close individual
objects and arrays, expand or collapse everything, find keys, and copy a value
or its path. It preserves duplicate JSON keys rather than overwriting them.

### Database browser

Each open connection has its own tree: schemas, then tables and views marked by
kind, then `functions` and `sequences` folders, then columns. Branches load as
you open them and wait while that connection runs SQL. Typing a name filters the
tree as you type. `.` → Structure on a table or view, or `\d name` run from
SQL, opens its structure: columns, keys, foreign keys in both directions,
indexes, checks, triggers, comments, an estimated row count, and size. `D`
there, or `.` → DDL on a function or sequence, opens generated DDL in a new
buffer. Generated SQL is never run for you, and quitting does not ask to save
it.

## SQL sessions and limits

Each connection is one persistent session. Temporary tables, `SET`, and
explicit `BEGIN` / `COMMIT` / `ROLLBACK` work across executions. The header
shows whether the current buffer's connection is in a transaction or failed.
Catalog requests use the same session and wait while its query runs.

Statements other than paged reads are sent as written. When a run keeps its row
or byte budget, the remaining rows are read and discarded without rewriting or
canceling the SQL. This preserves the outcome of statements with writes and
`RETURNING`. A single protocol row can exceed the retained-data budget while the
driver receives it. `--max-rows`, `--max-bytes`, and `--timeout` are available
when needed.

Connection loss never triggers automatic replay or reconnection. If PostgreSQL
does not answer a cancellation request within five seconds, the driver closes
the connection. A lost connection can leave the outcome of a write unknown.
A failed connection attempt leaves every open connection as it was. Removing a
saved connection only removes its local settings.

Each buffer keeps the results of its last run, so memory grows with the number
of buffers holding large results. Closing a buffer frees them. Grid cells are
for browsing; changes to database rows happen through SQL. `COPY` is not
supported by this version; use explicit CSV or JSON result export.

## Headless use

```sh
bin/scrunch --command 'select current_database(), current_user'
bin/scrunch --command 'select * from public.events' --format json > events.json
bin/scrunch --execute --file report.sql --format csv > report.csv
bin/scrunch --write --yes --command 'truncate scratch.import'
```

Headless runs are read-only unless you pass `--write`. `UPDATE` or `DELETE`
without `WHERE`, and `TRUNCATE`, are refused unless you also pass `--yes`.
Headless queries are never paged; they run to completion within `--max-rows`
and `--max-bytes`.

CSV and JSON require redirected output, so raw database data cannot send
terminal instructions. Shell redirection uses your shell's file permissions;
the workspace's explicit Export command creates owner-only files.

JSON exports contain column metadata (`name`, `type`, `oid`) and positional
rows. Non-NULL values use PostgreSQL text strings, so duplicate column names and
exact decimal values survive. SQL NULL is JSON null; bytea uses PostgreSQL's
`\x` hex representation. The `truncated` field reports a display limit.
Headless JSON wraps all statement results in a `results` array.

CSV writes SQL NULL as unquoted `\N`, quotes every non-NULL value, and preserves
bytea as PostgreSQL hex. Each statement receives its own header in headless CSV.
Workspace exports contain the fetched rows in the current sorted and filtered
view, not a rerun of the query. CSV values are data, not spreadsheet formulas;
use text import when opening untrusted SQL results in a spreadsheet.

## Build and SSH

Go 1.26.8 is the pinned build toolchain. Dependencies are vendored; once the Go
toolchain is installed, a build can run with dependency networking disabled:

```sh
GOPROXY=off GOSUMDB=off make build
make release
```

`make release` builds macOS arm64 plus Linux amd64 and arm64 with `CGO_ENABLED=0`.
Copy the appropriate Linux binary to a host you control and run it there:

```sh
scp bin/scrunch-linux-amd64 host:~/scrunch
ssh -t host './scrunch'
```

The host needs a terminal and access to PostgreSQL; no Go, Python, Node, or
shared-library installation is required. Use your existing SSH configuration
and tunnels. scrunch does not create SSH tunnels or store SSH credentials.

The vendored Charm sources disable environment-triggered trace/panic files and
the external `tmux` color probe. Builds explicitly use this vendored source.
Refresh dependencies with `make vendor`, which reapplies
[the privacy patch](docs/vendor-privacy.patch); plain `go mod vendor` alone
would discard it. Terminal environment and terminfo still determine colors.

## Development

```sh
make check
make race
make gif      # re-record docs/assets/demo.gif from docs/demo.tape; needs Charm's VHS
```

Database integration tests run only when `SCRUNCH_TEST_DSN` names an isolated
test database; some also use `SCRUNCH_TEST_OTHER_DATABASE` and
`SCRUNCH_TEST_PASSWORD_USER`. They use synthetic data; leave them unset for the
ordinary unit suite. Never point them at a database whose contents matter.

The editor uses standard keys, with no Vim mode. Formatting is conservative and
declines ambiguous or incomplete SQL. Text inspection previews 64 KiB, clipboard
copies are capped at 4 MiB, and JSON trees have size, depth, and node limits.
Original retained values remain available for export. Syntax highlighting has
limited context across multiline strings and comments; query execution uses the
full SQL scanner.

The code is organized as `app / lib / pkg`; see [architecture](docs/ARCHITECTURE.md).

## Who made this

scrunchie is made by Dustin Currie, who also builds Senda, a language-learning
platform. The mascot was modeled and rendered in Blender.

Issues and pull requests are welcome, especially from Charm fans with opinions
about neon.

## License

MIT. See [LICENSE](LICENSE).
