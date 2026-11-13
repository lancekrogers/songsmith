package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// labelWidth is the label column of the Song box rows.
const labelWidth = 8

func (c *composeModel) boxWidth() int { return max(min(c.width-2, 100), 30) }

// editorRows is the height the open editor and its tag menu share: the
// screen less the header and blank line, the box border, the message and
// the two key lines.
func (c *composeModel) editorRows() int {
	return max(c.height-6-len(c.messageLines()), 4)
}

// layoutEditor fits the open text editor, and its tag menu, to the screen.
func (c *composeModel) layoutEditor() {
	if c.editor == nil {
		return
	}
	c.editor.setSize(c.boxWidth()-8, max(c.editorRows()-c.editorMenuRows(), 3))
}

func (c *composeModel) editorMenuRows() int {
	if c.editor == nil || c.editor.menu == nil {
		return 0
	}
	return c.editor.menuRows(min(6, c.editorRows()/2))
}

// formBox draws a titled, rounded box around body lines, indented two
// columns. The border is the accent colour when the box has focus.
func formBox(title string, body []string, width int, focused bool) []string {
	border := lipgloss.NewStyle().Foreground(formMuted)
	if focused {
		border = lipgloss.NewStyle().Foreground(formAccent)
	}
	inner := width - 4
	titleText := " " + truncate(title, width-6) + " "
	fill := max(width-3-ansi.StringWidth(titleText), 0)
	lines := make([]string, 0, len(body)+2)
	lines = append(lines, "  "+border.Render("╭─")+titleText+border.Render(strings.Repeat("─", fill)+"╮"))
	for _, line := range body {
		content := truncate(line, inner)
		pad := max(inner-ansi.StringWidth(content), 0)
		lines = append(lines, "  "+border.Render("│")+" "+content+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	return append(lines, "  "+border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
}

func (a app) composeView() string {
	c := a.compose
	w, h := a.width, a.height
	head := []string{c.headerLine(), ""}
	var body, tail []string
	if c.editor != nil {
		tail = append(c.messageLines(), footer(w, "  ", c.editor.modeKeys()...), footer(w, "  ", editorKeys()...))
		body = fitLines(c.editorLines(), max(h-len(head)-len(tail), 3))
	} else {
		tail = append(c.messageLines(), a.formFooter()...)
		budget := max(h-len(head)-len(tail), 3)
		boxes, focus := a.formBoxes(budget)
		body = scrollBoxes(boxes, focus, budget)
	}
	all := append(append(head, body...), tail...)
	for i, l := range all {
		all[i] = truncate(l, w)
	}
	return strings.Join(all, "\n")
}

func (c composeModel) headerLine() string {
	switch {
	case c.copied:
		return headerStyle.Render("  ♪ Edit a copy") + dimStyle.Render("  of "+c.base.Title+"; the finished song and its audio stay as they are")
	case c.base.ID != "":
		return headerStyle.Render("  ♪ Edit song") + dimStyle.Render("  "+c.base.Title+" · "+string(c.base.Status))
	}
	return headerStyle.Render("  ♪ New song") + dimStyle.Render("  describe it and let the writer draft it, or write it yourself")
}

// messageLines is the latest message, wrapped to at most three lines.
func (c composeModel) messageLines() []string {
	if c.msg == "" {
		return []string{""}
	}
	style := map[chipTone]lipgloss.Style{toneDim: normalStyle, toneOK: statusStyle, toneWarn: warningStyle, toneBad: errorStyle}[c.tone]
	lines := strings.Split(lipgloss.NewStyle().Width(max(c.width-4, 20)).Render(c.msg), "\n")
	out := make([]string, 0, 3)
	for _, l := range lines[:min(len(lines), 3)] {
		out = append(out, "  "+style.Render(strings.TrimRight(l, " ")))
	}
	return out
}

// formFooter lists the keys that work on the focused row, then the form keys.
func (a app) formFooter() []string {
	c := a.compose
	w := a.width
	if c.drafting {
		return []string{footer(w, "  ", "esc cancel the draft")}
	}
	var keys []string
	_, down := a.writerUnavailable()
	switch {
	case c.focus == rowIdea && down:
		keys = append(keys, "enter next field")
	case c.focus == rowIdea:
		keys = append(keys, "enter draft lyrics and style")
	case c.focus == rowLength:
		keys = append(keys, "←/→ change length")
	case c.focus == rowVocals:
		keys = append(keys, "←/→ sung or instrumental")
	case c.focus.isText():
		keys = append(keys, "enter edit")
		if externalEditorSet() {
			keys = append(keys, "ctrl+e $EDITOR")
		}
	}
	keys = append(keys, "↑/↓/tab move")
	esc := "esc back"
	if c.armed == "esc" {
		esc = "esc discard changes"
	}
	return []string{footer(w, "  ", keys...), footer(w, "  ", "ctrl+s save & queue", "ctrl+d save draft", esc)}
}

// formBoxes draws the form's boxes and says which one has focus. avail is
// the height the boxes get.
func (a app) formBoxes(avail int) (boxes [][]string, focus int) {
	c := a.compose
	boxW := c.boxWidth()
	inner := boxW - 4
	lines := 0
	add := func(focused bool, box []string) {
		if focused {
			focus = len(boxes)
		}
		boxes = append(boxes, box)
		lines += len(box)
	}

	idea := []string{c.idea.View()}
	why, down := a.writerUnavailable()
	switch {
	case c.drafting:
		idea = append(idea, c.spinner.View()+" "+warningStyle.Render("Drafting lyrics and style with "+a.cfg().Ollama.Model+"…"))
	case down:
		idea = append(idea,
			warningStyle.Render("Writer unavailable: "+why+"."),
			dimStyle.Render("The fields below can be written by hand."))
	default:
		idea = append(idea, dimStyle.Render("enter drafts lyrics and a style with "+a.cfg().Ollama.Model))
	}
	add(c.focus == rowIdea, formBox(headerStyle.Render("Idea"), idea, boxW, c.focus == rowIdea))

	row := func(r formRow, value string) string {
		mark, label := "  ", dimStyle
		if c.focus == r {
			mark, label = selectedStyle.Render("▸ "), selectedStyle
		}
		return mark + label.Render(fmt.Sprintf("%-*s", labelWidth, r.name())) + " " + value
	}
	vocals, vocalsHint := "sung", "the draft writes lines to sing"
	if c.instrumental {
		vocals, vocalsHint = "instrumental", "the draft writes section tags, no sung lines"
	}
	song := []string{
		row(rowTitle, c.title.View()),
		row(rowLength, "‹ "+fmtDuration(c.seconds)+" ›"+dimStyle.Render("  the longest it may run")),
		row(rowVocals, "‹ "+vocals+" ›"+dimStyle.Render("  "+vocalsHint)),
		row(rowSeed, c.seed.View()),
	}
	songFocused := c.focus >= rowTitle && c.focus <= rowSeed
	add(songFocused, formBox(headerStyle.Render("Song"), song, boxW, songFocused))

	// Lyrics get the rows the other boxes leave; 15 is the Style boxes.
	previewRows := min(max(avail-lines-3-15, 2), 12)
	add(c.focus == rowLyrics, formBox(headerStyle.Render("Lyrics")+dimStyle.Render(" · section tags like [Verse] on their own lines"),
		lyricsPreview(c.texts[0], previewRows, c.instrumental), boxW, c.focus == rowLyrics))

	for _, s := range []struct {
		r    formRow
		hint string
	}{
		{rowGlobal, "genre, tempo, key, mood, production"},
		{rowVocal, "voice, delivery, harmonies"},
		{rowArrangement, "what each section adds or drops"},
	} {
		title := headerStyle.Render(s.r.name()) + dimStyle.Render(" · "+s.hint)
		add(c.focus == s.r, formBox(title, stylePreview(c.texts[s.r.textIndex()], inner), boxW, c.focus == s.r))
	}
	return boxes, focus
}

func lyricsPreview(text string, rows int, instrumental bool) []string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		if instrumental {
			return []string{dimStyle.Render("(empty) · enter to add section tags such as [Intro] [Instrumental] [Outro]")}
		}
		return []string{dimStyle.Render("(empty) · enter to write them")}
	}
	all := strings.Split(text, "\n")
	var out []string
	for _, l := range all[:min(rows, len(all))] {
		if st, ok := tagStyle(l); ok {
			l = st.Render(l)
		}
		out = append(out, l)
	}
	count := fmt.Sprintf("%d lines", len(all))
	if len(all) == 1 {
		count = "1 line"
	}
	return append(out, dimStyle.Render(count+" · enter to edit"))
}

// stylePreview is the first two wrapped lines of a style part.
func stylePreview(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{dimStyle.Render("(empty)"), "", dimStyle.Render("enter to write it")}
	}
	wrapped := strings.Split(lipgloss.NewStyle().Width(width).Render(strings.Join(strings.Fields(text), " ")), "\n")
	out := []string{strings.TrimRight(wrapped[0], " "), ""}
	if len(wrapped) > 1 {
		out[1] = strings.TrimRight(wrapped[1], " ")
	}
	if len(wrapped) > 2 {
		out[1] = truncate(out[1]+" …", width)
	}
	return append(out, dimStyle.Render("enter to edit"))
}

// scrollBoxes shows the whole boxes that fit in budget lines, starting as
// high as keeps the focused box in view. A line at either edge says when
// boxes are hidden that way; the result is padded so the footer stays at
// the bottom.
func scrollBoxes(boxes [][]string, focus, budget int) []string {
	for start := 0; start <= focus; start++ {
		used, end := 0, start
		if start > 0 {
			used++
		}
		for end < len(boxes) {
			need := len(boxes[end])
			if end+1 < len(boxes) {
				need++ // room for the marker below
			}
			if used+need > budget {
				break
			}
			used += len(boxes[end])
			end++
		}
		if end <= focus {
			continue
		}
		var out []string
		if start > 0 {
			out = append(out, dimStyle.Render("  ⋯ more above"))
		}
		for _, b := range boxes[start:end] {
			out = append(out, b...)
		}
		if end < len(boxes) {
			out = append(out, dimStyle.Render("  ⋯ more below"))
		}
		return fitLines(out, budget)
	}
	// The focused box alone is taller than the screen.
	return fitLines(boxes[focus], budget)
}

// fitLines cuts or pads lines to exactly n.
func fitLines(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

func (c composeModel) editorLines() []string {
	title := headerStyle.Render(c.editorRow.name()) + "  " + c.editor.modeChip()
	if c.editorRow == rowLyrics {
		title += dimStyle.Render("  [Verse] [Chorus] tags on their own lines")
	}
	return formBox(title, c.editor.bodyLines(c.editorMenuRows()), c.boxWidth(), true)
}
