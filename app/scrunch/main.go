// Command scrunch is a PostgreSQL terminal SQL IDE with no ancillary services.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/dustinevan/scrunchie/app/tui"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/files"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

var version = "dev"

type config struct {
	dsn                string
	file               string
	command            string
	format             string
	demo               bool
	connect            bool
	showVersion        bool
	execute            bool
	maxRows            int
	maxBytes           int64
	timeout            time.Duration
	connectTimeout     time.Duration
	headless           bool
	explicitConnection bool
	write              bool
	yes                bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "scrunch:", termtext.SafeMultiline(err.Error()))
		os.Exit(1)
	}
}

func parseConfig(args []string, stdout, stderr io.Writer) (config, bool, error) {
	var cfg config
	flags := flag.NewFlagSet("scrunch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.dsn, "dsn", "", "PostgreSQL URL or libpq connection string (otherwise PG* environment)")
	flags.StringVar(&cfg.file, "file", "", "open a SQL file in the editor")
	flags.StringVar(&cfg.file, "f", "", "alias for --file")
	flags.StringVar(&cfg.command, "command", "", "execute SQL, print results, and exit")
	flags.StringVar(&cfg.command, "c", "", "alias for --command")
	flags.StringVar(&cfg.format, "format", "table", "headless output: table, csv, or json")
	flags.BoolVar(&cfg.execute, "execute", false, "execute --file and exit")
	flags.BoolVar(&cfg.demo, "demo", false, "open sample workspace without any database connection")
	flags.BoolVar(&cfg.connect, "connect", false, "connect on startup using PostgreSQL environment settings")
	flags.BoolVar(&cfg.showVersion, "version", false, "print version and exit")
	flags.IntVar(&cfg.maxRows, "max-rows", 10000, "maximum retained rows per execution (SQL still completes)")
	flags.Int64Var(&cfg.maxBytes, "max-bytes", 32<<20, "maximum retained result bytes per execution")
	flags.DurationVar(&cfg.timeout, "timeout", 0, "query deadline, such as 30s (0 means no deadline)")
	flags.DurationVar(&cfg.connectTimeout, "connect-timeout", 10*time.Second, "connection deadline")
	flags.BoolVar(&cfg.write, "write", false, "allow writes; connections are read-only by default")
	flags.BoolVar(&cfg.yes, "yes", false, "confirm UPDATE/DELETE without WHERE and TRUNCATE in headless mode")
	flags.Usage = func() { fmt.Fprint(flags.Output(), helpText) }
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, helpText)
		return cfg, true, err
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, true, nil
		}
		return cfg, false, err
	}
	commandProvided, dsnProvided := false, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "command" || f.Name == "c" {
			commandProvided = true
		}
		if f.Name == "dsn" {
			dsnProvided = true
		}
	})
	if cfg.showVersion {
		_, err := fmt.Fprintln(stdout, "scrunch", version)
		return cfg, true, err
	}
	if flags.NArg() > 1 || (flags.NArg() == 1 && dsnProvided) {
		return cfg, false, errors.New("parsing arguments: provide one connection string, either --dsn or positional")
	}
	if flags.NArg() == 1 {
		cfg.dsn = flags.Arg(0)
	}
	if cfg.maxRows < 1 || cfg.maxRows > 1000000 {
		return cfg, false, errors.New("parsing arguments: --max-rows must be between 1 and 1000000")
	}
	if cfg.maxBytes < 1024 || cfg.maxBytes > 1<<30 {
		return cfg, false, errors.New("parsing arguments: --max-bytes must be between 1024 and 1073741824")
	}
	if cfg.timeout < 0 || cfg.connectTimeout <= 0 {
		return cfg, false, errors.New("parsing arguments: --timeout cannot be negative and --connect-timeout must be positive")
	}
	if cfg.format != "table" && cfg.format != "csv" && cfg.format != "json" {
		return cfg, false, errors.New("parsing arguments: --format must be table, csv, or json")
	}
	if commandProvided && cfg.file != "" {
		return cfg, false, errors.New("parsing arguments: use either --command or --file")
	}
	if commandProvided && strings.TrimSpace(cfg.command) == "" {
		return cfg, false, errors.New("parsing arguments: --command requires nonempty SQL")
	}
	if cfg.execute && cfg.file == "" {
		return cfg, false, errors.New("parsing arguments: --execute requires --file")
	}
	cfg.headless = commandProvided || cfg.execute
	cfg.explicitConnection = cfg.connect || dsnProvided || flags.NArg() == 1
	if cfg.demo && (cfg.headless || cfg.explicitConnection) {
		return cfg, false, errors.New("parsing arguments: --demo cannot be combined with a connection or headless execution")
	}
	if cfg.write && !cfg.headless && !cfg.explicitConnection {
		return cfg, false, errors.New("parsing arguments: --write applies to --dsn, a connection string, --connect, --command, or --execute; saved databases have their own Allow writes setting")
	}
	if cfg.yes && !cfg.headless {
		return cfg, false, errors.New("parsing arguments: --yes applies only to --command or --execute; the workspace always asks")
	}
	return cfg, false, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, earlyExit, err := parseConfig(args, stdout, stderr)
	if err != nil || earlyExit {
		return err
	}
	if output, ok := stdout.(*os.File); cfg.headless && cfg.format != "table" && ok && term.IsTerminal(output.Fd()) {
		return errors.New("writing results: redirect CSV or JSON to a file or pipe; use --format table for terminal display")
	}
	if !cfg.headless && (!term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd())) {
		return errors.New("opening workspace: interactive mode needs a terminal; use --command SQL or --execute --file FILE")
	}
	sql := cfg.command
	if cfg.file != "" {
		text, err := files.Open(cfg.file)
		if err != nil {
			return err
		}
		sql = text
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var session *postgres.Session
	connection, connectionError := "not connected", ""
	if cfg.demo {
		connection = "offline demo"
	}
	if cfg.headless || cfg.explicitConnection {
		connectCtx, cancel := context.WithTimeout(ctx, cfg.connectTimeout)
		var err error
		session, err = postgres.Open(connectCtx, cfg.dsn, postgres.Options{AllowWrites: cfg.write})
		cancel()
		if err != nil {
			if cfg.headless {
				return err
			}
			connectionError = err.Error()
		} else {
			connection = session.Description()
		}
	}
	limits := result.Limits{MaxRows: cfg.maxRows, MaxBytes: cfg.maxBytes}
	if cfg.headless {
		defer closeSession(session, stderr)
		// Check with the session's string mode; report findings, not the SQL.
		if findings := sqledit.Unfiltered(sql, session.BackslashEscapes()); len(findings) > 0 && !cfg.yes {
			var reasons []string
			for i, finding := range findings {
				if i == 3 {
					reasons = append(reasons, fmt.Sprintf("…and %d more.", len(findings)-i))
					break
				}
				reasons = append(reasons, finding.String())
			}
			return fmt.Errorf("refusing to run SQL without --yes; nothing was run: %s", strings.Join(reasons, " "))
		}
		queryCtx := ctx
		if cfg.timeout > 0 {
			var cancel context.CancelFunc
			queryCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
			defer cancel()
		}
		batch, queryErr := session.Execute(queryCtx, sql, limits)
		if err := printBatch(stdout, batch, cfg.format); err != nil {
			return err
		}
		for _, notice := range batch.Notices {
			fmt.Fprintln(stderr, "notice:", termtext.Safe(notice))
		}
		if batch.Truncated {
			fmt.Fprintln(stderr, "scrunch: some result output was not retained; SQL was sent unchanged")
		}
		var pgErr postgres.Error
		if errors.As(queryErr, &pgErr) && pgErr.Code == "25006" {
			if session.Standby() {
				return fmt.Errorf("%w; this server is a read-only standby", queryErr)
			}
			if !cfg.write {
				return fmt.Errorf("%w; connections are read-only by default, rerun with --write", queryErr)
			}
		}
		return queryErr
	}
	var profiles []connections.Profile
	profilesPath, profilesError := "", ""
	if !cfg.demo {
		var err error
		profilesPath, err = connections.DefaultPath()
		if err == nil {
			profiles, err = connections.Load(profilesPath)
		}
		if err != nil {
			profilesError = err.Error()
		}
	}
	model := tui.New(tui.Config{
		Session: session, Connection: connection, InitialSQL: sql,
		InitialPath: cfg.file, Limits: limits, Demo: cfg.demo, Timeout: cfg.timeout,
		DSN: cfg.dsn, ConnectTimeout: cfg.connectTimeout,
		ConnectionExplicit: cfg.explicitConnection, ConnectionError: connectionError,
		Profiles: profiles, ProfilesPath: profilesPath, ProfilesError: profilesError,
		AllowWrites: cfg.write,
	})
	defer closeWorkspace(model, stderr)
	if _, err := tea.NewProgram(model, tea.WithContext(ctx)).Run(); err != nil {
		return fmt.Errorf("running workspace: %w", err)
	}
	return nil
}

func closeWorkspace(model *tui.Model, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := model.Close(ctx); err != nil {
		fmt.Fprintln(stderr, "scrunch:", termtext.Safe(err.Error()))
	}
}

func closeSession(session *postgres.Session, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Close(ctx); err != nil {
		fmt.Fprintln(stderr, "scrunch:", termtext.Safe(err.Error()))
	}
}

func printBatch(w io.Writer, batch result.Batch, format string) error {
	if format == "json" {
		if _, err := io.WriteString(w, "{\"results\":[\n"); err != nil {
			return fmt.Errorf("writing results: %w", err)
		}
		for i, set := range batch.Results {
			if i > 0 {
				if _, err := io.WriteString(w, ",\n"); err != nil {
					return fmt.Errorf("writing results: %w", err)
				}
			}
			if err := files.Write(w, set, "json"); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "]}\n")
		return err
	}
	for i, set := range batch.Results {
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return fmt.Errorf("writing results: %w", err)
			}
		}
		if format == "csv" {
			if err := files.Write(w, set, format); err != nil {
				return err
			}
			continue
		}
		writer := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for i, column := range set.Columns {
			separator := "\t"
			if i == len(set.Columns)-1 {
				separator = "\n"
			}
			if _, err := io.WriteString(writer, termtext.Safe(column.Name)+separator); err != nil {
				return fmt.Errorf("writing column: %w", err)
			}
		}
		for _, row := range set.Rows {
			for i, value := range row {
				column := result.Column{}
				if i < len(set.Columns) {
					column = set.Columns[i]
				}
				text := []rune(cell.Display(value, column, cell.Auto))
				if len(text) > 100 {
					text = append(text[:99], '…')
				}
				separator := "\t"
				if i == len(row)-1 {
					separator = "\n"
				}
				if _, err := io.WriteString(writer, string(text)+separator); err != nil {
					return fmt.Errorf("writing value: %w", err)
				}
			}
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("writing table: %w", err)
		}
		if _, err := fmt.Fprintln(w, termtext.Safe(strings.TrimSpace(set.Command))); err != nil {
			return fmt.Errorf("writing command result: %w", err)
		}
		if set.Truncated {
			if _, err := fmt.Fprintf(w, "Showing %d of %d returned rows.\n", len(set.Rows), set.RowCount); err != nil {
				return fmt.Errorf("writing limit notice: %w", err)
			}
		}
	}
	return nil
}

const helpText = `scrunchie — PostgreSQL in your terminal

One binary. No telemetry, update checks, or services beyond your PostgreSQL
connection. Query history stays in memory. Works locally and over SSH.

Run scrunch with no arguments for a disconnected workspace. Ctrl+B adds or
selects a database. Named connections are stored in ~/.scrunch as JSON;
passwords are never stored. A connection can name a password environment
variable, which is read when connecting or reconnecting. Sessions are
read-only unless --write or a saved database's Allow writes is set.

Workspace keys
  Ctrl+J run SQL       Ctrl+C cancel/copy     Ctrl+P command palette
  F2 editor           F5 results             F6 catalog
  Enter inspect cell  F1 help                Ctrl+Q quit
  Ctrl+B databases

Options
  --dsn STRING           PostgreSQL URL or libpq connection string
  --connect              Connect immediately using PostgreSQL environment
  --file, -f PATH         Open SQL in the editor
  --command, -c SQL       Execute SQL and exit
  --execute              Execute --file and exit
  --format FORMAT        Headless output: table (default), csv, json
                         CSV/JSON require output redirected to a file or pipe
  --max-rows N           Retain at most N rows (default 10000)
  --max-bytes N          Retain at most N bytes (default 33554432)
  --timeout DURATION     Query deadline, e.g. 30s (default no deadline)
  --connect-timeout D    Connection deadline (default 10s)
  --write                Allow writes; connections are read-only by default
  --yes                  Confirm UPDATE/DELETE without WHERE and TRUNCATE in
                         headless mode
  --demo                 Sample workspace; no connection or network
  --version              Print version
  --help                 Print help

Headless commands and --connect use standard PostgreSQL environment settings
when no DSN is given. PostgreSQL password/service files are supported.
Put flags before a positional connection string.

Usage
  scrunch [options] [connection-string]

Examples
  scrunch
  scrunch --demo
  PGHOST=localhost PGDATABASE=app scrunch --connect
  scrunch 'postgres://user@localhost/app?sslmode=disable'
  scrunch --file query.sql 'postgres://user@localhost/app?sslmode=disable'
  scrunch --command 'select current_database()'
  scrunch --execute --file report.sql --format json > report.json
`
