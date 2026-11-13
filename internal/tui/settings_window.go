package tui

import (
	"fmt"
	"slices"

	"github.com/charmbracelet/lipgloss"
)

// A long list is drawn in a window of its options that keeps one height
// while the cursor moves: at either end it shows one more option in place of
// the "↑ n more" or "↓ n more" line. The window of the list the cursor is in
// follows the cursor; any other long list centres on its ✓.

// listTop is where the focused list's window starts, kept between moves.
type listTop struct {
	set   bool
	name  string
	start int
}

// listWindow is the part of a list's (filtered) options that is drawn.
type listWindow struct {
	// opts are the list's option lines, in order.
	opts         []int
	start, count int
	up, down     bool
}

func (w listWindow) visible(pos int) bool { return pos >= w.start && pos < w.start+w.count }

const (
	maxWindow = 10
	minWindow = 3
)

// fitWindow places a window of height h over m options from start s so that
// position cur, when it is one, is drawn.
func fitWindow(m, h, s, cur int) listWindow {
	if m <= h {
		return listWindow{count: m}
	}
	last := m - h + 1
	shape := func(s int) listWindow {
		switch {
		case s <= 0:
			return listWindow{start: 0, count: h - 1, down: true}
		case s >= last:
			return listWindow{start: last, count: h - 1, up: true}
		}
		return listWindow{start: s, count: h - 2, up: true, down: true}
	}
	w := shape(min(max(s, 0), last))
	switch {
	case cur < 0 || w.visible(cur):
	case cur < w.start:
		w = shape(cur)
	case cur >= last:
		w = shape(last)
	default:
		w = shape(cur - (h - 3))
	}
	return w
}

// listWindows are the windows of the section's long lists, keyed by row.
// The lists share the rows the rest of the section leaves; the one the
// cursor is in gets the most, and every window is between minWindow and
// maxWindow lines high.
func (a app) listWindows(rows []settingRow, lines []settingLine) map[int]listWindow {
	opts := map[int][]int{}
	var order []int
	for i, l := range lines {
		if l.option < 0 || !rows[l.row].long() {
			continue
		}
		if _, ok := opts[l.row]; !ok {
			order = append(order, l.row)
		}
		opts[l.row] = append(opts[l.row], i)
	}
	if len(order) == 0 {
		return nil
	}
	fixed := len(lines) + 1
	for _, r := range order {
		fixed -= len(opts[r])
	}
	room := a.settingsListRows() - fixed
	focused := -1
	if c := a.settings.cursor; c >= 0 && c < len(lines) && lines[c].option >= 0 {
		if _, ok := opts[lines[c].row]; ok {
			focused = lines[c].row
		}
	}
	out := make(map[int]listWindow, len(order))
	for _, r := range order {
		h := room / len(order)
		if focused >= 0 && len(order) > 1 {
			h = minWindow
			if r == focused {
				h = room - minWindow*(len(order)-1)
			}
		}
		h = min(max(h, minWindow), maxWindow)
		m := len(opts[r])
		cur, start := -1, 0
		if r == focused {
			cur = slices.Index(opts[r], a.settings.cursor)
			start = cur - (h-2)/2
			if t := a.settings.window; t.set && t.name == rows[r].anchorName() {
				start = t.start
			}
		} else if at := currentPos(rows[r], lines, opts[r]); at >= 0 {
			start = at - (h-2)/2
		}
		w := fitWindow(m, h, start, cur)
		w.opts = opts[r]
		out[r] = w
	}
	return out
}

func currentPos(row settingRow, lines []settingLine, opts []int) int {
	for p, i := range opts {
		if lines[i].option == row.current {
			return p
		}
	}
	return -1
}

// displayKind is what one drawn line of a section is.
type displayKind int

const (
	displayRow displayKind = iota
	displayMore
	displayDesc
)

type displayLine struct {
	kind displayKind
	// line indexes the section's lines for displayLine; text is a marker's
	// or a description's.
	line int
	text string
	// indent is a description's extra indent under an indented row.
	indent bool
}

// settingsDisplay is every line the section draws, top to bottom, and where
// the cursor's line is among them.
func (a app) settingsDisplay(rows []settingRow, lines []settingLine) ([]displayLine, int) {
	wins := a.listWindows(rows, lines)
	var out []displayLine
	at := 0
	pos := map[int]int{}
	desc := a.focusedDescription(rows, lines)
	pending := false
	flush := func() {
		if pending {
			row := rows[lines[a.settings.cursor].row]
			out = append(out, displayLine{kind: displayDesc, line: -1, text: desc, indent: row.indent})
			pending = false
		}
	}
	for i, l := range lines {
		if l.wrap == 0 {
			flush()
		}
		w, windowed := wins[l.row]
		if !windowed || l.option < 0 {
			if i == a.settings.cursor {
				at = len(out)
				pending = desc != ""
			}
			out = append(out, displayLine{line: i})
			continue
		}
		p := pos[l.row]
		pos[l.row]++
		if p == w.start && w.up {
			out = append(out, displayLine{kind: displayMore, line: -1, text: fmt.Sprintf("↑ %d more", w.start)})
		}
		if !w.visible(p) {
			continue
		}
		if i == a.settings.cursor {
			at = len(out)
			pending = desc != ""
		}
		out = append(out, displayLine{line: i})
		if p == w.start+w.count-1 && w.down {
			flush()
			out = append(out, displayLine{kind: displayMore, line: -1, text: fmt.Sprintf("↓ %d more", len(w.opts)-w.start-w.count)})
		}
	}
	flush()
	return out, at
}

// focusedDescription is the dim line drawn under the focused row, or "" on
// a screen too short for it.
func (a app) focusedDescription(rows []settingRow, lines []settingLine) string {
	c := a.settings.cursor
	if a.settingsCompact() || a.settings.edit.active || c < 0 || c >= len(lines) || !lines[c].focusable() {
		return ""
	}
	if a.settingsListRows() < 6 {
		return ""
	}
	return rows[lines[c].row].description(lines[c].option)
}

// descFits reports whether text fits on the line under a row.
func (a app) descFits(text string, indent bool) bool {
	room := a.settingsContentWidth() - rowIndent
	if indent {
		room -= 2
	}
	return lipgloss.Width(text) <= room
}

// ensureSettingsCursorVisible scrolls so the cursor's line, its description
// and the heading of its list or group are on screen.
func (a *app) ensureSettingsCursorVisible(rows []settingRow, lines []settingLine) {
	s := &a.settings
	disp, at := a.settingsDisplay(rows, lines)
	visible := a.settingsListRows()
	top, bottom := at, at
	for bottom+1 < len(disp) && (disp[bottom+1].kind == displayDesc || disp[bottom+1].kind == displayRow && lines[disp[bottom+1].line].wrap > 0) {
		bottom++
	}
	for top > 0 {
		d := disp[top-1]
		if d.kind == displayMore || d.kind == displayRow && (lines[d.line].header || lines[d.line].group || lines[d.line].filter) {
			top--
			continue
		}
		break
	}
	first, last := -1, -1
	for i, d := range disp {
		if d.kind == displayRow && lines[d.line].focusable() {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if at == first && bottom < visible {
		top = 0
	}
	if at == last && len(disp)-top <= visible {
		bottom = len(disp) - 1
	}
	if bottom-top+1 > visible {
		top, bottom = at, at
	}
	if bottom >= s.offset+visible {
		s.offset = bottom - visible + 1
	}
	if top < s.offset {
		s.offset = top
	}
	s.offset = max(min(s.offset, len(disp)-visible), 0)
}
