package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/library"
)

var (
	accent   = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	muted    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	okColor  = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	runColor = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	errColor = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(accent)
	mutedStyle    = lipgloss.NewStyle().Foreground(muted)
	errStyle      = lipgloss.NewStyle().Foreground(errColor)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	headingStyle  = lipgloss.NewStyle().Bold(true).Underline(true)
	paneStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(muted).Padding(0, 1)
	focusPane     = paneStyle.BorderForeground(accent)
	labelStyle    = lipgloss.NewStyle().Bold(true)
)

func statusBadge(s library.Status) string {
	switch s {
	case library.StatusDone:
		return lipgloss.NewStyle().Foreground(okColor).Render("● done     ")
	case library.StatusRendering:
		return lipgloss.NewStyle().Foreground(runColor).Render("◐ rendering")
	case library.StatusQueued:
		return lipgloss.NewStyle().Foreground(runColor).Render("○ queued   ")
	case library.StatusFailed:
		return errStyle.Render("✕ failed   ")
	default:
		return mutedStyle.Render("· draft    ")
	}
}
