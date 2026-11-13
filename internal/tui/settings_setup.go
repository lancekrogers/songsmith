package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/health"
)

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

func (a app) setupRows() []settingRow {
	width := a.settingsContentWidth() - 4
	var rows []settingRow
	if a.health.done {
		if b, blocked := blockerSummary(a.health.checks); blocked {
			rows = append(rows, styledReportRows("Blocked", "▲ "+b, width, &errorStyle)...)
		} else {
			rows = append(rows, styledReportRows("Blocked", "✓ Nothing blocks rendering", width, &statusStyle)...)
		}
	}
	if in, out := health.Missing(a.health.checks); a.health.done && len(in)+len(out) > 0 {
		text := "Missing: " + strings.Join(append(slices.Clone(in), out...), ", ")
		if len(in) > 0 {
			text += " · songsmith can install " + joinAnd(in)
		}
		if len(out) > 0 {
			text += " · get " + joinAnd(out) + " yourself"
		}
		rows = append(rows, reportRows("Missing", text, width)...)
	}
	if !a.health.done {
		rows = append(rows, settingRow{name: "Checks", kind: rowInfo, value: "checking ComfyUI, the music model files, Ollama and ffprobe…"})
	}
	for _, c := range a.setupChecks() {
		rows = append(rows, requirementRow(c))
	}
	rows = append(rows, a.rerunRow())
	if a.health.done {
		rows = append(rows, reportRows("Report", "Check report, as songsmith doctor prints it:", width)...)
		for _, c := range a.setupChecks() {
			rows = append(rows, checkReportRows(c, width)...)
		}
	}
	return rows
}

// setupChecks is the latest run's checks, with the node check marked skipped
// when the server was not running to answer it.
func (a app) setupChecks() []health.Check {
	checks := a.health.checks
	if !a.health.done {
		return nil
	}
	if _, ok := health.Find(checks, health.IDNodes); ok {
		return checks
	}
	if _, ok := health.Find(checks, health.IDServer); !ok {
		return checks
	}
	out := make([]health.Check, 0, len(checks)+1)
	for _, c := range checks {
		out = append(out, c)
		if c.ID == health.IDServer {
			out = append(out, health.Check{ID: health.IDNodes, Name: "ComfyUI nodes", Result: skipped,
				Detail: "checked while the ComfyUI server runs; start it in Settings → Music engine"})
		}
	}
	return out
}

// skipped is a check that was not run this time.
const skipped health.Result = -1

func checkState(r health.Result) string {
	switch r {
	case health.OK:
		return statusStyle.Render("OK     ")
	case health.Warn:
		return warningStyle.Render("WARN   ")
	case health.Fail:
		return errorStyle.Render("FAIL   ")
	default:
		return dimStyle.Render("skipped")
	}
}

// requirementRow is one setup check: its state, and who fixes it. A check an
// install fixes says songsmith can install it; once the install runs from
// here, this row becomes kind rowAction with verb "install" and a run that
// starts it, and nothing else about the row changes.
func requirementRow(c health.Check) settingRow {
	row := settingRow{name: c.Name, kind: rowInfo, value: checkState(c.Result), help: c.Detail}
	switch {
	case c.Result == health.OK || c.Result == skipped:
		row.detail = c.Detail
	case c.Fix != "":
		row.detail = "songsmith can install this"
		row.help = "songsmith can install this; the install action arrives with in-app setup (for now see docs/setup.md). " + c.Detail
	case c.ID == health.IDWriter:
		row.detail = "get Ollama from ollama.com; lyrics can still be written by hand"
	case c.Missing != "":
		row.detail = "songsmith cannot install this; get it yourself"
	default:
		row.detail = c.Detail
	}
	return row
}

func (a app) rerunRow() settingRow {
	row := settingRow{name: "Re-run checks", kind: rowAction, verb: "re-run", applies: appliesNow,
		help: "Checks ComfyUI, the music model files, the server, Ollama and ffprobe again"}
	if a.health.running {
		row.value = "checking… " + spinnerFrames[a.ticks%len(spinnerFrames)]
	}
	row.run = func(a *app) tea.Cmd {
		if a.health.running {
			a.settingsNote("The checks are already running")
			return nil
		}
		next, cmd := a.startHealth()
		*a = next
		a.settingsNote("Checking…")
		return cmd
	}
	return row
}

// checkReportRows is one check as songsmith doctor prints it, wrapped with
// a hanging indent.
func checkReportRows(c health.Check, width int) []settingRow {
	label := map[health.Result]string{health.OK: "OK  ", health.Warn: "WARN", health.Fail: "FAIL"}[c.Result]
	if c.Result == skipped {
		label = "SKIP"
	}
	text := fmt.Sprintf("%s %s: %s", label, c.Name, c.Detail)
	wrapped := strings.Split(lipgloss.NewStyle().Width(max(width-5, 10)).Render(text), "\n")
	rows := make([]settingRow, 0, len(wrapped))
	for i, l := range wrapped {
		if i > 0 {
			l = "     " + l
		}
		rows = append(rows, settingRow{name: "Report", kind: rowReport, value: strings.TrimRight(l, " ")})
	}
	return rows
}
