package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// File commands have one I/O command and one spinner tick. Exercise the real
// filesystem command without waiting for timer messages or starting a terminal.
func fileReply(t *testing.T, cmd tea.Cmd) fileMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing file command")
	}
	message := cmd()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			if reply, ok := child().(fileMsg); ok {
				return reply
			}
		}
	}
	if reply, ok := message.(fileMsg); ok {
		return reply
	}
	t.Fatalf("no file reply in %T", message)
	return fileMsg{}
}

func TestSaveAsConfirmsExistingFileAndDefaultsToCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{InitialSQL: "SELECT 42;"})
	m.saveBuffer(m.buffers[0].id, true)
	reply := fileReply(t, m.acceptPath(path))
	m.Update(reply)
	if m.modal.kind != "confirm-overwrite" || m.modal.index != 0 {
		t.Fatal("existing file did not require explicit replace")
	}
	press(m, tea.KeyEnter)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("default overwrite confirmation changed the file")
	}
	m.saveBuffer(m.buffers[0].id, true)
	m.Update(fileReply(t, m.acceptPath(path)))
	press(m, tea.KeyRight)
	cmd := press(m, tea.KeyEnter)
	m.Update(fileReply(t, cmd))
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "SELECT 42;" || m.buffers[0].path != path || m.modal.kind != "" {
		t.Fatalf("replace failed: %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("saved SQL permissions are not owner-only")
	}
}

func TestAsyncSaveAcknowledgesOnlyWrittenSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.sql")
	m := New(Config{InitialSQL: "SELECT 1;", InitialPath: path})
	m.buffers[0].editor.cursor = len(m.buffers[0].editor.text)
	m.buffers[0].editor.insert("\nSELECT 2;", "")
	cmd := m.saveBuffer(m.buffers[0].id, false)
	if second := m.saveBuffer(m.buffers[0].id, false); second != nil {
		t.Fatal("second save started while first was pending")
	}
	m.buffers[0].editor.insert("\nSELECT 3;", "")
	m.openPrompt("find", "Find", "unchanged prompt")
	m.Update(fileReply(t, cmd))
	if !m.buffers[0].dirty || strings.Contains(m.buffers[0].saved, "SELECT 3") {
		t.Fatal("save marked later edits as saved")
	}
	if m.modal.kind != "find" || string(m.modal.input.text) != "unchanged prompt" {
		t.Fatal("background save dismissed another modal")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "SELECT 1;\nSELECT 2;" {
		t.Fatalf("saved snapshot = %q %v", data, err)
	}
}

func TestSaveFailureKeepsDirtySQLAndQuitFlowOpen(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT 1;"})
	m.buffers[0].editor.insert("--unsaved\n", "")
	press(m, 'q', tea.ModCtrl)
	press(m, tea.KeyRight)
	press(m, tea.KeyEnter)
	if m.modal.kind != "save" {
		t.Fatal("Save choice did not request a path")
	}
	path := filepath.Join(t.TempDir(), "missing-directory", "query.sql")
	reply := fileReply(t, m.acceptPath(path))
	_, cmd := m.Update(reply)
	if reply.check && reply.err == nil && !reply.exists {
		reply = fileReply(t, cmd)
		_, cmd = m.Update(reply)
	}
	if reply.err == nil || cmd != nil || !m.buffers[0].dirty || !m.quitIntent || m.modal.error == "" {
		t.Fatal("save failure discarded SQL, quit, or hid the error")
	}
	press(m, tea.KeyEsc)
	if m.quitIntent || m.modal.kind != "" || !m.buffers[0].dirty {
		t.Fatal("canceling failed save did not return to dirty editor")
	}
}

func TestOpeningSQLNormalizesLinesWithoutMarkingDirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;\r\nSELECT 2;\r"), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{})
	m.openPrompt("open", "Open", "")
	m.Update(fileReply(t, m.acceptPath(path)))
	b := m.buffers[m.bufferIndex]
	if b.dirty || b.saved != "SELECT 1;\nSELECT 2;\n" || string(b.editor.text) != b.saved {
		t.Fatal("opened CRLF file was dirty or incorrectly normalized")
	}
	if err := os.WriteFile(path, []byte("\xff"), 0600); err != nil {
		t.Fatal(err)
	}
	m.openPrompt("open", "Open", "")
	m.Update(fileReply(t, m.acceptPath(path)))
	if len(m.buffers) != 2 || m.modal.error == "" {
		t.Fatal("invalid UTF-8 created a buffer or hid the failure")
	}
}

func TestCompletedQueryDoesNotAcceptPendingQuitConfirmation(t *testing.T) {
	m := New(Config{})
	c := offlineConnection(m)
	c.query.running, c.query.serial = true, 4
	press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "confirm-quit" || !strings.Contains(m.modal.message, "running query will be canceled") {
		t.Fatal("running query had no quit confirmation")
	}
	_, cmd := m.Update(queryMsg{conn: c.id, serial: 4, err: errors.New("query completed while dialog open")})
	if cmd != nil || m.modal.kind != "confirm-quit" || strings.Contains(m.modal.message, "running query") {
		t.Fatal("query completion accepted quit without user's choice")
	}
	press(m, tea.KeyEnter)
	if m.quitIntent {
		t.Fatal("default Stay did not cancel quit intent")
	}
}
