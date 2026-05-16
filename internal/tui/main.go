package tui

import (
	tea "charm.land/bubbletea/v2"
)

type Executor struct {
	PerformCheck     func(opts CheckOptions) ([]CheckResult, error)
	RepairResult     func(result CheckResult, idx int) error
	CreateSymlink    func(real, fake, device string, force, smart bool) error
	CreateHardlink   func(prim, seco, device string, force, smart bool) error
	RefreshStore     func() error
	StatusMessage    func(string)
}

func Start(version, buildTime string, exec Executor) error {
	initStyles()

	m := New(version, buildTime, exec)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}