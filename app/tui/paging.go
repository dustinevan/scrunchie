package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// firstPageRows is the first request for a plain read. Fetching more re-runs
// the query with ten times the previous page, up to the row budget.
const firstPageRows = 1000

// runOrigin identifies the SQL that produced the current results, so fetching
// more re-runs that statement even after the editor cursor or buffer changes.
// source is the buffer text at that run, used only to place error positions.
type runOrigin struct {
	sql    string
	buffer uint64
	conn   uint64
	offset int
	source string
	page   int
}

// rowBudget is --max-rows, or the session's default of 10,000 rows.
func (m *Model) rowBudget() int {
	if m.config.Limits.MaxRows > 0 {
		return m.config.Limits.MaxRows
	}
	return 10000
}

// fetchMore re-runs the SQL behind a partial result through the ordinary Run
// path with a larger page, on the connection that produced it. It never
// continues a cursor: PostgreSQL computes the query again from the start, in
// the current transaction state.
func (m *Model) fetchMore() tea.Cmd {
	r := m.results()
	if r.setIndex < 0 || r.setIndex >= len(r.batch.Results) {
		m.status, m.statusError = "There is no result to fetch more rows for · Ctrl+J runs SQL", false
		return nil
	}
	set := r.batch.Results[r.setIndex]
	switch {
	case !set.HasMore && set.Truncated:
		m.status, m.statusError = "This result reached the display limit · narrow the query or raise --max-rows or --max-bytes", false
		return nil
	case !set.HasMore:
		m.status, m.statusError = "Every row of this result is already shown", false
		return nil
	case set.Truncated:
		m.status, m.statusError = "Fetched rows already fill the display byte limit · narrow the query or raise --max-bytes", false
		return nil
	case r.origin.page >= m.rowBudget():
		m.status, m.statusError = "Row limit of "+thousands(m.rowBudget())+" reached · narrow the query or raise --max-rows", false
		return nil
	case r.origin.sql == "":
		m.status, m.statusError = "Run the query again to fetch more rows", false
		return nil
	case r.origin.conn != m.buffers[m.bufferIndex].conn || m.connectionFor(r.origin.conn) == nil:
		m.status, m.statusError = "These results came from "+termtext.Safe(r.connName)+", which this buffer no longer uses · run the query again", false
		return nil
	}
	m.fetchPending = true
	cmd := m.runSQL(false)
	if c := m.conn(); c != nil && c.query.running && c.query.fetchMore && !c.query.queued {
		m.status = "Re-running the query for up to " + thousands(c.query.page) + " rows · Ctrl+C to cancel"
	}
	return cmd
}

// runStatus summarizes a successful run. It reports what was fetched without
// claiming a total that PostgreSQL never computed. detail adds the explanation
// shown by Read last status.
func runStatus(batch result.Batch, page, budget int) (status, detail string) {
	var shown, received int64
	more := false
	for _, set := range batch.Results {
		shown += int64(len(set.Rows))
		received += set.RowCount
		more = more || set.HasMore
	}
	results := ""
	if len(batch.Results) != 1 {
		results = strconv.Itoa(len(batch.Results)) + " results · "
	}
	stopped := "PostgreSQL stopped after the first " + thousands(int(received)) + " rows, so there is no total row count. Select-list expressions, including functions with side effects, ran only for the rows fetched plus one that showed more exist."
	switch {
	case more && batch.Truncated:
		status = thousands(int(shown)) + " of the first " + thousands(int(received)) + " rows shown · display limit · more available"
		detail = stopped + " Only " + thousands(int(shown)) + " rows fit the display byte limit; narrow the query or raise --max-bytes."
	case more && page < budget:
		status = "First " + thousands(int(shown)) + " rows · more available · m fetches more (re-runs query)"
		detail = stopped + " m or Ctrl+P › Fetch more rows re-runs the same SQL for up to " + thousands(min(page*10, budget)) + " rows."
	case more:
		status = "First " + thousands(int(shown)) + " rows · more available · row limit reached; narrow the query or raise --max-rows"
		detail = stopped + " The row limit is " + thousands(budget) + " (--max-rows)."
	case batch.Truncated:
		status = results + thousands(int(shown)) + " of " + thousands(int(received)) + " rows shown · display limit"
		detail = "PostgreSQL ran the SQL to completion. scrunch kept at most " + thousands(budget) + " rows (--max-rows) within the display byte limit (--max-bytes) and read the remaining output without keeping it."
	default:
		status = results + thousands(int(received)) + " rows"
		if len(batch.Results) == 1 {
			status = "1 result · " + status
		}
	}
	return status + " · " + batch.Duration.Round(time.Millisecond).String(), detail
}

// pageNote labels a partial result in the results header.
func pageNote(set result.Set, page, budget int) string {
	switch {
	case set.Truncated && set.HasMore:
		return thousands(len(set.Rows)) + " of the first " + thousands(int(set.RowCount)) + " rows shown · display limit · more available"
	case set.Truncated:
		return thousands(len(set.Rows)) + " of " + thousands(int(set.RowCount)) + " rows shown · display limit"
	case set.HasMore && page < budget:
		return "first " + thousands(len(set.Rows)) + " rows · more available · m fetches more"
	case set.HasMore:
		return "first " + thousands(len(set.Rows)) + " rows · more available · row limit reached"
	}
	return ""
}

// failureText describes a run that produced no results, for the results pane.
// A PostgreSQL position becomes a line and column in source, the text of the
// buffer that ran, where the SQL began at rune offset.
func failureText(err error, source string, offset int) string {
	var pgErr postgres.Error
	if !errors.As(err, &pgErr) {
		return err.Error()
	}
	lines := []string{pgErr.Message}
	facts := ""
	if pgErr.Code != "" {
		facts = "SQLSTATE " + pgErr.Code
	}
	runes := []rune(source)
	if index := offset + pgErr.Position - 1; pgErr.Position > 0 && index <= len(runes) {
		line, column := 1, 1
		for _, r := range runes[:index] {
			column++
			if r == '\n' {
				line, column = line+1, 1
			}
		}
		if facts != "" {
			facts += " · "
		}
		facts += fmt.Sprintf("line %d, column %d", line, column)
	}
	if facts != "" {
		lines = append(lines, facts)
	}
	if pgErr.Detail != "" {
		lines = append(lines, "Detail: "+pgErr.Detail)
	}
	if pgErr.Hint != "" {
		lines = append(lines, "Hint: "+pgErr.Hint)
	}
	return strings.Join(lines, "\n")
}

// resultErrorView replaces earlier results after a run that failed without
// producing any, so they are not mistaken for its output.
func (m *Model) resultErrorView(width, height int) string {
	lines := []string{""}
	for i, line := range strings.Split(m.results().err, "\n") {
		wrapped := strings.Split(ansi.Wrap(termtext.Safe(line), max(1, width-4), ""), "\n")
		for j, part := range wrapped {
			switch {
			case i == 0 && j == 0:
				lines = append(lines, errorStyle.Render("  ! "+part))
			case i == 0:
				lines = append(lines, errorStyle.Render("    "+part))
			default:
				lines = append(lines, "    "+part)
			}
		}
	}
	lines = append(lines, "", mutedStyle.Render(ansi.Truncate("  This run returned no results.", width, "…")))
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

// thousands groups the digits of a count with commas: 10000 becomes "10,000".
func thousands(n int) string {
	digits := strconv.Itoa(max(0, n))
	var grouped strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return grouped.String()
}
