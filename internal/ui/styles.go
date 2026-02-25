package ui

import "github.com/charmbracelet/lipgloss"

var (
	TitleStyle    = lipgloss.NewStyle().Bold(true)
	DimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	HeaderStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")).Padding(0, 1)
	SelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("255"))
	RunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	StoppedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	FrozenStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	HelpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	ErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	PromptStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
	LabelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	ValueStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
)

// StatusDot returns a colored dot for the instance status.
func StatusDot(status string) string {
	switch status {
	case "Running":
		return RunningStyle.Render("●")
	case "Stopped":
		return StoppedStyle.Render("○")
	case "Frozen":
		return FrozenStyle.Render("◎")
	default:
		return DimStyle.Render("·")
	}
}
