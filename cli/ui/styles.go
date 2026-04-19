package ui

import "charm.land/lipgloss/v2"

var (
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7CFC00")).
			MarginBottom(1)

	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			BorderForeground(lipgloss.Color("#00BFFF"))

	HelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			MarginTop(1)

	SuccessStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7CFC00"))

	ErrorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF5F5F"))

	PromptStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFD166"))
)
