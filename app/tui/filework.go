package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/files"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func (m *Model) saveBuffer(id uint64, as bool) tea.Cmd {
	if m.fileBusy {
		m.status = "A file operation is already in progress"
		return nil
	}
	for _, b := range m.buffers {
		if b.id != id {
			continue
		}
		m.fileJob = fileJob{kind: "save", path: b.path, sql: string(b.editor.text), bufferID: b.id, modal: b.path == "" || as || m.quitIntent || m.closeIntent}
		if b.path == "" || as {
			m.openPrompt("save", "Save SQL file", b.path)
			return nil
		}
		if m.quitIntent || m.closeIntent {
			m.modal = modal{kind: "save", title: "Saving SQL"}
		}
		return m.performFile(m.fileJob, false)
	}
	return nil
}

func (m *Model) exportPrompt() {
	r := m.results()
	if m.fileBusy {
		m.status = "A file operation is already in progress"
		return
	}
	if r.setIndex >= len(r.batch.Results) || len(r.batch.Results[r.setIndex].Columns) == 0 {
		m.status, m.statusError = "There is no result table to export", false
		return
	}
	m.fileJob = fileJob{kind: "export", set: r.batch.Results[r.setIndex], modal: true}
	title := "Export retained rows · .csv or .json"
	if m.fileJob.set.Truncated {
		title = "Export retained rows only (result was truncated)"
	}
	if m.fileJob.set.HasMore {
		title = "Exports the " + thousands(len(m.fileJob.set.Rows)) + " fetched rows · m fetches more first"
	}
	if view := r.view; view.set == &r.batch.Results[r.setIndex] && view.changed() {
		// Export what the grid shows: sorted rows in order, filtered rows only.
		set := &m.fileJob.set
		set.Rows = make([][]result.Cell, len(view.rows))
		for position, index := range view.rows {
			set.Rows[position] = r.batch.Results[r.setIndex].Rows[index]
		}
		m.fileJob.scope = "sorted view"
		if view.filter != "" {
			m.fileJob.scope = "filtered view"
			set.RowCount = int64(len(view.rows))
			if view.order != unsorted {
				m.fileJob.scope = "sorted, filtered view"
			}
		}
		title = "Export " + m.fileJob.scope + " · " + countText(len(view.rows)) + " of " + countText(view.count) + " retained rows · .csv or .json"
		if set.HasMore {
			title += " · fetched rows only"
		}
	}
	m.openPrompt("export", title, "results.csv")
}

func (m *Model) acceptPath(value string) tea.Cmd {
	if value == "" {
		m.modal.error = "Enter a file path"
		return nil
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			m.modal.error = err.Error()
			return nil
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	job := m.fileJob
	if m.modal.kind == "open" {
		job = fileJob{kind: "open", path: value, modal: true, conn: m.buffers[m.bufferIndex].conn}
	} else {
		job.path = value
	}
	if job.kind == "export" {
		switch strings.ToLower(filepath.Ext(value)) {
		case ".csv":
			job.format = "csv"
		case ".json":
			job.format = "json"
		default:
			m.modal.error = "Use a .csv or .json extension"
			return nil
		}
	}
	m.fileJob = job
	return m.performFile(job, job.kind != "open")
}

func (m *Model) performFile(job fileJob, check bool) tea.Cmd {
	m.fileBusy = true
	m.status, m.statusError = "Working with "+job.path, false
	command := func() tea.Msg {
		if check {
			_, err := os.Stat(job.path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fileMsg{job: job, check: true, err: err}
			}
			return fileMsg{job: job, check: true, exists: err == nil}
		}
		switch job.kind {
		case "open":
			sql, err := files.Open(job.path)
			return fileMsg{job: job, sql: sql, err: err}
		case "save":
			return fileMsg{job: job, err: files.Save(job.path, job.sql)}
		case "export":
			return fileMsg{job: job, err: files.Export(job.path, job.set, job.format)}
		}
		return fileMsg{job: job}
	}
	return tea.Batch(command, m.spinner.Tick)
}

func (m *Model) fileResult(msg fileMsg) tea.Cmd {
	m.fileBusy = false
	if msg.err != nil {
		m.status, m.statusError = msg.err.Error(), true
		m.lastMessage = msg.err.Error()
		if m.modal.kind == "" {
			m.modal = modal{kind: "message", title: "File error", message: msg.err.Error()}
		} else {
			if (m.modal.kind == "save" || m.modal.kind == "export") && len(m.modal.input.text) == 0 {
				m.openPrompt(msg.job.kind, "Retry file operation", msg.job.path)
			}
			m.modal.error = msg.err.Error()
		}
		return nil
	}
	if msg.check {
		if msg.exists {
			m.fileJob = msg.job
			m.modal = modal{kind: "confirm-overwrite", title: "Replace existing file?", message: msg.job.path, buttons: []string{"Cancel", "Replace file"}}
			return nil
		}
		return m.performFile(msg.job, false)
	}
	if msg.job.modal {
		m.modal = modal{}
	}
	switch msg.job.kind {
	case "open":
		m.addBuffer(msg.sql, msg.job.path, true, msg.job.conn)
		m.status = "Opened " + msg.job.path
	case "save":
		for i := range m.buffers {
			if m.buffers[i].id == msg.job.bufferID {
				m.buffers[i].saved, m.buffers[i].path = msg.job.sql, msg.job.path
				m.buffers[i].dirty = string(m.buffers[i].editor.text) != msg.job.sql
			}
		}
		m.status = "Saved " + msg.job.path
		if m.quitIntent {
			return m.continueQuit()
		}
		if m.closeIntent {
			m.closeIntent = false
			for i, b := range m.buffers {
				if b.id == msg.job.bufferID {
					m.switchBuffer(i)
					cmd := m.closeBuffer()
					if m.buffers[m.bufferIndex].id != b.id {
						m.status, m.statusError = "Saved and closed "+msg.job.path, false
					}
					return cmd
				}
			}
		}
	case "export":
		scope := "retained rows"
		if msg.job.scope != "" {
			scope = msg.job.scope
		}
		m.status = "Exported " + scope + " to " + msg.job.path
	}
	m.statusError = false
	return nil
}
