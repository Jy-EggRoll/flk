package components

import (
	"fmt"
	"strings"
)

type TableModel struct {
	Results    []CheckResult
	Cursor     int
	Offset     int
	Width      int
	Height     int
	Focus      string
}

func NewTableModel() TableModel {
	return TableModel{
		Focus: "tree",
	}
}

func (m *TableModel) SetResults(results []CheckResult) {
	m.Results = results
	if m.Cursor >= len(results) && len(results) > 0 {
		m.Cursor = 0
	}
	m.updateOffset()
}

func (m *TableModel) Focused() string {
	return m.Focus
}

func (m *TableModel) SetFocus(f string) {
	m.Focus = f
}

func (m *TableModel) CursorUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
	m.updateOffset()
}

func (m *TableModel) CursorDown() {
	if m.Cursor < len(m.Results)-1 {
		m.Cursor++
	}
	m.updateOffset()
}

func (m *TableModel) SelectedResult() *CheckResult {
	if len(m.Results) == 0 {
		return nil
	}
	return &m.Results[m.Cursor]
}

func (m *TableModel) updateOffset() {
	if m.Height <= 0 {
		return
	}
	if m.Cursor < m.Offset {
		m.Offset = m.Cursor
	}
	if m.Cursor >= m.Offset+m.Height {
		m.Offset = m.Cursor - m.Height + 1
	}
}

func (m *TableModel) View() string {
	if len(m.Results) == 0 {
		return "  (no links)  "
	}

	rightW := m.Width
	if rightW <= 0 {
		rightW = 60
	}

	leftW := rightW / 3

	n := m.Height
	if n <= 0 {
		n = 20
	}
	if len(m.Results)-m.Offset < n {
		n = len(m.Results) - m.Offset
	}
	if n <= 0 {
		return "  (no links)  "
	}

	var b strings.Builder

	for i := 0; i < n; i++ {
		idx := m.Offset + i
		if idx >= len(m.Results) {
			break
		}
		r := m.Results[idx]

		isSelected := idx == m.Cursor
		if isSelected {
			b.WriteString("▸ ")
		} else {
			b.WriteString("  ")
		}

		typeShort := "?"
		switch r.Type {
		case "symlink":
			typeShort = "sym"
		case "hardlink":
			typeShort = "hd"
		}

		status := "✓"
		if !r.Valid {
			status = "✗"
		}

		pathStr := r.Real
		if pathStr == "" {
			pathStr = r.Prim
		}

		fakeStr := r.Fake
		if fakeStr == "" {
			fakeStr = r.Seco
		}

		displayW := rightW - 4 - 1 - 3 - 1 - 1 - 1
		if displayW < 10 {
			displayW = 10
		}

		pathPart := truncatePadded(pathStr, displayW/2, leftW/2)
		fakePart := truncatePadded(fakeStr, displayW/2, leftW/2)

		line := fmt.Sprintf("%s %s %s",
			truncatePad(typeShort, 3),
			rpad(truncatePadded(pathPart, displayW/2, leftW/2), displayW/2),
			rpad(fakePart, displayW/2),
		)

		if isSelected {
			b.WriteString(line + " " + status + strings.Repeat(" ", maxG(0, rightW-2-len(line)-2)) + "\n")
		} else {
			b.WriteString(line + " " + status + "\n")
		}
	}

	return b.String()
}

func truncatePad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) > w {
		if w > 3 {
			return string(runes[:w-3]) + "..."
		}
		return string(runes[:w])
	}
	return s + strings.Repeat(" ", w-len(runes))
}

func truncatePadded(s string, maxW int, fallback int) string {
	if maxW <= 0 {
		maxW = fallback
	}
	if maxW <= 0 {
		maxW = 30
	}
	runes := []rune(s)
	if len(runes) <= maxW {
		return s
	}
	if maxW > 3 {
		return string(runes[:maxW-3]) + "..."
	}
	return string(runes[:maxW])
}

func rpad(s string, w int) string {
	runes := []rune(s)
	if len(runes) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(runes))
}

func maxG(a, b int) int {
	if a > b {
		return a
	}
	return b
}