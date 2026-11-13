package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/lancekrogers/songsmith/internal/library"
)

// Palette. Hex values paint as truecolor; under SONGSMITH_COLOR_PROFILE=ansi
// (VHS) lipgloss maps them onto the bright 16-color slots.
var (
	colorBg      = lipgloss.Color("#0d0d11")
	colorRaised  = lipgloss.Color("#16161a")
	colorAccent  = lipgloss.Color("#8BE9FD") // cyan
	colorDim     = lipgloss.Color("#6B7280") // muted gray
	colorNormal  = lipgloss.Color("#F8F8F2") // near-white
	colorStatus  = lipgloss.Color("#50FA7B") // green
	colorError   = lipgloss.Color("#FF5F57") // red
	colorSelect  = lipgloss.Color("#FF79C6") // pink
	colorWarning = lipgloss.Color("#FEBC2E") // amber
)

// forceColorProfile makes lipgloss emit color even when the terminal or a
// recorder reports a weak capability. NO_COLOR wins unless color is forced.
// It runs when the TUI starts rather than at package init, so piped CLI
// output such as `songsmith doctor` keeps the profile its terminal reports.
func forceColorProfile() {
	force := (os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0") ||
		(os.Getenv("FORCE_COLOR") != "" && os.Getenv("FORCE_COLOR") != "0") ||
		os.Getenv("SONGSMITH_COLOR_PROFILE") != ""
	if os.Getenv("NO_COLOR") != "" && !force {
		return
	}
	// Never block on an OSC background query under a bare PTY.
	lipgloss.SetHasDarkBackground(true)

	switch strings.ToLower(strings.TrimSpace(os.Getenv("SONGSMITH_COLOR_PROFILE"))) {
	case "truecolor", "24bit", "true":
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	case "ansi256", "256":
		lipgloss.SetColorProfile(termenv.ANSI256)
		return
	case "ansi", "16":
		lipgloss.SetColorProfile(termenv.ANSI)
		return
	case "ascii", "mono":
		lipgloss.SetColorProfile(termenv.Ascii)
		return
	}
	if force || os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}
	if lipgloss.ColorProfile() == termenv.Ascii {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

var (
	// headerStyle is a single-line title with no margin.
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	subtitleStyle = lipgloss.NewStyle().Foreground(colorDim)
	selectedStyle = lipgloss.NewStyle().Foreground(colorSelect).Bold(true)
	normalStyle   = lipgloss.NewStyle().Foreground(colorNormal)
	dimStyle      = lipgloss.NewStyle().Foreground(colorDim)
	statusStyle   = lipgloss.NewStyle().Foreground(colorStatus)
	errorStyle    = lipgloss.NewStyle().Foreground(colorError)
	warningStyle  = lipgloss.NewStyle().Foreground(colorWarning)

	headingStyle = lipgloss.NewStyle().Bold(true).Underline(true)
	labelStyle   = lipgloss.NewStyle().Bold(true)
	paneStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorDim).Padding(0, 1)
	focusPane    = paneStyle.BorderForeground(colorAccent)
)

func statusBadge(s library.Status) string {
	switch s {
	case library.StatusDone:
		return statusStyle.Render("● done     ")
	case library.StatusRendering:
		return warningStyle.Render("◐ rendering")
	case library.StatusQueued:
		return warningStyle.Render("○ queued   ")
	case library.StatusFailed:
		return errorStyle.Render("✕ failed   ")
	default:
		return dimStyle.Render("· draft    ")
	}
}

// footer is the dim key line every screen ends with. indent lines it up
// with the screen's content.
func footer(width int, indent string, keys ...string) string {
	return dimStyle.Render(ansi.Truncate(indent+strings.Join(keys, "   "), width, "…"))
}
