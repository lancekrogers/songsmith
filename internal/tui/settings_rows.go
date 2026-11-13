package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// A row is one setting. Sections build their rows from the running config and
// from state loaded in the background each time they are drawn, so building a
// row never does I/O. A pick row draws its list under a heading with ✓ on the
// current option, in a window when the list is long; every other kind is one
// line, name then value.
//
// The kind is the verb:
//
//	rowPick    one of a list: the options are drawn; enter on one chooses it
//	rowChoice  one of a few, drawn on the row: enter, space or +/- step it
//	rowSlider  a level: +/- nudge it, enter types an exact value
//	rowToggle  on or off: enter or space flips it
//	rowText    typed: enter opens the one-line editor
//	rowAction  does something: enter runs it
//	rowInfo    read-only
//	rowReport  one prewrapped line of a longer read-only text
//	rowNote    one dim line the cursor passes over
type rowKind int

const (
	rowPick rowKind = iota
	rowChoice
	rowSlider
	rowToggle
	rowText
	rowAction
	rowInfo
	rowReport
	rowNote
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
	// group heads the rows that share it; a row whose group differs from the
	// row before starts a new block. gap starts one without a heading.
	group string
	gap   bool
	// mark is drawn before the name, such as a check's ✓ or ✕; indent puts
	// the row under the one before.
	mark   string
	indent bool
	// desc is drawn dim under the row while it is focused; empty means help.
	desc string
	// count is drawn in a list's heading after its value, such as "3 installed".
	count string
	// extras are dim tails after a list's options; optionDescs stand in for
	// desc while that option is focused.
	extras      []string
	optionDescs []string
	// lo, hi and at place a slider; scale is its range as shown.
	lo, hi, at float64
	scale      string
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
	// anchor tells apart rows that share a name, such as Cancel rows, so
	// the cursor stays on the one it was on.
	anchor string
	// set applies option idx (pick, choice). run performs the row's own verb
	// (text, action). nudge moves a slider by delta steps. Each gets
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
	case sectionMaster:
		return a.masterSettingsRows()
	case sectionServer:
		return a.serverRows()
	case sectionAdvanced:
		return a.advancedRows()
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
	var rows []settingRow
	for _, l := range wrapText(text, width) {
		rows = append(rows, settingRow{name: name, kind: rowReport, value: l, style: style})
	}
	return rows
}

// noteRows wraps text to width as dim lines the cursor passes over.
func noteRows(name, text string, width int, style *lipgloss.Style) []settingRow {
	rows := styledReportRows(name, text, width, style)
	for i := range rows {
		rows[i].kind = rowNote
	}
	return rows
}

// description is what the focused row, or its option, is for.
func (r settingRow) description(option int) string {
	if option < 0 && r.kind == rowPick && len(r.options) == 1 {
		option = 0
	}
	if option >= 0 && option < len(r.optionDescs) && r.optionDescs[option] != "" {
		return r.optionDescs[option]
	}
	if r.desc != "" {
		return r.desc
	}
	if option >= 0 && option < len(r.details) && r.details[option] != "" {
		return r.details[option]
	}
	return r.help
}

func (r settingRow) anchorName() string {
	if r.anchor != "" {
		return r.anchor
	}
	return r.name
}
