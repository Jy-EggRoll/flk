package components

import (
	"strings"
)

type TreeItem struct {
	ID       string
	Label    string
	Depth    int
	Count    int
	Expanded bool
	ParentID string
}

type TreeModel struct {
	Items      []TreeItem
	Cursor     int
	SelectedID string
	Width      int
	Height     int
}

func NewTreeModel() TreeModel {
	return TreeModel{
		Items:  []TreeItem{},
		Cursor: 0,
	}
}

func (m *TreeModel) SetItems(items []TreeItem) {
	m.Items = items
	if m.Cursor >= len(m.Items) && len(m.Items) > 0 {
		m.Cursor = len(m.Items) - 1
	}
}

func (m *TreeModel) SelectedItem() *TreeItem {
	if len(m.Items) == 0 {
		return nil
	}
	return &m.Items[m.Cursor]
}

func (m *TreeModel) CursorUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *TreeModel) CursorDown() {
	if m.Cursor < len(m.Items)-1 {
		m.Cursor++
	}
}

func (m *TreeModel) ToggleExpand() {
	item := m.SelectedItem()
	if item == nil {
		return
	}
	item.Expanded = !item.Expanded
}

func (m *TreeModel) View() string {
	if len(m.Items) == 0 {
		return "  (no data)"
	}

	var b strings.Builder
	start, end := 0, len(m.Items)
	if m.Height > 0 && len(m.Items) > m.Height {
		half := m.Height / 2
		if m.Cursor-half > 0 {
			start = m.Cursor - half
		}
		end = start + m.Height
		if end > len(m.Items) {
			end = len(m.Items)
			start = end - m.Height
			if start < 0 {
				start = 0
			}
		}
	}

	for i, item := range m.Items {
		if i < start || i >= end {
			continue
		}

		prefix := strings.Repeat("  ", item.Depth)
		if i == m.Cursor {
			prefix += "▸ "
		} else {
			prefix += "  "
		}

		label := item.Label
		if item.Expanded {
			label = "▼ " + label
		} else if item.Count > 0 {
			label = "▶ " + label
		}

		countStr := ""
		if item.Count > 0 {
			countStr = " (" + itoa(item.Count) + ")"
		}

		b.WriteString(prefix + label + countStr + "\n")
	}

	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}