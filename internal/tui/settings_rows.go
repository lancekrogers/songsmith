package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// A row is one setting. Sections build their rows from the running config and
// from state loaded in the background each time they are drawn, so building a
// row never does I/O. A pick row draws its whole list under a heading with ✓
// on the current option; every other kind is one line, name then value.
//
// The kind is the verb:
//
//	rowPick    one of a list: the options are drawn; enter on one chooses it
//	rowChoice  one of a few, drawn on the row: enter, space or +/- step it
//	rowToggle  on or off: enter or space flips it
//	rowSlider  a level: +/- nudge it
//	rowText    typed: enter opens the one-line editor
//	rowAction  does something: enter runs it
//	rowInfo    read-only
//	rowReport  one prewrapped line of a longer read-only text
type rowKind int

const (
	rowPick rowKind = iota
	rowChoice
	rowToggle
	rowSlider
	rowText
	rowAction
	rowInfo
	rowReport
)

// When a change takes effect, stated on every row that changes something.
const (
	appliesNow        = "applies now"
	appliesNextDraft  = "applies to the next draft"
	appliesNextRender = "applies to the next render"
	appliesNewSong    = "applies to the next new song"
	appliesRestart    = "applies after restart"
)

type settingRow struct {
	// key is the config key the row writes; "" for actions and info rows.
	key  string
	name string
	kind rowKind
	// value is what the row shows; detail is a dim tail after it.
	value  string
	detail string
	// options and current are the list behind a pick or choice row; current
	// is -1 when no option is the current value.
	options []string
	current int
	// muted options draw dim: choosing one does not change the config (it
	// would need a download first) and says so.
	muted []bool
	// details stands in for help while that option is focused.
	details []string
	applies string
	help    string
	// verb names what enter does on an action row, for the footer.
	verb string
	// style draws a report line in place of the dim default.
	style *lipgloss.Style
	// set applies option idx (pick, choice). run performs the row's own verb
	// (toggle, text, action). nudge moves a slider by delta steps. Each gets
	// the app to change, never a copy captured when the row was built.
	set   func(a *app, idx int) tea.Cmd
	run   func(a *app) tea.Cmd
	nudge func(a *app, delta int) tea.Cmd
}

func (a app) sectionRows() []settingRow {
	switch a.settings.section {
	case sectionSetup:
		return a.setupRows()
	case sectionWriter:
		return a.writerRows()
	case sectionMusicModel:
		return a.musicModelRows()
	case sectionRender:
		return a.renderRows()
	case sectionEngine:
		return a.engineRows()
	case sectionLibrary:
		return a.libraryRows()
	}
	return nil
}

// reportRows wraps text to width as report lines sharing name.
func reportRows(name, text string, width int) []settingRow {
	return styledReportRows(name, text, width, nil)
}

func styledReportRows(name, text string, width int, style *lipgloss.Style) []settingRow {
	wrapped := lipgloss.NewStyle().Width(max(width, 10)).Render(text)
	var rows []settingRow
	for _, l := range strings.Split(wrapped, "\n") {
		rows = append(rows, settingRow{name: name, kind: rowReport, value: strings.TrimRight(l, " "), style: style})
	}
	return rows
}
