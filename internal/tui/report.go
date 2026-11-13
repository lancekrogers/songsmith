package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/health"
)

// reportModel shows the setup checks. It stands in for the Settings screen
// and the setup flow until those exist.
type reportModel struct {
	title string
	// setup is set when the screen was opened to fix a failing check.
	setup bool
	// offset is the first body line shown when the report is taller than
	// the terminal.
	offset int
}

func (a app) updateReport(msg tea.Msg) (app, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	body, avail := a.reportBody(), a.reportRows()
	last := max(0, len(body)-avail)
	switch key.String() {
	case "esc":
		return a, func() tea.Msg { return switchScreenMsg(a.back()) }
	case "r":
		if !a.health.running {
			a.report.offset = 0
			return a.startHealth()
		}
	case "up", "k":
		a.report.offset = max(0, a.report.offset-1)
	case "down", "j":
		a.report.offset = min(last, a.report.offset+1)
	case "pgup":
		a.report.offset = max(0, a.report.offset-avail)
	case "pgdown":
		a.report.offset = min(last, a.report.offset+avail)
	}
	return a, nil
}

// reportRows is how many body lines fit under the header and above the footer.
func (a app) reportRows() int { return max(1, a.height-6) }

func (a app) reportBody() []string {
	inner := max(20, a.width-4)
	var lines []string
	switch {
	case a.health.running:
		lines = append(lines, dimStyle.Render("Checking ComfyUI, the music model files, Ollama and ffprobe…"), "")
	case a.report.setup:
		if _, blocked := health.Blocker(a.health.checks); !blocked {
			lines = append(lines, statusStyle.Render("✓ Nothing blocks rendering."), "")
		} else {
			if head, fix, ok := missingSummary(a.health.checks); ok {
				lines = append(lines, errorStyle.Render(head), dimStyle.Render(fix))
			}
			lines = append(lines, "Each failed check says how to fix it. Press r to check again once it is fixed.", "")
		}
	}
	nameW := 24
	detailW := max(10, inner-5-nameW-1)
	indent := strings.Repeat(" ", 5+nameW+1)
	for _, c := range a.health.checks {
		label := statusStyle.Render("OK  ")
		switch c.Result {
		case health.Warn:
			label = warningStyle.Render("WARN")
		case health.Fail:
			label = errorStyle.Render("FAIL")
		}
		detail := strings.Split(lipgloss.NewStyle().Width(detailW).Render(c.Detail), "\n")
		lines = append(lines, fmt.Sprintf("%s %-*s %s", label, nameW, ansi.Truncate(c.Name, nameW, "…"), detail[0]))
		for _, d := range detail[1:] {
			lines = append(lines, indent+d)
		}
	}
	return lines
}

func (a app) reportView() string {
	inner := max(20, a.width-4)
	lines := []string{
		headerStyle.Render("♪ " + a.report.title),
		subtitleStyle.Render(ansi.Truncate("Config file: "+a.d.Config.Path(), inner, "…")),
		"",
	}
	body, avail := a.reportBody(), a.reportRows()
	var keys []string
	if len(body) > avail {
		start := min(a.report.offset, len(body)-avail)
		body = body[start : start+avail]
		keys = append(keys, "↑/↓ scroll")
	}
	if !a.health.running {
		keys = append(keys, "r re-run checks")
	}
	keys = append(keys, "esc back")
	lines = append(append(lines, body...), "", footer(inner, "", keys...))
	return lipgloss.NewStyle().Padding(1, 2, 0).Render(strings.Join(lines, "\n"))
}
