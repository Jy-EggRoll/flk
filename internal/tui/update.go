package tui

import (
	"fmt"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jy-eggroll/flk/internal/tui/components"
)

func update(m Model, msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = handleResize(m)

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			return m, tea.Quit

		case ":":
			if m.state == stateNormal {
				m.state = stateCommand
				m.input.Focused = true
				return m, nil
			}

		case "tab":
			if m.state == stateNormal {
				switch m.table.Focus {
				case "tree":
					m.table.SetFocus("table")
				case "table":
					m.table.SetFocus("tree")
				}
			}

		case "r":
			if m.state == stateNormal {
				m = refreshData(m)
			}
		}

		if m.state == stateNormal {
			return handleNormalKey(m, msg)
		} else if m.state == stateCommand {
			return handleCommandKey(m, msg)
		}

	case components.CommandExecMsg:
		m = executeCommand(m, msg.Command)
	}

	return m, nil
}

func handleNormalKey(m Model, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		switch m.table.Focus {
		case "tree":
			m.tree.CursorUp()
			m = syncTableFromTree(m)
		case "table":
			m.table.CursorUp()
			m = syncDetailFromTable(m)
		}

	case "down", "j":
		switch m.table.Focus {
		case "tree":
			m.tree.CursorDown()
			m = syncTableFromTree(m)
		case "table":
			m.table.CursorDown()
			m = syncDetailFromTable(m)
		}

	case "enter":
		if m.table.Focus == "tree" {
			m.tree.ToggleExpand()
		}

	case "left", "h":
		if m.table.Focus == "table" {
			m.table.SetFocus("tree")
		}

	case "right", "l":
		if m.table.Focus == "tree" {
			m.table.SetFocus("table")
		}

	case "esc":
		m.state = stateNormal
		m.input.Focused = false
	}

	return m, nil
}

func handleCommandKey(m Model, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		cmdLine := m.input.Submit()
		m.state = stateNormal
		m.input.Focused = false
		if cmdLine != "" {
			return m, components.CommandExec(cmdLine)
		}

	case "esc":
		m.state = stateNormal
		m.input.Focused = false
		m.input.Value = ""
		m.input.Cursor = 0

	case "backspace":
		m.input.DeleteBackward()

	case "delete":
		m.input.DeleteForward()

	case "left":
		m.input.MoveCursorLeft()

	case "right":
		m.input.MoveCursorRight()

	case "home":
		m.input.MoveCursorHome()

	case "end":
		m.input.MoveCursorEnd()

	case "up":
		m.input.HistoryUp()

	case "down":
		m.input.HistoryDown()

	case "space":
		m.input.InsertRune(' ')

	case "tab":
		m.input.Value += "  "
		m.input.Cursor += 2

	default:
		if len(msg.Text) == 1 {
			m.input.InsertRune(rune(msg.Text[0]))
		}
	}

	return m, nil
}

func executeCommand(m Model, input string) Model {
	parts := parseCommand(input)
	if len(parts) == 0 {
		m.status.SetError("empty command")
		return m
	}

	switch parts[0] {
	case "check", "ck":
		return executeCheck(m, parts[1:])
	case "fix", "fx":
		return executeFix(m, parts[1:])
	case "symlink", "sm":
		return executeCreateSymlink(m, parts[1:])
	case "hardlink", "hd":
		return executeCreateHardlink(m, parts[1:])
	case "upgrade", "up", "update":
		return executeUpgrade(m, parts[1:])
	case "version", "ver":
		m.status.Text = fmt.Sprintf("flk %s (built: %s) [%s-%s]", m.version, m.buildTime, runtime.GOOS, runtime.GOARCH)
	case "help", "h":
		m.status.Text = "commands: check, fix, symlink, hardlink, upgrade, version, clear, quit"
	case "clear", "cls":
	case "quit", "q", "exit":
		m.err = fmt.Errorf("quit")
		return m
	default:
		m.status.Text = "unknown command: " + parts[0]
	}

	return m
}

func parseCommand(input string) []string {
	var parts []string
	current := strings.Builder{}
	inQuote := false

	for _, r := range strings.TrimSpace(input) {
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if r == ' ' && !inQuote {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

func executeCheck(m Model, args []string) Model {
	opts := CheckOptions{
		CheckSymlink:  true,
		CheckHardlink: true,
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--device", "-d":
			if i+1 < len(args) {
				i++
				opts.DeviceFilters = strings.Split(args[i], ",")
			}
		case "--symlink":
			opts.CheckHardlink = false
		case "--hardlink":
			opts.CheckSymlink = false
		case "--dir":
			if i+1 < len(args) {
				i++
				opts.CheckDir = args[i]
			}
		}
	}

	if m.exec.PerformCheck == nil {
		m.status.Text = "check not available"
		return m
	}

	results, err := m.exec.PerformCheck(opts)
	if err != nil {
		m.status.Text = "check failed: " + err.Error()
		return m
	}

	m.results = results
	m = rebuildTree(m)
	m.table.SetResults(results)
	m.table.Cursor = 0
	m.table.Offset = 0
	m = syncDetailFromTable(m)
	m.status.Text = fmt.Sprintf("checked %d links", len(results))

	return m
}

func executeFix(m Model, args []string) Model {
	opts := CheckOptions{
		CheckSymlink:  true,
		CheckHardlink: true,
	}
	all := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--device", "-d":
			if i+1 < len(args) {
				i++
				opts.DeviceFilters = strings.Split(args[i], ",")
			}
		case "--symlink":
			opts.CheckHardlink = false
		case "--hardlink":
			opts.CheckSymlink = false
		case "--all", "-a":
			all = true
		case "--dir":
			if i+1 < len(args) {
				i++
				opts.CheckDir = args[i]
			}
		}
	}

	if m.exec.PerformCheck == nil || m.exec.RepairResult == nil {
		m.status.Text = "fix not available"
		return m
	}

	results, err := m.exec.PerformCheck(opts)
	if err != nil {
		m.status.Text = "check failed: " + err.Error()
		return m
	}

	var fixed, failed int
	for _, r := range results {
		if r.Valid {
			continue
		}
		if all {
			err := m.exec.RepairResult(r, 0)
			if err != nil {
				failed++
			} else {
				fixed++
			}
		}
	}

	m = refreshData(m)
	m.status.Text = fmt.Sprintf("fixed %d links, %d failed", fixed, failed)
	return m
}

func executeCreateSymlink(m Model, args []string) Model {
	var real, fake, device string
	force := false
	smart := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--real", "-r":
			if i+1 < len(args) {
				i++
				real = args[i]
			}
		case "--fake", "-f":
			if i+1 < len(args) {
				i++
				fake = args[i]
			}
		case "--device", "-d":
			if i+1 < len(args) {
				i++
				device = args[i]
			}
		case "--force":
			force = true
		case "--smart":
			smart = true
		}
	}

	if real == "" || fake == "" {
		m.status.Text = "symlink: --real and --fake are required"
		return m
	}
	if device == "" {
		device = "all"
	}

	if m.exec.CreateSymlink == nil {
		m.status.Text = "symlink not available"
		return m
	}

	err := m.exec.CreateSymlink(real, fake, device, force, smart)
	if err != nil {
		m.status.Text = "symlink failed: " + err.Error()
	} else {
		m.status.Text = "symlink created successfully"
		m = refreshData(m)
	}

	return m
}

func executeCreateHardlink(m Model, args []string) Model {
	var prim, seco, device string
	force := false
	smart := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--prim", "-p":
			if i+1 < len(args) {
				i++
				prim = args[i]
			}
		case "--seco", "-s":
			if i+1 < len(args) {
				i++
				seco = args[i]
			}
		case "--device", "-d":
			if i+1 < len(args) {
				i++
				device = args[i]
			}
		case "--force":
			force = true
		case "--smart":
			smart = true
		}
	}

	if prim == "" || seco == "" {
		m.status.Text = "hardlink: --prim and --seco are required"
		return m
	}
	if device == "" {
		device = "all"
	}

	if m.exec.CreateHardlink == nil {
		m.status.Text = "hardlink not available"
		return m
	}

	err := m.exec.CreateHardlink(prim, seco, device, force, smart)
	if err != nil {
		m.status.Text = "hardlink failed: " + err.Error()
	} else {
		m.status.Text = "hardlink created successfully"
		m = refreshData(m)
	}

	return m
}

func executeUpgrade(m Model, args []string) Model {
	_ = args
	m.status.Text = "upgrade not available in TUI mode. Use 'flk upgrade' in CLI."
	return m
}

func refreshData(m Model) Model {
	if m.exec.RefreshStore != nil {
		if err := m.exec.RefreshStore(); err != nil {
			m.status.Text = "reload failed: " + err.Error()
			return m
		}
	}
	return executeCheck(m, []string{})
}

func rebuildTree(m Model) Model {
	counts := make(map[string]map[string]int)

	for _, r := range m.results {
		if counts[r.Device] == nil {
			counts[r.Device] = make(map[string]int)
		}
		counts[r.Device][r.Type]++
	}

	var items []components.TreeItem
	for device, types := range counts {
		total := 0
		for _, v := range types {
			total += v
		}
		items = append(items, components.TreeItem{
			ID:       "device:" + device,
			Label:    device,
			Depth:    0,
			Count:    total,
			Expanded: true,
		})
		for ltype, count := range types {
			items = append(items, components.TreeItem{
				ID:       "type:" + device + ":" + ltype,
				Label:    ltype,
				Depth:    1,
				Count:    count,
				ParentID: "device:" + device,
			})
		}
	}

	m.tree.SetItems(items)
	return m
}

func syncTableFromTree(m Model) Model {
	item := m.tree.SelectedItem()
	if item == nil {
		return m
	}

	var filtered []CheckResult
	for _, r := range m.results {
		match := false
		if strings.HasPrefix(item.ID, "device:") {
			dev := strings.TrimPrefix(item.ID, "device:")
			if r.Device == dev {
				match = true
			}
		} else if strings.HasPrefix(item.ID, "type:") {
			parts := strings.SplitN(strings.TrimPrefix(item.ID, "type:"), ":", 2)
			if len(parts) == 2 && r.Device == parts[0] && r.Type == parts[1] {
				match = true
			}
		}
		if match {
			filtered = append(filtered, r)
		}
	}

	m.table.SetResults(filtered)
	m.table.Cursor = 0
	m.table.Offset = 0
	m = syncDetailFromTable(m)
	return m
}

func syncDetailFromTable(m Model) Model {
	r := m.table.SelectedResult()
	if r != nil {
		chk := components.CheckResult(*r)
		m.detail.SetResult(&chk)
	} else {
		m.detail.SetResult(nil)
	}
	return m
}

func handleResize(m Model) Model {
	titleH := 1
	cmdH := 1
	statusH := 1
	panelH := m.height - titleH - cmdH - statusH

	leftW := m.width * 40 / 100
	rightW := m.width - leftW

	treeH := panelH * 3 / 5
	detailH := panelH - treeH

	m.tree.Width = leftW - 4
	m.tree.Height = treeH - 2
	m.detail.Width = leftW - 4
	m.detail.Height = detailH - 2
	m.table.Width = rightW - 4
	m.table.Height = panelH - 2
	m.input.Width = m.width - 4

	return m
}