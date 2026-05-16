package components

import (
	"fmt"
	"strings"
)

type DetailModel struct {
	Selected *CheckResult
	Width    int
	Height   int
}

func NewDetailModel() DetailModel {
	return DetailModel{}
}

func (m *DetailModel) SetResult(r *CheckResult) {
	m.Selected = r
}

func (m *DetailModel) View() string {
	if m.Selected == nil {
		return "  Select an entry to view details"
	}

	r := m.Selected

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Type:      %s\n", r.Type))

	if r.Device != "" {
		b.WriteString(fmt.Sprintf("Device:    %s\n", r.Device))
	}
	if r.Path != "" {
		b.WriteString(fmt.Sprintf("Group:     %s\n", r.Path))
	}

	switch r.Type {
	case "symlink":
		b.WriteString(fmt.Sprintf("Real:      %s\n", r.Real))
		b.WriteString(fmt.Sprintf("Fake:      %s\n", r.Fake))
	case "hardlink":
		b.WriteString(fmt.Sprintf("Prim:      %s\n", r.Prim))
		b.WriteString(fmt.Sprintf("Seco:      %s\n", r.Seco))
	}

	if r.Valid {
		b.WriteString("Status:    ✓ Valid")
	} else {
		b.WriteString("Status:    ✗ Invalid")
		if r.ErrorType != "" {
			b.WriteString(fmt.Sprintf(" (%s)", r.ErrorType))
		}
	}
	b.WriteString("\n")

	if r.Error != "" {
		b.WriteString(fmt.Sprintf("Error:     %s", truncateStr(r.Error, 60)))
	}

	return b.String()
}

func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}