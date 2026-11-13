package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

type reviseStage int

const (
	reviseOff reviseStage = iota
	reviseInput
	reviseRunning
	reviseReview
)

// reviseModel is a revision of the form's song by the writer: the
// instruction being typed, the request running, or the result under review.
// The form's fields change only when a reviewed result is applied.
type reviseModel struct {
	stage reviseStage
	input textinput.Model
	// instruction is the one last sent, which r sends again.
	instruction string
	sent        writer.Draft
	result      writer.Draft
	// seconds is the Length applying the result sets.
	seconds float64
	summary string
	diff    []diffLine
	scroll  int
}

func (r *reviseModel) reset() {
	r.stage, r.instruction, r.scroll = reviseOff, "", 0
	r.result, r.sent, r.diff, r.summary = writer.Draft{}, writer.Draft{}, nil, ""
	r.input.SetValue("")
	r.input.Blur()
}

type reviseDoneMsg struct {
	seq   int
	model string
	draft writer.Draft
	err   error
}

const extendedNote = "Editing a copy. Same seed and style as the original; the music is a new performance, not a continuation of the recording."

func (c *composeModel) hasLyrics() bool { return strings.TrimSpace(c.texts[0]) != "" }

// extendCopy readies a copy of orig to be extended: the original's seed,
// pinned, and a title that says what it is.
func (c *composeModel) extendCopy(orig library.Song) {
	if orig.Seed != 0 {
		c.base.Seed, c.base.SeedPinned = orig.Seed, true
		c.seed.SetValue(strconv.FormatInt(orig.Seed, 10))
		c.fitSeed()
	}
	if t := strings.TrimSpace(c.title.Value()); t != "" && !strings.HasSuffix(t, " (extended)") {
		c.title.SetValue(t + " (extended)")
	}
	c.note = extendedNote
	c.initial = c.fields()
}

// toggleSeedPin pins the seed the latest render used, or unpins a pinned one.
func (c *composeModel) toggleSeedPin() {
	defer c.fitSeed()
	if strings.TrimSpace(c.seed.Value()) != "" {
		c.seed.SetValue("")
		return
	}
	if c.lastSeed != 0 {
		c.seed.SetValue(strconv.FormatInt(c.lastSeed, 10))
		c.seed.CursorEnd()
	}
}

// openRevise shows the instruction input, or says why revising cannot start.
func (a app) openRevise() (app, tea.Cmd) {
	c := &a.compose
	if why, down := a.writerUnavailable(); down {
		c.setMsg(toneWarn, "No revision: %s. Edit the lyrics and style by hand.", why)
		return a, nil
	}
	if !c.hasLyrics() {
		c.setMsg(toneWarn, "Nothing to revise yet: draft or write the lyrics first.")
		return a, nil
	}
	c.rv.stage = reviseInput
	c.rv.input.SetValue(c.rv.instruction)
	c.rv.input.CursorEnd()
	c.focus = rowRevise
	return a, c.applyFocus()
}

func (a app) reviseInputKey(key tea.KeyMsg) (app, tea.Cmd) {
	c := &a.compose
	c.armed = ""
	switch key.String() {
	case "enter":
		instruction := strings.TrimSpace(c.rv.input.Value())
		if instruction == "" {
			c.setMsg(toneWarn, "Say what should change first, such as: make it longer with two more verses that continue the story.")
			return a, nil
		}
		return a.startRevise(instruction)
	case "esc":
		c.rv.stage, c.msg = reviseOff, ""
		return a, c.applyFocus()
	}
	c.msg = ""
	return a, c.updateInput(key)
}

// startRevise sends the form's song and instruction to the writer.
func (a app) startRevise(instruction string) (app, tea.Cmd) {
	c := &a.compose
	c.msg = ""
	if why, down := a.writerUnavailable(); down {
		c.rv.stage = reviseOff
		c.setMsg(toneWarn, "No revision: %s. Edit the lyrics and style by hand.", why)
		return a, c.applyFocus()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	c.cancel, c.drafting = cancel, true
	c.draftSeq++
	c.rv.stage, c.rv.instruction = reviseRunning, instruction
	c.rv.sent = c.currentDraft()
	c.applyFocus()
	seq, drafter, model, current := c.draftSeq, a.d.Drafter, a.cfg().Ollama.Model, c.rv.sent
	idea, opts := strings.TrimSpace(c.idea.Value()), writer.Options{Instrumental: c.instrumental}
	return a, tea.Batch(c.spinner.Tick, func() tea.Msg {
		d, err := drafter.Revise(ctx, current, idea, instruction, opts)
		if ctx.Err() != nil {
			return nil
		}
		return reviseDoneMsg{seq: seq, model: model, draft: d, err: err}
	})
}

// currentDraft is the form's song as the writer sees it.
func (c *composeModel) currentDraft() writer.Draft {
	return writer.Draft{
		Title:  strings.TrimSpace(c.title.Value()),
		Lyrics: strings.TrimSpace(c.texts[0]),
		Caption: writer.Caption{
			GlobalMetadata: strings.TrimSpace(c.texts[1]),
			VocalDetails:   strings.TrimSpace(c.texts[2]),
			Arrangement:    strings.TrimSpace(c.texts[3]),
		},
		Seconds:      c.seconds,
		Instrumental: c.instrumental,
	}
}

// reviseDone shows a revision for review. A failed one changes nothing and
// goes back to the instruction.
func (a app) reviseDone(msg reviseDoneMsg) (app, tea.Cmd) {
	c := &a.compose
	if !c.drafting || msg.seq != c.draftSeq || c.rv.stage != reviseRunning {
		return a, nil
	}
	c.drafting, c.cancel = false, nil
	if msg.err != nil {
		what := "Revision failed"
		if msg.model != "" && !strings.Contains(msg.err.Error(), msg.model) {
			what = "Revision with " + msg.model + " failed"
		}
		c.rv.stage = reviseInput
		c.rv.input.SetValue(c.rv.instruction)
		c.rv.input.CursorEnd()
		c.setMsg(toneBad, "%s: %v. The song is unchanged; enter tries again, esc leaves it as it is.", what, msg.err)
		return a, c.applyFocus()
	}
	c.rv.result = msg.draft
	c.rv.diff = lineDiff(splitLines(c.rv.sent.Lyrics), splitLines(msg.draft.Lyrics))
	var lengthNote string
	c.rv.seconds, lengthNote = c.revisedLength(msg.draft.Seconds)
	c.rv.summary = reviseSummary(c.rv.sent, msg.draft, lengthNote)
	c.rv.stage, c.rv.scroll = reviseReview, 0
	return a, c.applyFocus()
}

// revisedLength is the Length applying a revision sets, and how the summary
// says it. A longer suggestion replaces the form's Length unless the user set
// it here; an automatic Length always takes the writer's.
func (c *composeModel) revisedLength(suggested float64) (float64, string) {
	cur := c.seconds
	from := fmtDuration(cur)
	if c.autoLength && !c.draftedLength {
		from = "auto"
	}
	switch {
	case suggested <= 0:
		return cur, "length " + from
	case c.autoLength:
		return suggested, "length " + from + " → " + fmtDuration(suggested)
	case c.lengthSet && suggested != cur:
		return cur, "length kept at " + from + " as you set it (the writer suggested " + fmtDuration(suggested) + ")"
	case suggested > cur:
		return suggested, "length " + from + " → " + fmtDuration(suggested)
	case suggested < cur:
		return cur, "length " + from + " (the writer suggested " + fmtDuration(suggested) + "; Length is the longest it may run)"
	}
	return cur, "length " + from
}

// reviseSummary says in one line what a revision changes.
func reviseSummary(before, after writer.Draft, length string) string {
	parts := []string{fmt.Sprintf("Lyrics: %d → %d lines", lyricLines(before.Lyrics), lyricLines(after.Lyrics))}
	added, removed := sectionChanges(before.Lyrics, after.Lyrics)
	if len(added) > 0 {
		parts = append(parts, "sections added: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "sections removed: "+strings.Join(removed, ", "))
	}
	parts = append(parts, length)
	var style []string
	for _, p := range []struct{ name, a, b string }{
		{"global metadata", before.Caption.GlobalMetadata, after.Caption.GlobalMetadata},
		{"vocal details", before.Caption.VocalDetails, after.Caption.VocalDetails},
		{"arrangement", before.Caption.Arrangement, after.Caption.Arrangement},
	} {
		if strings.TrimSpace(p.a) != strings.TrimSpace(p.b) {
			style = append(style, p.name)
		}
	}
	if len(style) > 0 {
		parts = append(parts, "style: "+joinAnd(style)+" updated")
	} else {
		parts = append(parts, "style unchanged")
	}
	if t, ok := renamed(before.Title, after.Title); ok {
		parts = append(parts, "title: "+before.Title+" → "+t)
	}
	return strings.Join(parts, " · ")
}

// renamed is the revision's title when it renames the song. Dropping the
// " (extended)" a copy was given is not a rename.
func renamed(sent, got string) (string, bool) {
	got = strings.TrimSpace(got)
	if got == "" || got == sent || got == strings.TrimSuffix(sent, " (extended)") {
		return "", false
	}
	return got, true
}

// lyricLines counts lines as the Lyrics box does.
func lyricLines(text string) int {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

// sectionLabeler names section tags in song order, numbering repeats: the
// third [Verse] is "Verse 3".
type sectionLabeler map[string]int

// label is the line's section name, or "" when it is not a tag line.
func (seen sectionLabeler) label(line string) string {
	start, end, _, ok := writer.TagLine(line)
	if !ok {
		return ""
	}
	name := strings.Join(strings.Fields(line[start+1:end-1]), " ")
	if name == "" {
		return ""
	}
	seen[name]++
	switch n := seen[name]; {
	case n > 1 && strings.ContainsAny(name[len(name)-1:], "0123456789"):
		return fmt.Sprintf("%s (%d)", name, n)
	case n > 1:
		return fmt.Sprintf("%s %d", name, n)
	}
	return name
}

// sectionChanges lists the section tags the diff from before to after adds,
// named by their place in after, and those it removes, named by their place
// in before.
func sectionChanges(before, after string) (added, removed []string) {
	inBefore, inAfter := sectionLabeler{}, sectionLabeler{}
	for _, d := range lineDiff(splitLines(before), splitLines(after)) {
		var b, a string
		if d.op != diffAdded {
			b = inBefore.label(d.text)
		}
		if d.op != diffRemoved {
			a = inAfter.label(d.text)
		}
		switch {
		case d.op == diffAdded && a != "":
			added = append(added, a)
		case d.op == diffRemoved && b != "":
			removed = append(removed, b)
		}
	}
	return added, removed
}

func (a app) reviewKey(key tea.KeyMsg) (app, tea.Cmd) {
	c := &a.compose
	c.armed = ""
	switch key.String() {
	case "enter":
		c.applyRevision()
		return a, c.applyFocus()
	case "r":
		return a.startRevise(c.rv.instruction)
	case "e":
		c.rv.stage, c.msg = reviseInput, ""
		c.rv.input.SetValue(c.rv.instruction)
		c.rv.input.CursorEnd()
		return a, c.applyFocus()
	case "esc":
		c.rv.stage = reviseOff
		c.setMsg(toneDim, "Revision discarded; the song is as it was.")
		return a, c.applyFocus()
	case "up", "k":
		c.scrollReview(-1)
	case "down", "j":
		c.scrollReview(1)
	case "pgup":
		c.scrollReview(-c.reviewRows())
	case "pgdown":
		c.scrollReview(c.reviewRows())
	case "home":
		c.rv.scroll = 0
	case "end":
		c.scrollReview(len(c.reviewLines()))
	}
	return a, nil
}

func (c *composeModel) applyRevision() {
	d := c.rv.result
	if t, ok := renamed(c.rv.sent.Title, d.Title); ok {
		c.title.SetValue(t)
	}
	c.texts = [4]string{d.Lyrics, d.Caption.GlobalMetadata, d.Caption.VocalDetails, d.Caption.Arrangement}
	if c.rv.seconds != c.seconds || c.autoLength {
		c.seconds = c.rv.seconds
		c.draftedLength = c.autoLength
	}
	c.rv.stage = reviseOff
	c.focus = rowLyrics
	c.setMsg(toneOK, "Revision applied; nothing is saved yet. ctrl+s saves and queues, ctrl+d saves a draft, esc twice discards it.")
}

func (c *composeModel) scrollReview(delta int) {
	c.rv.scroll = min(max(c.rv.scroll+delta, 0), max(len(c.reviewLines())-c.reviewRows(), 0))
}

// noteLines is the visit's note, wrapped.
func (c composeModel) noteLines() []string {
	if c.note == "" {
		return nil
	}
	return wrapPlain(c.note, max(c.width-4, 20), "  ", dimStyle)
}

// wrapPlain wraps text to width and styles each line after indent.
func wrapPlain(text string, width int, indent string, style lipgloss.Style) []string {
	var out []string
	for _, l := range strings.Split(ansi.Wrap(text, max(width, 1), ""), "\n") {
		out = append(out, indent+style.Render(strings.TrimRight(l, " ")))
	}
	return out
}

// reviewChrome is the lines above and below the review box, and the rows
// the box gets. The footer line is left blank for the caller, since which
// keys it lists depends on the rows.
func (c composeModel) reviewChrome() (head, tail []string, rows int) {
	head = append([]string{c.headerLine()}, c.noteLines()...)
	head = append(head, "")
	tail = append(c.messageLines(), "")
	head, tail = fitChrome(c.height, head, tail)
	return head, tail, max(c.height-len(head)-len(tail), 0)
}

// reviewTop is the summary and the instruction, wrapped to the box.
func (c composeModel) reviewTop() []string {
	inner := c.boxWidth() - 4
	top := wrapPlain(c.rv.summary, inner, "", normalStyle)
	top = append(top, wrapPlain("You asked: "+c.rv.instruction, inner, "", dimStyle)...)
	return append(top, "")
}

// reviewRows is how many diff lines the box shows at once.
func (c composeModel) reviewRows() int {
	_, _, rows := c.reviewChrome()
	return max(rows-2-len(c.reviewTop()), 1)
}

// reviewLines is the revised lyrics with added lines marked + and removed
// ones -, each wrapped to the box.
func (c composeModel) reviewLines() []string {
	width := max(c.boxWidth()-6, 10)
	var out []string
	for _, d := range c.rv.diff {
		style, mark := normalStyle, "  "
		switch d.op {
		case diffAdded:
			style, mark = statusStyle, "+ "
		case diffRemoved:
			style, mark = errorStyle, "- "
		default:
			if st, ok := tagStyle(d.text); ok {
				style = st
			}
		}
		for i, l := range strings.Split(ansi.Wrap(d.text, width, ""), "\n") {
			if i > 0 {
				mark = "  "
			}
			out = append(out, style.Render(mark+l))
		}
	}
	if len(out) == 0 {
		out = append(out, dimStyle.Render("(no lyrics)"))
	}
	return out
}

func (c composeModel) reviewKeys() []string {
	keys := []string{"enter apply", "r try again", "e edit the instruction", "esc discard"}
	if len(c.reviewLines()) > c.reviewRows() {
		keys = append(keys, "↑/↓ scroll")
	}
	return keys
}

// reviewView is the revision under review: what changes, then the lyrics
// with their changes marked.
func (c composeModel) reviewView() []string {
	head, tail, rows := c.reviewChrome()
	lines, n := c.reviewLines(), c.reviewRows()
	start := min(c.rv.scroll, max(len(lines)-n, 0))
	end := min(start+n, len(lines))
	title := headerStyle.Render("Revision")
	if len(lines) > n {
		title += dimStyle.Render(fmt.Sprintf(" · lines %d–%d of %d", start+1, end, len(lines)))
	}
	body := append(c.reviewTop(), fitLines(lines[start:end], n)...)
	box := formBox(title, body, c.boxWidth(), true)
	if len(tail) > 0 {
		tail[len(tail)-1] = footer(c.width, "  ", c.reviewKeys()...)
	}
	return append(append(head, fitLines(box, rows)...), tail...)
}

// reviseRows are the Idea box lines for revising: the action row, the
// instruction input, or the running request.
func (a app) reviseRows() []string {
	c := a.compose
	model := a.cfg().Ollama.Model
	switch c.rv.stage {
	case reviseRunning:
		return []string{"  " + c.spinner.View() + " " + warningStyle.Render("Revising with "+model+"…")}
	case reviseInput, reviseReview:
		return []string{
			selectedStyle.Render("▸ ✎ What should change?"),
			"    " + c.rv.input.View(),
		}
	}
	if _, down := a.writerUnavailable(); down || !c.hasLyrics() {
		return nil
	}
	mark, style := "  ", normalStyle
	if c.focus == rowRevise {
		mark, style = selectedStyle.Render("▸ "), selectedStyle
	}
	return []string{mark + style.Render("✎ Revise with "+model+"…") + dimStyle.Render("  enter or ctrl+r")}
}
