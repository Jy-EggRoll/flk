package components

import "strings"

type StatusBarModel struct {
	Text       string
	StatusType string
	LeftText   string
	RightText  string
}

func NewStatusBarModel() StatusBarModel {
	return StatusBarModel{}
}

func (m *StatusBarModel) SetInfo(text string) {
	m.Text = text
	m.StatusType = "info"
}

func (m *StatusBarModel) SetSuccess(text string) {
	m.Text = text
	m.StatusType = "success"
}

func (m *StatusBarModel) SetError(text string) {
	m.Text = text
	m.StatusType = "error"
}

func (m *StatusBarModel) SetWarning(text string) {
	m.Text = text
	m.StatusType = "warning"
}

func (m *StatusBarModel) View() string {
	if m.LeftText != "" || m.RightText != "" {
		pad := 60 - len(m.LeftText) - len(m.RightText)
	if pad < 0 {
		pad = 0
	}
	return m.LeftText + strings.Repeat(" ", pad) + m.RightText
	}
	return m.Text
}

