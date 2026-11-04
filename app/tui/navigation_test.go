package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEditorVerticalMovementRetainsColumnAcrossShortLines(t *testing.T) {
	for _, shift := range []bool{false, true} {
		e := newEditor("abcdefgh\nx\nabcdefgh")
		e.cursor = 6
		steps := []struct {
			key  string
			want int
		}{{"down", 10}, {"down", 17}, {"up", 10}, {"up", 6}, {"pgdown", 17}, {"pgup", 6}}
		for _, step := range steps {
			e.move(step.key, shift, 2)
			if e.cursor != step.want {
				t.Fatalf("shift=%v after %s: cursor %d, want %d", shift, step.key, e.cursor, step.want)
			}
			if shift && e.anchor != 6 {
				t.Fatalf("selection anchor %d, want 6", e.anchor)
			}
		}
	}
}

func TestEditorVerticalColumnResetsAfterOtherMovementAndEdits(t *testing.T) {
	cases := []struct {
		action string
		want   int
	}{
		{"left", 11}, {"home", 11}, {"end", 12}, {"ctrl+home", 9},
		{"goto", 11}, {"insert", 14}, {"delete", 10},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			e := newEditor("abcdefgh\nx\nabcdefgh")
			e.cursor = 6
			e.move("down", false, 1)
			switch tc.action {
			case "goto":
				if err := e.gotoLine("2"); err != nil {
					t.Fatal(err)
				}
			case "insert":
				e.insert("z", "typing")
			case "delete":
				e.delete(9, 10)
			default:
				e.move(tc.action, false, 1)
			}
			e.move("down", false, 1)
			if e.cursor != tc.want {
				t.Fatalf("cursor %d, want %d", e.cursor, tc.want)
			}
		})
	}
}

func TestEditorUndoAndRedoResetVerticalColumn(t *testing.T) {
	e := newEditor("abcdefgh\nx\nabcdefgh")
	e.cursor = 6
	e.move("down", false, 1)
	e.insert("z", "typing")
	e.move("down", false, 1)
	e.key(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}, 1)
	e.move("down", false, 1)
	if e.cursor != 12 {
		t.Fatalf("after undo and down: cursor %d, want 12", e.cursor)
	}
	e.key(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}, 1)
	e.move("up", false, 1)
	e.move("up", false, 1)
	if e.cursor != 2 {
		t.Fatalf("after redo and two ups: cursor %d, want 2", e.cursor)
	}
}
