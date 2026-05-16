package tui

import (
	"charm.land/lipgloss/v2"
)

var (
	// 主题色
	ColorPrimary   = lipgloss.Color("#7C3AED")
	ColorSecondary = lipgloss.Color("#A78BFA")
	ColorSuccess   = lipgloss.Color("#10B981")
	ColorWarning   = lipgloss.Color("#F59E0B")
	ColorError     = lipgloss.Color("#EF4444")
	ColorInfo      = lipgloss.Color("#3B82F6")
	ColorMuted     = lipgloss.Color("#6B7280")

	// 表面色
	ColorSurface     = lipgloss.Color("#1E1E2E")
	ColorSurfaceAlt  = lipgloss.Color("#181825")
	ColorOverlay     = lipgloss.Color("#313244")
	ColorBorder      = lipgloss.Color("#45475A")
	ColorText        = lipgloss.Color("#CDD6F4")
	ColorTextDim     = lipgloss.Color("#6C7086")
	ColorTextBright  = lipgloss.Color("#F5F5F5")

	// 选中态
	ColorSelected    = lipgloss.Color("#89B4FA")
	ColorSelectedBg  = lipgloss.Color("#45475A")
)

func initStyles() {
}

// TitleBar 样式
func TitleBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(ColorPrimary).
		Foreground(ColorTextBright).
		Padding(0, 2).
		Bold(true)
}

// TitleBarVersion 样式
func TitleBarVersionStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(ColorPrimary).
		Foreground(lipgloss.Color("240")).
		Padding(0, 2)
}

// LeftPanel 样式
func LeftPanelStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)
}

// RightPanel 样式
func RightPanelStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)
}

// SubPanel 样式（左下详情）
func SubPanelStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)
}

// CommandBar 样式
func CommandBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(ColorSurfaceAlt).
		Foreground(ColorText).
		Padding(0, 1)
}

// StatusBar 样式
func StatusBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(ColorOverlay).
		Foreground(ColorTextDim).
		Padding(0, 1)
}

// StatusInfo 样式（状态栏中的信息文字）
func StatusInfoStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(ColorInfo)
}

// StatusSuccess 样式
func StatusSuccessStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(ColorSuccess)
}

// StatusError 样式
func StatusErrorStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(ColorError)
}

// StatusWarning 样式
func StatusWarningStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(ColorWarning)
}

// HelpKey 样式
func HelpKeyStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(ColorSelected).
		Bold(true)
}

// HelpDesc 样式
func HelpDescStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(ColorTextDim)
}