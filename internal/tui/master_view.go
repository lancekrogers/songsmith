package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

// The screen is a title, the before → after meters, a rule, the rows, a
// status line and the footer.

func (a app) masterWidth() int { return max(a.width-2, 20) }

func (a app) masterHeight() int {
	if a.height <= 0 {
		return 24
	}
	return a.height
}

// masterHead is the title and the meters, wrapped to the screen.
func (a app) masterHead() []string {
	m := a.master
	w := a.masterWidth()
	if m.defaults {
		out := []string{headerStyle.Render("  ♪ Master") + dimStyle.Render("  your default chain")}
		for _, l := range wrapText("Changes save as you make them. "+master.Summary(m.settings), w-2) {
			out = append(out, "  "+dimStyle.Render(l))
		}
		return out
	}
	song, _ := a.masterSong()
	title := headerStyle.Render("  ♪ Master") + "  " + normalStyle.Render(song.Title)
	takes := a.masterTakes()
	if i := library.TakeIndex(takes, m.take); i > 0 {
		title += dimStyle.Render(fmt.Sprintf(" · %s · take %d of %d", m.take, i, len(takes)))
	}
	out := []string{ansi.Truncate(title, a.width, "…")}
	if m.unavailable != nil {
		return out
	}
	for _, l := range packItems(a.meterItems(), w-2, "   ") {
		out = append(out, "  "+l)
	}
	return out
}

// packItems lays items out in lines no wider than width, breaking only
// between items; an item wider than the line is wrapped on its own.
func packItems(items []string, width int, sep string) []string {
	var lines []string
	line := ""
	for _, it := range items {
		switch {
		case line == "":
			line = it
		case ansi.StringWidth(line+sep+it) <= width:
			line += sep + it
		default:
			lines = append(lines, line)
			line = it
		}
		if ansi.StringWidth(line) > width {
			wrapped := wrapText(line, width)
			lines = append(lines, wrapped[:len(wrapped)-1]...)
			line = wrapped[len(wrapped)-1]
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// meterItems are the before → after meters.
func (a app) meterItems() []string {
	m := a.master
	if m.measureErr != "" {
		return []string{errorStyle.Render("Could not measure " + m.take + ": " + m.measureErr)}
	}
	if !m.measured {
		return []string{warningStyle.Render("measuring " + m.take + "…")}
	}
	before, after, scope := m.before, m.after, m.afterScope
	label := "Whole take"
	switch scope {
	case scopeExcerpt:
		before = m.afterOrig
		label = fmt.Sprintf("Excerpt %s–%s", fmtDuration(m.afterAt), fmtDuration(m.afterAt+after.Duration))
	case scopeNone:
		label = "Whole take"
	}
	has := scope != scopeNone
	pair := func(name string, b, f float64, format func(float64) string, unit string) string {
		s := dimStyle.Render(name+" ") + normalStyle.Render(format(b))
		if has {
			s += dimStyle.Render(" → ") + statusStyle.Render(format(f))
		}
		if unit != "" {
			s += dimStyle.Render(" " + unit)
		}
		return s
	}
	db := func(x float64) string { return master.Signed(math.Round(x*10) / 10) }
	dc := func(x float64) string { return fmt.Sprintf("%+.3f", math.Round(x*1000)/1000+0) }
	items := []string{
		labelStyle.Render(label),
		pair("Loudness", before.Loudness, after.Loudness, db, "LUFS"),
		pair("Peak", before.TruePeak, after.TruePeak, db, "dBTP"),
		pair("Range", before.Range, after.Range, func(x float64) string { return master.Num(math.Round(x*10) / 10) }, "LU"),
		pair("Hiss", before.Hiss, after.Hiss, db, "dB"),
		pair("DC", before.DC, after.DC, dc, ""),
		pair("Floor", before.NoiseFloor, after.NoiseFloor, db, "dB"),
	}
	length := dimStyle.Render("Length ") + normalStyle.Render(fmtDuration(m.before.Duration))
	if out := m.settings.Length(m.before.Info); out != m.before.Duration {
		length += dimStyle.Render(" → ") + statusStyle.Render(fmtDuration(out))
	}
	if !has {
		items[0] += dimStyle.Render(" · before")
	}
	return append(items, length)
}

const masterStatusLines = 2

func (a app) masterFrame() rowFrame {
	rows := a.masterBodyRows()
	if a.master.edit.active {
		rows -= len(editorView(a.master.edit, a.masterWidth()))
	}
	rows = max(rows, 1)
	return rowFrame{height: rows, width: a.masterWidth(), desc: rows >= 6, editing: a.master.edit.active}
}

// masterBodyRows is the height between the rule and the status line.
func (a app) masterBodyRows() int {
	used := len(a.masterHead()) + 1 + masterStatusLines + a.masterFooterRows()
	return max(a.masterHeight()-used, 1)
}

func (a app) masterView() string {
	m := a.master
	parts := a.masterHead()
	parts = append(parts, dimStyle.Render(strings.Repeat("─", max(a.width, 1))))
	var body []string
	if m.edit.active {
		body = append(body, editorView(m.edit, a.masterWidth())...)
	}
	rows, lines := a.masterLayout()
	body = append(body, rowContent(rows, lines, m.rowCursor, a.masterFrame())...)
	parts = append(parts, fitLines(body, a.masterBodyRows())...)
	parts = append(parts, a.masterStatus()...)
	parts = append(parts, fitLines(footerRows(a.width, "  ", a.masterKeys()...), a.masterFooterRows())...)
	for i := range parts {
		parts[i] = ansi.Truncate(parts[i], max(a.width, 0), "…")
	}
	return strings.Join(parts, "\n")
}

// masterStatus is the latest message, or what the focused row is for when
// the line under it does not say it, wrapped to two lines.
func (a app) masterStatus() []string {
	m := a.master
	text, style := m.message, statusStyle
	if m.failed {
		style = errorStyle
	}
	if text == "" {
		style = dimStyle
		if row, line, ok := a.focusedMaster(); ok {
			help := optionHelp(row, line.option)
			rows, lines := a.masterLayout()
			if d := focusedDesc(rows, lines, m.rowCursor, a.masterFrame()); d == help && descFitsIn(d, row.indent, a.masterWidth()) {
				help = ""
			}
			text = joinHelp(row.applies, help)
		}
	}
	w := max(a.width-4, 10)
	wrapped := strings.Split(lipgloss.NewStyle().Width(w).Render(text), "\n")
	out := make([]string, masterStatusLines)
	for i := range out {
		if i >= len(wrapped) || text == "" {
			continue
		}
		line := strings.TrimRight(wrapped[i], " ")
		if i == masterStatusLines-1 && len(wrapped) > masterStatusLines {
			line = ansi.Truncate(line+" "+strings.Join(wrapped[i+1:], " "), w, "…")
		}
		out[i] = "  " + style.Render(line)
	}
	return out
}

// masterFooterRows is the lines the footer may take, counted on the longest
// set of keys so the rows above do not jump as the cursor moves.
func (a app) masterFooterRows() int {
	longest := []string{"↑/↓ move", "enter work on it", "c make it current", "space play master", "o play original", "[/] ±15 s", "ctrl+s master", "esc back"}
	return max(len(footerRows(a.width, "  ", longest...)), len(footerRows(a.width, "  ", a.masterKeys()...)))
}

// masterKeys names the keys that do something now.
func (a app) masterKeys() []string {
	m := a.master
	if m.edit.active {
		return []string{"enter save", "esc cancel"}
	}
	keys := []string{"↑/↓ move"}
	if row, line, ok := a.focusedMaster(); ok {
		switch {
		case row.anchor == anchorTakes && line.option >= 0:
			song, _ := a.masterSong()
			file := a.masterTakes()[line.option].File
			if file != m.take {
				keys = append(keys, "enter work on it")
			}
			if file != song.Audio {
				keys = append(keys, "c make it current")
			}
		case row.anchor == anchorPosition:
			keys = append(keys, "←/→ move", "enter type a time")
		case row.kind == rowChoice:
			keys = append(keys, "enter/+/- step")
		case row.kind == rowToggle:
			keys = append(keys, "enter toggle")
		case row.kind == rowSlider:
			keys = append(keys, "+/- adjust", "enter type")
		case row.kind == rowAction && row.run != nil:
			keys = append(keys, "enter "+row.verb)
		}
	}
	if !m.defaults && m.unavailable == nil {
		switch a.masterPlaying() {
		case excerptMaster:
			keys = append(keys, "space stop", "o original")
		case excerptOriginal:
			keys = append(keys, "space master", "o stop")
		default:
			keys = append(keys, "space play master", "o play original")
		}
		keys = append(keys, "[/] ±15 s")
		if a.canStartMastering() {
			keys = append(keys, "ctrl+s master")
		}
	}
	return append(keys, "esc back")
}
