package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// A line is what the cursor rests on. A pick row contributes a heading, one
// line per option and a blank around it; every other row is one line, except
// a choice row whose options wrap onto more. Headings, blanks and wrapped
// options are skipped by the cursor, so ↑/↓ only land where a key does
// something or, on a report, where there is more to read.
type settingLine struct {
	row int
	// option indexes the row's options on a list, or is -1 for the row itself.
	option int
	header bool
	blank  bool
	// wrap > 0 is a choice row's wrap-th continuation line.
	wrap int
}

func (l settingLine) focusable() bool { return !l.header && !l.blank && l.wrap == 0 }

// expands reports whether a row draws its options under a heading.
func (r settingRow) expands() bool { return r.kind == rowPick && len(r.options) > 0 }

func linesFor(rows []settingRow, valueWidth int) []settingLine {
	var lines []settingLine
	blank := func(i int) {
		if n := len(lines); n > 0 && !lines[n-1].blank {
			lines = append(lines, settingLine{row: i, option: -1, blank: true})
		}
	}
	for i, row := range rows {
		if !row.expands() {
			lines = append(lines, settingLine{row: i, option: -1})
			for w := 1; w < len(row.valueLines(valueWidth)); w++ {
				lines = append(lines, settingLine{row: i, option: -1, wrap: w})
			}
			continue
		}
		blank(i)
		lines = append(lines, settingLine{row: i, option: -1, header: true})
		for j := range row.options {
			lines = append(lines, settingLine{row: i, option: j})
		}
		blank(i)
	}
	for n := len(lines); n > 0 && lines[n-1].blank; n = len(lines) {
		lines = lines[:n-1]
	}
	return lines
}

// nameColumn is the width of the name column one-line rows share.
func nameColumn(rows []settingRow) int {
	width := 0
	for _, row := range rows {
		if !row.expands() && row.kind != rowReport {
			width = max(width, lipgloss.Width(row.name))
		}
	}
	return min(max(width, 12), 26) + 2
}

func (a app) settingsValueWidth(rows []settingRow) int {
	return a.settingsContentWidth() - 4 - nameColumn(rows)
}

func (a app) settingsLayout() ([]settingRow, []settingLine) {
	rows := a.sectionRows()
	return rows, linesFor(rows, a.settingsValueWidth(rows))
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
	s.cursor = nearestFocusable(lines, i)
	s.marks[s.section] = anchorAt(rows, lines, s.cursor)
	a.ensureSettingsCursorVisible(lines)
}

// moveSettingsCursor moves |delta| focusable lines and stops at either end.
func (a *app) moveSettingsCursor(delta int) {
	rows, lines := a.settingsLayout()
	step := 1
	if delta < 0 {
		step, delta = -1, -delta
	}
	at := a.settings.cursor
	for ; delta > 0; delta-- {
		next := at + step
		for next >= 0 && next < len(lines) && !lines[next].focusable() {
			next += step
		}
		if next < 0 || next >= len(lines) {
			break
		}
		at = next
	}
	a.placeSettingsCursor(rows, lines, at)
}

// ensureSettingsCursorVisible scrolls so the cursor is on screen, keeping a
// list's heading above its first option.
func (a *app) ensureSettingsCursorVisible(lines []settingLine) {
	s := &a.settings
	visible := a.settingsListRows()
	top, bottom := s.cursor, s.cursor
	if top > 0 && top < len(lines) && lines[top-1].header {
		top--
	}
	for bottom >= 0 && bottom+1 < len(lines) && lines[bottom+1].wrap > 0 {
		bottom++
	}
	if bottom-top+1 > visible {
		// Too few rows for the heading or wrapped lines: the cursor's line wins.
		top, bottom = s.cursor, s.cursor
	}
	if bottom >= s.offset+visible {
		s.offset = bottom - visible + 1
	}
	if top < s.offset {
		s.offset = top
	}
	s.offset = max(min(s.offset, len(lines)-visible), 0)
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
		at = 0
		if last {
			at = len(lines) - 1
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
	switch row.kind {
	case rowChoice:
		return a.stepChoice(row, 1)
	case rowText, rowAction:
		if row.run != nil {
			return row.run(a)
		}
	}
	return nil
}

// toggleSetting is space: a choice steps; anything else ignores it.
func (a *app) toggleSetting(row settingRow, line settingLine) tea.Cmd {
	if line.option < 0 && row.kind == rowChoice {
		return a.stepChoice(row, 1)
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
		a.settingsNote(row.name + " is at its first option")
		return nil
	case next >= n:
		a.settingsNote(row.name + " is at its last option")
		return nil
	}
	return row.set(a, next)
}
