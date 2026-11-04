package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInheritedTraceEnvironmentCannotCreateLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive-screen.log")
	t.Setenv("TEA_TRACE", path)
	t.Setenv("TEA_DEBUG", "true")
	// This catches accidental removal of the pinned vendor privacy patch.
	_ = tea.NewProgram(New(Config{Demo: true}))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inherited TEA_TRACE created a log: %v", err)
	}
}
