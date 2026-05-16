package components

import tea "charm.land/bubbletea/v2"

type CommandExecMsg struct {
	Command string
}

func CommandExec(command string) tea.Cmd {
	return func() tea.Msg {
		return CommandExecMsg{Command: command}
	}
}