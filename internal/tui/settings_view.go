package tui

import (
	"fmt"
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
		rows -= 2
	}
	return max(rows, 1)
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

// statusArea is the latest message or, without one, what the focused line
// does and when a change applies, wrapped to statusLines lines.
func (a app) statusArea(width int) []string {
	text, style := a.settings.message, a.messageStyle()
	if text == "" {
		text, style = a.focusedHelp(), dimStyle
	}
	wrapped := strings.Split(lipgloss.NewStyle().Width(max(width-4, 10)).Render(text), "\n")
	out := make([]string, statusLines)
	for i := range out {
		if i < len(wrapped) {
			line := strings.TrimRight(wrapped[i], " ")
			if i == statusLines-1 && len(wrapped) > statusLines {
				line = ansi.Truncate(line+" "+wrapped[i+1], width-4, "…")
			}
			out[i] = "  " + style.Render(line)
		}
	}
	return out
}

// focusedHelp says when a change applies, then what the line is for.
func (a app) focusedHelp() string {
	row, line, ok := a.focusedSetting()
	if !ok {
		return ""
	}
	help := row.help
	if line.option >= 0 && line.option < len(row.details) && row.details[line.option] != "" {
		help = row.details[line.option]
	}
	var parts []string
	if row.applies != "" {
		parts = append(parts, capitalize(row.applies))
	}
	if help != "" {
		parts = append(parts, help)
	}
	return strings.Join(parts, " · ")
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
		return []string{"enter save", "esc cancel"}
	}
	keys := []string{"↑/↓ move", "←/→ section"}
	if row, line, ok := a.focusedSetting(); ok {
		switch {
		case line.option >= 0:
			keys = append(keys, "enter choose")
		case row.kind == rowChoice:
			keys = append(keys, "enter/space/+/- step")
		case row.kind == rowSlider:
			keys = append(keys, "+/- adjust")
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

// settingsContent is the visible window of the section, under the editor
// while one is open.
func (a app) settingsContent() []string {
	var out []string
	if a.settings.edit.active {
		out = append(out, "  "+selectedStyle.Render(a.settings.edit.label+": ")+a.settings.edit.input.View(), "")
	}
	rows, lines := a.settingsLayout()
	nameWidth := nameColumn(rows)
	valueWidth := a.settingsValueWidth(rows)
	visible := a.settingsListRows()
	start := min(max(a.settings.offset, 0), max(len(lines)-visible, 0))
	end := min(start+visible, len(lines))
	cw := a.settingsContentWidth()
	for i := start; i < end; i++ {
		l := lines[i]
		focused := i == a.settings.cursor && !a.settings.edit.active
		var s string
		switch {
		case l.blank:
		case l.header:
			s = headerLine(rows[l.row])
		case l.option >= 0:
			s = optionLine(rows[l.row], l.option, focused)
		case l.wrap > 0:
			s = wrapLine(rows[l.row], l.wrap, nameWidth, valueWidth)
		default:
			s = rowLine(rows[l.row], focused, nameWidth, valueWidth)
		}
		out = append(out, ansi.Truncate(s, cw, "…"))
	}
	return out
}

// headerLine heads a list with its name, its value and its detail.
func headerLine(row settingRow) string {
	line := "  " + groupStyle.Render(row.name)
	if row.value != "" {
		line += dimStyle.Render(" · " + row.value)
	}
	if row.detail != "" {
		line += "  " + dimStyle.Render(row.detail)
	}
	return line
}

// optionLine is one option of a list: ✓ on the current one, dim when muted.
func optionLine(row settingRow, idx int, focused bool) string {
	cursor, style := "  ", normalStyle
	if idx < len(row.muted) && row.muted[idx] {
		style = dimStyle
	}
	if focused {
		cursor, style = "▸ ", selectedStyle
	}
	mark := "  "
	if idx == row.current {
		mark = statusStyle.Render("✓ ")
	}
	return "  " + cursor + mark + style.Render(row.options[idx])
}

// rowLine is a one-line row: its name, then its value drawn so the verb is
// clear before a key is pressed.
func rowLine(row settingRow, focused bool, nameWidth, valueWidth int) string {
	cursor, nameStyle := "  ", normalStyle
	if row.kind == rowInfo {
		nameStyle = dimStyle
	}
	if focused {
		cursor, nameStyle = "▸ ", selectedStyle
	}
	if row.kind == rowReport {
		style := dimStyle
		if row.style != nil {
			style = *row.style
		}
		return "  " + cursor + style.Render(row.value)
	}
	name := row.name
	if lipgloss.Width(name) > nameWidth-1 {
		name = ansi.Truncate(name, nameWidth-2, "…")
	}
	pad := max(nameWidth-lipgloss.Width(name), 1)
	values := row.valueLines(valueWidth)
	if row.kind != rowChoice {
		values = []string{rowValue(row)}
	}
	line := "  " + cursor + nameStyle.Render(name) + strings.Repeat(" ", pad) + values[0]
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
	line := strings.Repeat(" ", 4+nameWidth) + values[wrap]
	if row.detail != "" && wrap == len(values)-1 {
		line += "  " + dimStyle.Render(row.detail)
	}
	return line
}

func rowValue(row settingRow) string {
	switch row.kind {
	case rowPick:
		return dimStyle.Render(row.value)
	case rowSlider:
		return normalStyle.Render(row.value) + dimStyle.Render("  - / +")
	case rowInfo:
		return dimStyle.Render(row.value)
	}
	return normalStyle.Render(row.value)
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
