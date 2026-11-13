package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// A line is what the cursor rests on. A pick row contributes a heading and
// one line per option; every other row is one line, except a choice row whose
// options wrap onto more. Rows are grouped into blocks with a blank line
// between them and, for a named group, a heading. Headings, blanks, notes and
// wrapped options are skipped by the cursor, so ↑/↓ only land where a key
// does something or, on a report, where there is more to read.
//
// Lines are the whole section. What is drawn is worked out from them: a long
// list shows a window of its options, and the focused line gets its
// description under it (see settingsDisplay).
type settingLine struct {
	row int
	// option indexes the row's options on a list, or is -1 for the row itself.
	option int
	header bool
	blank  bool
	// group heads a block of rows; note is a dim line of a rowNote.
	group bool
	note  bool
	// filter is a list's filter while one is typed; empty stands in for its
	// options when none match.
	filter bool
	empty  bool
	// wrap > 0 is a choice row's wrap-th continuation line.
	wrap int
}

func (l settingLine) focusable() bool {
	return !l.header && !l.blank && !l.group && !l.note && !l.filter && l.wrap == 0
}

// shortList is the longest list drawn whole; a longer one gets a window and
// can be filtered.
const shortList = 8

// expands reports whether a row draws its options under a heading. A list of
// one is not a choice, so it is drawn as a plain row.
func (r settingRow) expands() bool { return r.kind == rowPick && len(r.options) > 1 }

func (r settingRow) long() bool { return r.expands() && len(r.options) > shortList }

// settingsFilter is a typed filter on one long list.
type settingsFilter struct {
	on   bool
	row  string
	text string
}

func (f settingsFilter) matches(row settingRow, option string) bool {
	if !f.on || f.row != row.anchorName() || f.text == "" {
		return true
	}
	return strings.Contains(strings.ToLower(option), strings.ToLower(f.text))
}

func linesFor(rows []settingRow, valueWidth []int, f settingsFilter) []settingLine {
	var lines []settingLine
	blank := func(i int) {
		if n := len(lines); n > 0 && !lines[n-1].blank {
			lines = append(lines, settingLine{row: i, option: -1, blank: true})
		}
	}
	const afterList = "\x00list"
	group := ""
	for i, row := range rows {
		switch {
		case row.expands():
			blank(i)
			lines = append(lines, settingLine{row: i, option: -1, header: true})
			filtered := f.on && f.row == row.anchorName()
			if filtered {
				lines = append(lines, settingLine{row: i, option: -1, filter: true})
			}
			n := 0
			for j, o := range row.options {
				if f.matches(row, o) {
					lines = append(lines, settingLine{row: i, option: j})
					n++
				}
			}
			if filtered && n == 0 {
				lines = append(lines, settingLine{row: i, option: -1, empty: true})
			}
			group = afterList
			continue
		case row.gap || row.group != "" && row.group != group:
			blank(i)
		}
		if row.group != "" && row.group != group {
			lines = append(lines, settingLine{row: i, option: -1, group: true})
			group = row.group
		}
		if row.kind == rowNote {
			lines = append(lines, settingLine{row: i, option: -1, note: true})
			continue
		}
		lines = append(lines, settingLine{row: i, option: -1})
		for w := 1; w < len(row.valueLines(valueWidth[i])); w++ {
			lines = append(lines, settingLine{row: i, option: -1, wrap: w})
		}
	}
	for n := len(lines); n > 0 && lines[n-1].blank; n = len(lines) {
		lines = lines[:n-1]
	}
	return lines
}

// nameColumns is the width of each row's name column. The one-line rows of
// a block share one, so their values line up without a long name in one
// block pushing the values of another away.
func nameColumns(rows []settingRow) []int {
	block := make([]int, len(rows))
	n, group := 0, ""
	for i, row := range rows {
		switch {
		case row.expands():
			n, group = n+1, "\x00list"
		case row.gap || row.group != "" && row.group != group:
			n, group = n+1, row.group
		}
		block[i] = n
	}
	widths := map[int]int{}
	for i, row := range rows {
		if row.expands() || row.kind == rowReport || row.kind == rowNote {
			continue
		}
		w := lipgloss.Width(row.name)
		if row.indent {
			w += 2
		}
		widths[block[i]] = max(widths[block[i]], w)
	}
	out := make([]int, len(rows))
	for i := range rows {
		out[i] = min(max(widths[block[i]], 12), 36) + 2
	}
	return out
}

// rowIndent is where every row, option and note starts: the margin, the
// cursor and the mark.
const rowIndent = 6

func (a app) settingsValueWidths(rows []settingRow) []int {
	return valueWidths(rows, a.settingsContentWidth())
}

// valueWidths is the room each row has for its value, in a column width wide.
func valueWidths(rows []settingRow, width int) []int {
	names := nameColumns(rows)
	out := make([]int, len(rows))
	for i, n := range names {
		out[i] = width - rowIndent - n
	}
	return out
}

func (a app) settingsLayout() ([]settingRow, []settingLine) {
	rows := a.sectionRows()
	return rows, linesFor(rows, a.settingsValueWidths(rows), a.settings.filter)
}

// focusedSetting is the row under the cursor and, on a list, which option.
func (a app) focusedSetting() (settingRow, settingLine, bool) {
	rows, lines := a.settingsLayout()
	c := a.settings.cursor
	if c < 0 || c >= len(lines) || !lines[c].focusable() {
		return settingRow{}, settingLine{}, false
	}
	return rows[lines[c].row], lines[c], true
}

// lineAnchor names a line by what is drawn there rather than by index, so a
// list that finishes loading or a resize keeps the cursor on its line.
type lineAnchor struct {
	set    bool
	name   string
	option int
	// nth tells apart lines with the same name and option, such as the
	// lines of one report.
	nth int
}

func anchorAt(rows []settingRow, lines []settingLine, at int) lineAnchor {
	if at < 0 || at >= len(lines) || !lines[at].focusable() {
		return lineAnchor{}
	}
	l := lines[at]
	an := lineAnchor{set: true, name: rows[l.row].anchorName(), option: l.option}
	for i := range at {
		if lines[i].focusable() && rows[lines[i].row].anchorName() == an.name && lines[i].option == an.option {
			an.nth++
		}
	}
	return an
}

// findAnchor returns an's line. When it is gone it falls back to the ✓ of a
// list with the same name (a loading line that became a list), then to any
// line of that name.
func findAnchor(rows []settingRow, lines []settingLine, an lineAnchor) (int, bool) {
	if !an.set {
		return 0, false
	}
	n, sameName := 0, -1
	for i, l := range lines {
		if !l.focusable() || rows[l.row].anchorName() != an.name {
			continue
		}
		if l.option == an.option {
			if n == an.nth {
				return i, true
			}
			n++
		}
		if sameName < 0 || l.option >= 0 && l.option == rows[l.row].current {
			sameName = i
		}
	}
	return sameName, sameName >= 0
}

// homeLine is where a section opens the first time: on the ✓ of its first
// list, or on its first line when no list comes before the other rows. A
// report is passed over while something below it can be acted on.
func homeLine(rows []settingRow, lines []settingLine) int {
	first := -1
	for i, l := range lines {
		if !l.focusable() {
			continue
		}
		if first < 0 {
			first = i
		}
		if rows[l.row].kind == rowReport {
			continue
		}
		if l.option < 0 {
			return i
		}
		for j := i; j < len(lines) && lines[j].row == l.row; j++ {
			if lines[j].option == rows[l.row].current {
				return j
			}
		}
		return i
	}
	return max(first, 0)
}

// nearestFocusable is at, or the closest focusable line below it, or above.
func nearestFocusable(lines []settingLine, at int) int {
	if len(lines) == 0 {
		return 0
	}
	at = min(max(at, 0), len(lines)-1)
	for i := at; i < len(lines); i++ {
		if lines[i].focusable() {
			return i
		}
	}
	for i := at; i >= 0; i-- {
		if lines[i].focusable() {
			return i
		}
	}
	return at
}

// syncSettings puts the cursor back on the current section's anchored line
// after anything that may have changed the section's shape.
func (a *app) syncSettings() {
	s := &a.settings
	rows, lines := a.settingsLayout()
	i, ok := findAnchor(rows, lines, s.marks[s.section])
	if !ok {
		i = nearestFocusable(lines, s.cursor)
	}
	a.placeSettingsCursor(rows, lines, i)
}

func (a *app) placeSettingsCursor(rows []settingRow, lines []settingLine, i int) {
	s := &a.settings
	s.rowCursor.place(rows, lines, i, a.settingsFrame())
	s.marks[s.section] = anchorAt(rows, lines, s.cursor)
}

// place puts the cursor on line i, or the nearest line it can rest on, and
// scrolls it into view.
func (c *rowCursor) place(rows []settingRow, lines []settingLine, i int, f rowFrame) {
	c.cursor = nearestFocusable(lines, i)
	if at := c.cursor; at < len(lines) && lines[at].option >= 0 {
		if w, ok := listWindowsIn(rows, lines, *c, f)[lines[at].row]; ok {
			c.window = listTop{name: rows[lines[at].row].anchorName(), start: w.start, set: true}
		}
	}
	c.ensureVisible(rows, lines, f)
}

func (a *app) moveSettingsCursor(delta int) {
	rows, lines := a.settingsLayout()
	a.placeSettingsCursor(rows, lines, a.settings.rowCursor.step(rows, lines, delta))
}

// step is the line |delta| focusable lines away, stopping at either end.
// While a filter is typed it stays among the filtered options.
func (c rowCursor) step(rows []settingRow, lines []settingLine, delta int) int {
	step := 1
	if delta < 0 {
		step, delta = -1, -delta
	}
	stay := func(int) bool { return true }
	if f := c.filter; f.on {
		stay = func(i int) bool { return rows[lines[i].row].anchorName() == f.row }
	}
	at := c.cursor
	for ; delta > 0; delta-- {
		next := at + step
		for next >= 0 && next < len(lines) && !lines[next].focusable() {
			next += step
		}
		if next < 0 || next >= len(lines) || !stay(next) {
			break
		}
		at = next
	}
	return at
}

// gotoSection leaves the cursor where it was in the section it leaves and
// returns to where it was in the one it enters; a first visit opens at the
// section's home line. Entering a section refreshes what it loads.
func (a *app) gotoSection(sec settingsSection) tea.Cmd {
	if sec < 0 || sec >= settingsSectionCount {
		return nil
	}
	s := &a.settings
	s.section = sec
	s.offset = 0
	s.filter = settingsFilter{}
	s.message, s.failed = "", false
	cmd := a.enterSection(sec)
	rows, lines := a.settingsLayout()
	if !s.visited[sec] {
		s.visited[sec] = true
		a.placeSettingsCursor(rows, lines, homeLine(rows, lines))
	} else {
		a.syncSettings()
	}
	return cmd
}

func (a *app) switchSection(delta int) tea.Cmd {
	n := int(settingsSectionCount)
	return a.gotoSection(settingsSection(((int(a.settings.section)+delta)%n + n) % n))
}

// moveSettingsCursorToEnd puts the cursor on the first line of the section
// a key acts on, or the last when last is set. A section with nothing to act
// on goes to its first or last line.
func (a *app) moveSettingsCursorToEnd(last bool) {
	rows, lines := a.settingsLayout()
	actionable := func(l settingLine) bool {
		if f := a.settings.filter; f.on && rows[l.row].anchorName() != f.row {
			return false
		}
		k := rows[l.row].kind
		return l.focusable() && (l.option >= 0 || k != rowInfo && k != rowReport)
	}
	at := slices.IndexFunc(lines, actionable)
	if last {
		at = -1
		for i := len(lines) - 1; i >= 0 && at < 0; i-- {
			if actionable(lines[i]) {
				at = i
			}
		}
	}
	if at < 0 {
		at = a.settings.cursor
		if !a.settings.filter.on {
			at = 0
			if last {
				at = len(lines) - 1
			}
		}
	}
	a.placeSettingsCursor(rows, lines, at)
}

// activateSetting is enter: choose the option under the cursor, step a
// choice, edit a value, run an action.
func (a *app) activateSetting(row settingRow, line settingLine) tea.Cmd {
	if line.option >= 0 {
		if row.set == nil {
			return nil
		}
		return row.set(a, line.option)
	}
	if row.run != nil {
		return row.run(a)
	}
	switch row.kind {
	case rowPick:
		if len(row.options) == 1 && row.set != nil {
			return row.set(a, 0)
		}
	case rowChoice:
		return a.stepChoice(row, 1)
	case rowToggle:
		return a.stepChoice(row, 1-2*row.current)
	}
	return nil
}

// toggleSetting is space: a choice steps, a toggle flips; anything else
// ignores it.
func (a *app) toggleSetting(row settingRow, line settingLine) tea.Cmd {
	switch {
	case line.option >= 0:
	case row.kind == rowChoice:
		return a.stepChoice(row, 1)
	case row.kind == rowToggle:
		return a.stepChoice(row, 1-2*row.current)
	}
	return nil
}

// adjustSetting is + and -: nudge a slider or step a choice. It never moves
// along a list, where an option is chosen with enter.
func (a *app) adjustSetting(row settingRow, line settingLine, delta int) tea.Cmd {
	if line.option >= 0 {
		return nil
	}
	switch row.kind {
	case rowSlider:
		if row.nudge != nil {
			return row.nudge(a, delta)
		}
	case rowChoice:
		return a.stepChoice(row, delta)
	}
	return nil
}

// rowNote says something on the status line of the screen drawn from rows.
func (a *app) rowNote(msg string) {
	if a.screen == screenMaster {
		a.masterNote(msg)
		return
	}
	a.settingsNote(msg)
}

// stepChoice moves a choice row delta options along and stops at the ends.
func (a *app) stepChoice(row settingRow, delta int) tea.Cmd {
	n := len(row.options)
	if n == 0 || row.set == nil {
		return nil
	}
	next := 0
	if row.current >= 0 {
		next = row.current + delta
	} else if delta < 0 {
		next = n - 1
	}
	switch {
	case next < 0:
		a.rowNote(row.name + " is at its first option")
		return nil
	case next >= n:
		a.rowNote(row.name + " is at its last option")
		return nil
	}
	return row.set(a, next)
}
