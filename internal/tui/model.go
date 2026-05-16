package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jy-eggroll/flk/internal/tui/components"
)

type CheckResult = components.CheckResult
type CheckOptions = components.CheckOptions

type sessionState int

const (
	stateNormal sessionState = iota
	stateCommand
)

type Model struct {
	version   string
	buildTime string
	exec      Executor
	state     sessionState

	tree    components.TreeModel
	detail  components.DetailModel
	table   components.TableModel
	input   components.ShellInputModel
	status  components.StatusBarModel
	width   int
	height  int
	results []CheckResult
	err     error
}

func New(version, buildTime string, exec Executor) Model {
	return Model{
		version:   version,
		buildTime: buildTime,
		exec:      exec,
		state:     stateNormal,
		tree:      components.NewTreeModel(),
		detail:    components.NewDetailModel(),
		table:     components.NewTableModel(),
		input:     components.NewShellInputModel(),
		status:    components.NewStatusBarModel(),
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return update(m, msg)
}

func (m Model) View() tea.View {
	v := tea.NewView(view(m))
	v.AltScreen = true
	return v
}

func (m Model) focusedComponent() string {
	if m.state == stateCommand {
		return "input"
	}
	return m.table.Focused()
}