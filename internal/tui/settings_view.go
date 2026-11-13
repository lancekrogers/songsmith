package tui

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The screen is a numbered sidebar of sections beside the section's lines,
// then a status line and the footer. Every section is on the sidebar, so any
// of them is one key away.

var groupStyle = lipgloss.NewStyle().Foreground(colorNormal).Bold(true)

func (a app) settingsWidth() int { return max(a.width, 20) }

func (a app) settingsCompact() bool { return a.height > 0 && a.height < 12 }

// settingsSidebarWidth is the sidebar's column, or 0 on a terminal too
// narrow to give the section lines room beside it.
func (a app) settingsSidebarWidth() int {
	if a.settingsWidth() < 60 {
		return 0
	}
	longest := 0
	for _, name := range sectionNames {
		longest = max(longest, lipgloss.Width(name))
	}
	return 2 + 2 + longest + 2
}

func (a app) settingsContentWidth() int {
	side := a.settingsSidebarWidth()
	if side == 0 {
		return a.settingsWidth()
	}
	return max(a.settingsWidth()-side-2, 20)
}

// statusLines is how many lines the status takes under the section.
const statusLines = 2

// settingsChrome is what surrounds the section lines. Full: title, rule,
// status and footer. Compact: title and one bottom line. On a narrow
// terminal a section strip stands in for the sidebar. A screen too short
// for all of it drops the rule, the title, the bottom lines, then the strip,
// so at least one section line is always drawn.
type settingsChrome struct {
	title, rule, strip bool
	bottom             int
}

func (c settingsChrome) lines() int {
	n := c.bottom
	for _, on := range []bool{c.title, c.rule, c.strip} {
		if on {
			n++
		}
	}
	return n
}

func (a app) settingsHeight() int {
	if a.height <= 0 {
		return 24
	}
	return a.height
}

func (a app) settingsChrome() settingsChrome {
	h := a.settingsHeight()
	c := settingsChrome{title: true, rule: !a.settingsCompact(), strip: a.settingsSidebarWidth() == 0, bottom: statusLines + 1}
	if a.settingsCompact() {
		c.bottom = 1
	}
	if c.lines() >= h {
		c.rule = false
	}
	if c.lines() >= h {
		c.title = false
	}
	for c.lines() >= h && c.bottom > 0 {
		c.bottom--
	}
	if c.lines() >= h {
		c.strip = false
	}
	return c
}

// settingsBodyRows is the height of the sidebar and section area.
func (a app) settingsBodyRows() int {
	return max(a.settingsHeight()-a.settingsChrome().lines(), 1)
}

// settingsListRows is how many section lines fit under the editor, if open.
func (a app) settingsListRows() int {
	rows := a.settingsBodyRows()
	if a.settings.edit.active {
		rows -= len(a.editorLines())
	}
	return max(rows, 1)
}

// editorLines are the one-line editor and what is under it: why the value
// was not saved, wrapped so a long path is read whole, or what the typed
// value is, or a blank line.
func (a app) editorLines() []string {
	return editorView(a.settings.edit, a.settingsContentWidth())
}

// editorView is a one-line editor and the line under it, in a column width wide.
func editorView(e settingsEditor, width int) []string {
	out := []string{"  " + selectedStyle.Render(e.label+": ") + e.input.View()}
	switch {
	case e.note != "":
		style := warningStyle
		if e.noteFailed {
			style = errorStyle
		}
		for _, l := range wrapText(e.note, width-4) {
			out = append(out, "    "+style.Render(l))
		}
	case e.hint != "":
		out = append(out, "    "+dimStyle.Render(e.hint))
	}
	return append(out, "")
}

func (a app) settingsView() string {
	if a.settings.text != nil {
		return a.settingsTextView()
	}
	width := a.settingsWidth()
	compact := a.settingsCompact()
	body := a.settingsBodyRows()
	chrome := a.settingsChrome()

	var parts []string
	if chrome.title {
		parts = append(parts, headerStyle.Render("  ♪ Settings"))
	}
	if chrome.rule {
		parts = append(parts, dimStyle.Render(strings.Repeat("─", width)))
	}
	side := a.settingsSidebarWidth()
	if chrome.strip {
		parts = append(parts, a.sectionStrip())
	}

	content := a.settingsContent()
	if len(content) > body {
		content = content[:body]
	}
	for len(content) < body {
		content = append(content, "")
	}
	if side > 0 {
		bar := a.sidebarLines(body)
		for i := range content {
			content[i] = padRight(bar[i], side) + dimStyle.Render("│ ") + content[i]
		}
	}
	parts = append(parts, content...)

	keys := a.settingsKeys()
	var bottom []string
	switch {
	case compact && a.settings.message != "":
		bottom = []string{a.messageStyle().Render("  " + a.settings.message)}
	case compact && a.focusedHelp() != "":
		// Without room for the status area, say when a change applies here.
		bottom = []string{dimStyle.Render("  " + a.focusedHelp())}
	case compact:
		bottom = []string{footer(width, "  ", keys...)}
	default:
		bottom = append(a.statusArea(width), footer(width, "  ", keys...))
	}
	parts = append(parts, bottom[:min(chrome.bottom, len(bottom))]...)
	for i := range parts {
		parts[i] = ansi.Truncate(parts[i], max(a.width, 0), "…")
	}
	return strings.Join(parts, "\n")
}

func (a app) messageStyle() lipgloss.Style {
	if a.settings.failed {
		return errorStyle
	}
	return statusStyle
}

// statusArea is the latest message or, without one, when a change to the
// focused line applies, with whatever of its help the line under it could
// not show, wrapped to statusLines lines.
// A new install folder waiting for a restart takes the first line until then.
func (a app) statusArea(width int) []string {
	var out []string
	if a.settings.restartDir != "" {
		line := "↻ " + restartNote + " · after restart: " + a.afterRestart()
		out = append(out, "  "+warningStyle.Render(ansi.Truncate(line, max(width-4, 1), "…")))
	}
	text, style := a.settings.message, a.messageStyle()
	if text == "" {
		text, style = a.statusHelp(), dimStyle
	}
	n := statusLines - len(out)
	wrapped := strings.Split(lipgloss.NewStyle().Width(max(width-4, 10)).Render(text), "\n")
	for i := range n {
		if i >= len(wrapped) {
			out = append(out, "")
			continue
		}
		line := strings.TrimRight(wrapped[i], " ")
		if i == n-1 && len(wrapped) > n {
			line = ansi.Truncate(line+" "+strings.Join(wrapped[i+1:], " "), width-4, "…")
		}
		out = append(out, "  "+style.Render(line))
	}
	return out
}

// focusedHelp says when a change applies, then what the line is for.
func (a app) focusedHelp() string {
	row, line, ok := a.focusedSetting()
	if !ok {
		return ""
	}
	return joinHelp(row.applies, optionHelp(row, line.option))
}

func optionHelp(row settingRow, option int) string {
	if option < 0 && row.kind == rowPick && len(row.options) == 1 {
		option = 0
	}
	if option >= 0 && option < len(row.details) && row.details[option] != "" {
		return row.details[option]
	}
	return row.help
}

func joinHelp(applies, help string) string {
	var parts []string
	if applies != "" {
		parts = append(parts, capitalize(applies))
	}
	if help != "" {
		parts = append(parts, help)
	}
	return strings.Join(parts, " · ")
}

// statusHelp is focusedHelp less what the line under the row already says.
func (a app) statusHelp() string {
	row, line, ok := a.focusedSetting()
	if !ok {
		return ""
	}
	help := optionHelp(row, line.option)
	rows, lines := a.settingsLayout()
	if desc := a.focusedDescription(rows, lines); desc == help && a.descFits(desc, row.indent) {
		help = ""
	}
	return joinHelp(row.applies, help)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// settingsKeys names the keys that do something on the focused line.
func (a app) settingsKeys() []string {
	if a.settings.edit.active {
		if a.settings.edit.complete != nil {
			return []string{"enter save", "tab complete", "esc cancel"}
		}
		return []string{"enter save", "esc cancel"}
	}
	if a.settings.filter.on {
		return []string{"type to filter", "↑/↓ move", "enter choose", "esc clear"}
	}
	keys := []string{"↑/↓ move", "←/→ section"}
	if row, line, ok := a.focusedSetting(); ok {
		switch {
		case line.option >= 0 || row.kind == rowPick && len(row.options) == 1 && row.set != nil:
			keys = append(keys, "enter choose")
			if row.long() {
				keys = append(keys, "/ filter")
			}
		case row.kind == rowChoice && row.run != nil:
			keys = append(keys, "space/+/- step", "enter type")
		case row.kind == rowChoice:
			keys = append(keys, "enter/space/+/- step")
		case row.kind == rowToggle:
			keys = append(keys, "enter/space toggle")
		case row.kind == rowSlider:
			keys = append(keys, "+/- adjust", "enter type")
		case row.kind == rowText:
			keys = append(keys, "enter edit")
		case row.kind == rowAction && row.run != nil:
			verb := row.verb
			if verb == "" {
				verb = "run"
			}
			keys = append(keys, "enter "+verb)
		}
	}
	return append(keys, "esc back")
}

func (a app) sidebarLines(rows int) []string {
	n := int(settingsSectionCount)
	start := 0
	if n > rows {
		start = min(max(int(a.settings.section)-rows/2, 0), n-rows)
	}
	out := make([]string, rows)
	for i := range out {
		s := start + i
		if s >= n {
			continue
		}
		label := fmt.Sprintf("%d %s", s+1, sectionNames[s])
		if settingsSection(s) == a.settings.section {
			out[i] = "  " + selectedStyle.Render(label)
		} else {
			out[i] = "  " + dimStyle.Render(label)
		}
	}
	return out
}

// sectionStrip stands in for the sidebar on a narrow terminal.
func (a app) sectionStrip() string {
	s := a.settings.section
	return "  " + dimStyle.Render("‹ ") + selectedStyle.Render(fmt.Sprintf("%d %s", int(s)+1, sectionNames[s])) +
		dimStyle.Render(fmt.Sprintf(" ›  %d/%d", int(s)+1, settingsSectionCount))
}

func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-lipgloss.Width(s), 0))
}

// settingsContent is the visible part of the section, under the editor
// while one is open.
func (a app) settingsContent() []string {
	var out []string
	if a.settings.edit.active {
		out = append(out, a.editorLines()...)
	}
	rows, lines := a.settingsLayout()
	return append(out, rowContent(rows, lines, a.settings.rowCursor, a.settingsFrame())...)
}

// rowContent is the visible part of rows, in a frame.
func rowContent(rows []settingRow, lines []settingLine, c rowCursor, f rowFrame) []string {
	var out []string
	disp, _ := rowDisplay(rows, lines, c, f)
	nameWidth := nameColumns(rows)
	valueWidth := valueWidths(rows, f.width)
	visible := f.height
	start := min(max(c.offset, 0), max(len(disp)-visible, 0))
	end := min(start+visible, len(disp))
	cw := f.width
	pad := strings.Repeat(" ", rowIndent)
	for _, d := range disp[start:end] {
		var s string
		switch d.kind {
		case displayMore:
			s = pad + dimStyle.Render(d.text)
		case displayDesc:
			p := pad
			if d.indent {
				p += "  "
			}
			s = p + dimStyle.Render(ansi.Truncate(d.text, max(cw-len(p), 1), "…"))
		default:
			s = rowLineView(rows, lines, d.line, c, f, nameWidth, valueWidth)
		}
		out = append(out, ansi.Truncate(s, cw, "…"))
	}
	return out
}

func rowLineView(rows []settingRow, lines []settingLine, i int, c rowCursor, f rowFrame, nameWidths, valueWidths []int) string {
	l := lines[i]
	row := rows[l.row]
	nameWidth, valueWidth := nameWidths[l.row], valueWidths[l.row]
	focused := i == c.cursor && !f.editing
	pad := strings.Repeat(" ", rowIndent)
	switch {
	case l.blank:
		return ""
	case l.header:
		return headerLine(row, len(row.options))
	case l.group:
		return "  " + groupStyle.Render(row.group)
	case l.filter:
		flt := c.filter
		n := 0
		for _, o := range row.options {
			if flt.matches(row, o) {
				n++
			}
		}
		return pad + selectedStyle.Render("/ "+flt.text) + normalStyle.Render("▏") +
			dimStyle.Render(fmt.Sprintf("  %d of %d", n, len(row.options)))
	case l.empty:
		cursor := "  "
		if focused {
			cursor = selectedStyle.Render("▸ ")
		}
		return "  " + cursor + "  " + dimStyle.Render("nothing matches "+quoted(c.filter.text))
	case l.note:
		style := dimStyle
		if row.style != nil {
			style = *row.style
		}
		if row.indent {
			pad += "  "
		}
		return pad + style.Render(row.value)
	case l.option >= 0:
		return optionLine(row, l.option, focused)
	case l.wrap > 0:
		return wrapLine(row, l.wrap, nameWidth, valueWidth)
	}
	return rowLine(row, focused, nameWidth, valueWidth)
}

// headerLine heads a list with its name, its current value, how many
// options it has and its detail.
func headerLine(row settingRow, n int) string {
	line := "  " + groupStyle.Render(row.name)
	value := row.value
	if value == "" && row.current >= 0 && row.current < n {
		value = row.options[row.current]
	}
	if value != "" {
		line += dimStyle.Render(" · " + value)
	}
	if row.count != "" {
		line += dimStyle.Render(" · " + row.count)
	}
	if row.detail != "" {
		line += "  " + dimStyle.Render(row.detail)
	}
	return line
}

// optionLine is one option of a list: ✓ on the current one, dim when muted,
// with its dim extra after it.
func optionLine(row settingRow, idx int, focused bool) string {
	cursor, style := "  ", normalStyle
	if idx < len(row.muted) && row.muted[idx] {
		style = dimStyle
	}
	if focused {
		cursor, style = selectedStyle.Render("▸ "), selectedStyle
	}
	mark := "  "
	if idx == row.current {
		mark = statusStyle.Render("✓ ")
	}
	line := "  " + cursor + mark + style.Render(row.options[idx])
	if idx < len(row.extras) && row.extras[idx] != "" {
		line += "  " + dimStyle.Render(row.extras[idx])
	}
	return line
}

// rowLine is a one-line row: its mark, its name, then its value drawn so the
// verb is clear before a key is pressed.
func rowLine(row settingRow, focused bool, nameWidth, valueWidth int) string {
	cursor, nameStyle := "  ", normalStyle
	if row.kind == rowInfo {
		nameStyle = dimStyle
	}
	if focused {
		cursor, nameStyle = selectedStyle.Render("▸ "), selectedStyle
	}
	mark := "  "
	if row.mark != "" {
		mark = row.mark + " "
	}
	if row.kind == rowReport {
		style := dimStyle
		if row.style != nil {
			style = *row.style
		}
		return "  " + cursor + mark + style.Render(row.value)
	}
	name, nw := row.name, nameWidth
	if row.indent {
		name, nw = "  "+name, nameWidth
	}
	if lipgloss.Width(name) > nw-1 {
		name = ansi.Truncate(name, nw-2, "…")
	}
	gap := max(nw-lipgloss.Width(name), 1)
	values := row.valueLines(valueWidth)
	if row.kind != rowChoice {
		values = []string{rowValue(row)}
	}
	line := "  " + cursor + mark + nameStyle.Render(name) + strings.Repeat(" ", gap) + values[0]
	if row.detail != "" && len(values) == 1 {
		line += "  " + dimStyle.Render(row.detail)
	}
	return line
}

func wrapLine(row settingRow, wrap, nameWidth, valueWidth int) string {
	values := row.valueLines(valueWidth)
	if wrap >= len(values) {
		return ""
	}
	line := strings.Repeat(" ", rowIndent+nameWidth) + values[wrap]
	if row.detail != "" && wrap == len(values)-1 {
		line += "  " + dimStyle.Render(row.detail)
	}
	return line
}

func rowValue(row settingRow) string {
	switch row.kind {
	case rowPick:
		if len(row.options) == 1 {
			if len(row.muted) > 0 && row.muted[0] {
				return dimStyle.Render(row.options[0])
			}
			return normalStyle.Render(row.options[0])
		}
		return dimStyle.Render(row.value)
	case rowSlider:
		return sliderValue(row)
	case rowToggle:
		if row.current == 1 {
			return statusStyle.Render("ON")
		}
		return dimStyle.Render("OFF")
	case rowInfo:
		return dimStyle.Render(shortPath(row.value))
	case rowText:
		if looksLikePath(row.value) {
			return dimStyle.Render(shortPath(row.value))
		}
	}
	return normalStyle.Render(row.value)
}

// sliderBar is the width of a slider's bar.
const sliderBar = 13

// sliderValue is a slider's value, a bar showing where it sits in its
// range, and the range.
func sliderValue(row settingRow) string {
	pos := 0
	if row.hi > row.lo {
		f := min(max((row.at-row.lo)/(row.hi-row.lo), 0), 1)
		pos = int(math.Round(f * float64(sliderBar-1)))
	}
	bar := normalStyle.Render(strings.Repeat("━", pos)) + headerStyle.Render("●") +
		dimStyle.Render(strings.Repeat("━", sliderBar-1-pos))
	return normalStyle.Render(padRight(row.value, 5)) + bar + "  " + dimStyle.Render(row.scale)
}

func looksLikePath(v string) bool {
	return strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~") || strings.Contains(v, "://")
}

// shortPath writes a path under the home folder with ~.
func shortPath(v string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return v
	}
	if rest, ok := strings.CutPrefix(v, home+string(os.PathSeparator)); ok {
		return "~/" + rest
	}
	return v
}

// valueLines is a choice row's options with [current] marked, wrapped to
// width so every option stays on screen; any other row is one line.
func (r settingRow) valueLines(width int) []string {
	if r.kind != rowChoice {
		return []string{""}
	}
	parts := make([]string, 0, len(r.options)+1)
	if r.current < 0 && r.value != "" {
		parts = append(parts, normalStyle.Render(r.value)+dimStyle.Render(" ·"))
	}
	for i, option := range r.options {
		if i == r.current {
			parts = append(parts, statusStyle.Render("["+option+"]"))
		} else {
			parts = append(parts, dimStyle.Render(" "+option+" "))
		}
	}
	if width <= 0 || lipgloss.Width(strings.Join(parts, " ")) <= width {
		return []string{strings.Join(parts, " ")}
	}
	var lines []string
	line, used := "", 0
	for _, part := range parts {
		w := lipgloss.Width(part)
		if used > 0 && used+1+w > width {
			lines = append(lines, line)
			line, used = "", 0
		}
		if used > 0 {
			line += " "
			used++
		}
		line += part
		used += w
	}
	return append(lines, line)
}

func (a app) settingLineView(rows []settingRow, lines []settingLine, i int, nameWidths, valueWidths []int) string {
	return rowLineView(rows, lines, i, a.settings.rowCursor, a.settingsFrame(), nameWidths, valueWidths)
}
