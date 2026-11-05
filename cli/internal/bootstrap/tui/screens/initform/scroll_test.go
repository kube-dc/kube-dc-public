package initform

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func scrollingPanel(t *testing.T) *PanelModel {
	t.Helper()
	m := NewEmbeddedPanel(tenHostInputs(t), "", nil)
	m.ConfigureServices(PanelServices{ReviewOnly: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.openSection("Configuration")
	m.focus = focusFields
	m.View()
	return m
}

func TestPortablePagingDoesNotSnapToSelectedField(t *testing.T) {
	for _, pair := range [][2]tea.KeyPressMsg{
		{{Code: 'f', Text: "f"}, {Code: 'b', Text: "b"}},
		{{Code: 'd', Mod: tea.ModCtrl}, {Code: 'u', Mod: tea.ModCtrl}},
		{{Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}},
	} {
		t.Run(pair[0].String(), func(t *testing.T) {
			m := scrollingPanel(t)
			m.Update(pair[0])
			m.View()
			offset := m.fieldsVP.YOffset()
			if offset <= 0 {
				t.Fatal("paging did not scroll the long configuration")
			}
			m.View()
			if m.fieldsVP.YOffset() != offset {
				t.Fatal("render snapped back to the selected field")
			}
			m.Update(pair[1])
			m.View()
			if m.fieldsVP.YOffset() != 0 {
				t.Fatal("reverse paging did not return to the start")
			}
		})
	}
}

func TestMouseWheelScrollRemainsVisible(t *testing.T) {
	m := scrollingPanel(t)
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.View()
	if m.fieldsVP.YOffset() <= 0 {
		t.Fatal("mouse scroll snapped back to the first field")
	}
}

func TestPagingLettersRemainTextDuringEditing(t *testing.T) {
	m := scrollingPanel(t)
	m.openSection("Basics")
	m.focus, m.fieldCursor, m.editing = focusFields, 0, true
	m.input.Focus()
	m.input.SetValue("name")
	m.input.CursorEnd()
	m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.input.Value() != "namefb" || m.manualScroll {
		t.Fatal("paging aliases intercepted text input")
	}
	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if m.manualScroll {
		t.Fatal("editing shortcut became a page command")
	}
}
