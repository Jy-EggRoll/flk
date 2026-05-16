package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

func view(m Model) string {
	if m.err != nil {
		return ""
	}

	titleH := 1
	cmdH := 1
	statusH := 1

	// 标题栏
	title := renderTitle(m)

	// 中间主区域
	panelH := m.height - titleH - cmdH - statusH
	mainContent := renderPanels(m, panelH)

	// 命令输入框
	cmdBar := renderCommandBar(m)

	// 状态栏
	statusBar := renderStatusBar(m)

	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		mainContent,
		cmdBar,
		statusBar,
	)
}

func renderTitle(m Model) string {
	titleText := fmt.Sprintf(" flk TUI v%s ", m.version)
	spacer := m.width - len(titleText) - 5
	if spacer < 1 {
		spacer = 1
	}

	rightInfo := fmt.Sprintf(" %d links | :cmd ", len(m.results))

	titleStyle := TitleBarStyle().Width(m.width)
	title := titleStyle.Render(titleText + strings.Repeat(" ", spacer) + rightInfo)

	return title
}

func renderPanels(m Model, panelH int) string {
	if panelH < 3 {
		return ""
	}

	leftW := m.width * 40 / 100
	rightW := m.width - leftW

	treeH := panelH * 3 / 5
	detailH := panelH - treeH

	treeBorder := LeftPanelStyle()
	if m.table.Focus == "tree" {
		treeBorder = treeBorder.BorderForeground(ColorSelected)
	}
	treeView := treeBorder.
		Width(leftW - 2).
		Height(treeH - 2).
		Render(renderTreeWithHeader(m))

	detailBorder := SubPanelStyle()
	if m.table.Focus == "tree" {
		detailBorder = detailBorder.BorderForeground(ColorSelected)
	}
	detailView := detailBorder.
		Width(leftW - 2).
		Height(detailH - 2).
		Render(renderDetailWithHeader(m))

	leftPanel := lipgloss.JoinVertical(lipgloss.Left, treeView, detailView)

	rightBorder := RightPanelStyle()
	if m.table.Focus == "table" {
		rightBorder = rightBorder.BorderForeground(ColorSelected)
	}
	rightView := rightBorder.
		Width(rightW - 2).
		Height(panelH - 2).
		Render(renderTableWithHeader(m))

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightView)
}

func renderTreeWithHeader(m Model) string {
	header := lipgloss.NewStyle().Bold(true).Foreground(ColorSelected).Render(" Devices / Types")
	return header + "\n" + m.tree.View()
}

func renderDetailWithHeader(m Model) string {
	header := lipgloss.NewStyle().Bold(true).Foreground(ColorSelected).Render(" Details")
	return header + "\n" + m.detail.View()
}

func renderTableWithHeader(m Model) string {
	header := lipgloss.NewStyle().Bold(true).Foreground(ColorSelected).Render(" Link Details")
	helpLine := lipgloss.NewStyle().Foreground(ColorTextDim).Render("  Tab:focus  ↑↓:nav  ::cmd  r:refresh  ?:help  q:quit")
	return header + "\n" + m.table.View() + "\n" + helpLine
}

func renderCommandBar(m Model) string {
	prompt := " > "
	inputLine := m.input.Value

	if m.input.Focused {
		runes := []rune(m.input.Value)
		before := string(runes[:m.input.Cursor])
		after := string(runes[m.input.Cursor:])
		inputLine = before + "█" + after
		prompt = ">:"
	}

	content := prompt + inputLine
	spacer := m.width - len([]rune(content)) - 2
	if spacer < 1 {
		spacer = 1
	}

	return CommandBarStyle().Width(m.width).Render(content + strings.Repeat(" ", spacer))
}

func renderStatusBar(m Model) string {
	left := ""
	right := ""

	if len(m.results) > 0 {
		var valid, invalid int
		for _, r := range m.results {
			if r.Valid {
				valid++
			} else {
				invalid++
			}
		}
		left = fmt.Sprintf(" %d total | %d ✓ | %d ✗", len(m.results), valid, invalid)
	}

	if m.status.Text != "" {
		if left != "" {
			left += " | "
		}
		left += m.status.Text
	}

	focus := ""
	switch m.table.Focus {
	case "tree":
		focus = "Focus: Tree"
	case "table":
		focus = "Focus: Table"
	}

	right = fmt.Sprintf(" %s | Tab:switch ↑↓:nav ::cmd r:refresh ", focus)

	pad := m.width - len([]rune(left)) - len([]rune(right)) - 2
	if pad < 1 {
		pad = 1
	}

	return StatusBarStyle().Width(m.width).Render(left + strings.Repeat(" ", pad) + right)
}