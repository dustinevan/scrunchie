package tui

// focusPane changes only the input owner. Each pane keeps its selection so a
// return trip goes back to the user's place instead of resetting the workspace.
func (m *Model) focusPane(target pane) {
	m.focus = target
	m.inspector.active = false
	m.structure.active = false
	m.completion = completion{}
	m.completeAfterCatalog = false
	m.mouse.drag = ""
	m.gridScrolling = false
	if target == catalogPane {
		m.showCatalog = true
		m.databasePane.manualScroll = false
	}
	if target == editorPane {
		m.buffers[m.bufferIndex].editor.scrolling = false
	}
}

func (m *Model) cyclePane(backward bool) {
	target := (m.focus + 1) % 3
	if backward {
		target = (m.focus + 2) % 3
	}
	if target == catalogPane && !m.showCatalog {
		if backward {
			target = resultsPane
		} else {
			target = editorPane
		}
	}
	m.focusPane(target)
}
