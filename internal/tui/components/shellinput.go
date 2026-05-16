package components

type ShellInputModel struct {
	Value      string
	Cursor     int
	History    []string
	HistoryPos int
	Width      int
	Focused    bool
}

func NewShellInputModel() ShellInputModel {
	return ShellInputModel{
		History:    []string{},
		HistoryPos: -1,
	}
}

func (m *ShellInputModel) InsertRune(r rune) {
	runes := []rune(m.Value)
	runes = append(runes[:m.Cursor], append([]rune{r}, runes[m.Cursor:]...)...)
	m.Value = string(runes)
	m.Cursor++
}

func (m *ShellInputModel) DeleteBackward() {
	if m.Cursor > 0 {
		runes := []rune(m.Value)
		runes = append(runes[:m.Cursor-1], runes[m.Cursor:]...)
		m.Value = string(runes)
		m.Cursor--
	}
}

func (m *ShellInputModel) DeleteForward() {
	runes := []rune(m.Value)
	if m.Cursor < len(runes) {
		runes = append(runes[:m.Cursor], runes[m.Cursor+1:]...)
		m.Value = string(runes)
	}
}

func (m *ShellInputModel) MoveCursorLeft() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *ShellInputModel) MoveCursorRight() {
	if m.Cursor < len([]rune(m.Value)) {
		m.Cursor++
	}
}

func (m *ShellInputModel) MoveCursorHome() {
	m.Cursor = 0
}

func (m *ShellInputModel) MoveCursorEnd() {
	m.Cursor = len([]rune(m.Value))
}

func (m *ShellInputModel) HistoryUp() {
	if len(m.History) == 0 {
		return
	}
	if m.HistoryPos == -1 {
		m.HistoryPos = len(m.History) - 1
	} else if m.HistoryPos > 0 {
		m.HistoryPos--
	}
	m.Value = m.History[m.HistoryPos]
	m.Cursor = len([]rune(m.Value))
}

func (m *ShellInputModel) HistoryDown() {
	if m.HistoryPos == -1 {
		return
	}
	if m.HistoryPos < len(m.History)-1 {
		m.HistoryPos++
		m.Value = m.History[m.HistoryPos]
	} else {
		m.HistoryPos = -1
		m.Value = ""
	}
	m.Cursor = len([]rune(m.Value))
}

func (m *ShellInputModel) Submit() string {
	cmd := m.Value
	if cmd != "" {
		m.History = append(m.History, cmd)
		m.HistoryPos = -1
	}
	m.Value = ""
	m.Cursor = 0
	return cmd
}

func (m *ShellInputModel) View() string {
	if !m.Focused {
		return "> " + m.Value
	}

	runes := []rune(m.Value)
	before := string(runes[:m.Cursor])
	after := string(runes[m.Cursor:])

	return "> " + before + "█" + after
}