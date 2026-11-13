package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/queue"
	"github.com/lancekrogers/songsmith/internal/setup"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// formRow is a focusable row of the song form, in screen order.
type formRow int

const (
	rowIdea formRow = iota
	rowWriter
	rowDraft
	rowTitle
	rowLength
	rowVocals
	rowSeed
	rowLyrics
	rowGlobal
	rowVocal
	rowArrangement
	rowCount
)

// isText reports whether the row is a long field edited in the text editor.
func (r formRow) isText() bool { return r >= rowLyrics }

// textIndex is the row's slot in composeModel.texts.
func (r formRow) textIndex() int { return int(r - rowLyrics) }

func (r formRow) name() string {
	return [...]string{"Idea", "Writer", "Draft", "Title", "Length", "Vocals", "Seed", "Lyrics", "Global metadata", "Vocal details", "Arrangement"}[r]
}

const (
	lengthStep = 15
	minLength  = writer.MinSeconds
	maxLength  = writer.MaxSeconds
)

// songFields are the form's values, compared to tell whether it changed.
type songFields struct {
	idea, title, seed string
	seconds           float64
	autoLength        bool
	instrumental      bool
	texts             [4]string
}

// composeModel is the song form, used for new songs and edits.
type composeModel struct {
	// returnTo is the screen that opened the form; leaving goes back there.
	returnTo screen
	// base is the song being edited; base.ID is empty for a new song.
	base library.Song
	// copied is set when base is a copy of a finished or rendering song.
	copied  bool
	initial songFields

	// idea wraps over as many lines as it needs, up to maxIdeaLines.
	idea        textarea.Model
	title, seed textinput.Model
	seconds     float64
	// autoLength leaves the length to the writer: drafts get no target and
	// seconds takes the drafted length, or stays the default without one.
	// draftedLength is set once seconds holds a drafted length.
	autoLength    bool
	draftedLength bool
	instrumental  bool
	// texts are Lyrics, Global metadata, Vocal details and Arrangement.
	texts [4]string
	focus formRow
	// skip are rows focus passes over: the writer row without a model to
	// choose, the draft row without a writer.
	skip [rowCount]bool

	// editor is the open text editor, or nil on the form.
	editor    *textEditor
	editorRow formRow

	spinner  spinner.Model
	drafting bool
	// keepTitle keeps the title through a draft started from the library.
	keepTitle bool
	draftSeq  int
	cancel    context.CancelFunc

	// armed is a key pressed once that acts on its second press: esc
	// discards changes, "draft" replaces filled fields with a draft.
	armed string
	msg   string
	tone  chipTone

	width, height int
}

type (
	draftDoneMsg struct {
		seq   int
		idea  string
		model string
		draft writer.Draft
		err   error
	}
	editorDoneMsg struct {
		field formRow
		path  string
		err   error
	}
)

func newCompose() composeModel {
	input := func(placeholder string, limit int) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.CharLimit = limit
		return ti
	}
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	idea := textarea.New()
	idea.Prompt = ""
	idea.ShowLineNumbers = false
	idea.CharLimit = 0
	idea.Placeholder = "a road-trip anthem about leaving a small town"
	idea.KeyMap.InsertNewline.SetEnabled(false)
	idea.FocusedStyle.CursorLine = lipgloss.NewStyle()
	idea.FocusedStyle.Placeholder = dimStyle
	idea.BlurredStyle.Placeholder = dimStyle
	idea.SetHeight(1)
	return composeModel{
		idea:    idea,
		title:   input("Untitled", 0),
		seed:    input("random each render", 20),
		spinner: sp,
	}
}

// maxIdeaLines is the most lines the idea takes before it scrolls.
const maxIdeaLines = 6

// fitIdea makes the idea box as tall as its wrapped text, up to
// maxIdeaLines. The idea is one line, so its wrapped height is the line's.
// Re-setting the value scrolls the box back to its top.
func (c *composeModel) fitIdea() {
	if v := c.idea.Value(); strings.ContainsAny(v, "\r\n") {
		c.idea.SetValue(strings.Join(strings.Fields(v), " "))
	}
	info := c.idea.LineInfo()
	n := min(max(info.Height, 1), maxIdeaLines)
	col := info.StartColumn + info.ColumnOffset
	c.idea.SetHeight(n)
	c.idea.SetValue(c.idea.Value())
	c.idea.SetCursor(col)
	if info.Height > maxIdeaLines && c.idea.Focused() {
		c.idea, _ = c.idea.Update(nil)
	}
}

func (c *composeModel) resize(w, h int) {
	c.width, c.height = w, h
	inner := c.boxWidth() - 4
	c.idea.SetWidth(max(inner-1, 10))
	c.fitIdea()
	c.title.Width = max(inner-labelWidth-4, 10)
	c.seed.Width = 22
	c.layoutEditor()
}

// startEdit fills the form from song, or empties it for a new song when
// song has no ID. newSeconds is the length a song without one starts with.
func (c *composeModel) startEdit(song library.Song, newSeconds float64) tea.Cmd {
	c.copied = false
	if song.Status == library.StatusDone || song.Status == library.StatusRendering {
		// Keep finished audio: edits become a new song.
		song = asCopy(song)
		c.copied = true
	}
	c.base = song
	c.idea.SetValue(song.Idea)
	c.fitIdea()
	c.title.SetValue(song.Title)
	c.seed.SetValue("")
	if song.SeedPinned {
		c.seed.SetValue(strconv.FormatInt(song.Seed, 10))
	}
	c.seconds = cmp.Or(song.Seconds, newSeconds)
	c.autoLength = song.ID == "" && !c.copied && song.Seconds == 0
	c.draftedLength = false
	c.instrumental = song.Instrumental
	c.texts = [4]string{song.Lyrics, song.Caption.GlobalMetadata, song.Caption.VocalDetails, song.Caption.Arrangement}
	c.initial = c.fields()
	c.editor, c.drafting, c.cancel, c.armed, c.msg = nil, false, nil, "", ""
	c.focus = rowIdea
	if song.ID != "" || c.copied {
		c.focus = rowLyrics
	}
	return c.applyFocus()
}

// asCopy turns song into an unsaved new song with the same words and style,
// and the same seed only when it was pinned.
func asCopy(song library.Song) library.Song {
	song.ID, song.Status, song.Audio, song.Duration, song.Error, song.JobID = "", "", "", 0, "", ""
	song.Started, song.Finished, song.Timings = time.Time{}, time.Time{}, nil
	if !song.SeedPinned {
		song.Seed = 0
	}
	return song
}

func (c *composeModel) fields() songFields {
	return songFields{
		idea: c.idea.Value(), title: c.title.Value(), seed: c.seed.Value(),
		seconds: c.seconds, autoLength: c.autoLength, instrumental: c.instrumental, texts: c.texts,
	}
}

func (c *composeModel) dirty() bool { return c.fields() != c.initial }

// empty reports whether there is nothing worth saving.
func (c *composeModel) empty() bool {
	f := c.fields()
	return strings.TrimSpace(f.idea+f.title+strings.Join(f.texts[:], "")) == ""
}

func (c *composeModel) applyFocus() tea.Cmd {
	c.idea.Blur()
	c.title.Blur()
	c.seed.Blur()
	if c.editor != nil {
		return nil
	}
	switch c.focus {
	case rowIdea:
		return c.idea.Focus()
	case rowTitle:
		return c.title.Focus()
	case rowSeed:
		return c.seed.Focus()
	}
	return nil
}

func (c *composeModel) move(delta int) tea.Cmd {
	for range rowCount {
		c.focus = formRow((int(c.focus) + delta + int(rowCount)) % int(rowCount))
		if !c.skip[c.focus] {
			break
		}
	}
	return c.applyFocus()
}

func (c *composeModel) focusOn(r formRow) tea.Cmd {
	c.focus = r
	return c.applyFocus()
}

// setMsg shows a message, and refits an open editor to the lines it takes.
func (c *composeModel) setMsg(tone chipTone, format string, args ...any) {
	c.tone, c.msg = tone, fmt.Sprintf(format, args...)
	c.layoutEditor()
}

// stepLength moves seconds to the next 15 s mark in dir, within the
// lengths a render accepts.
func stepLength(seconds float64, dir int) float64 {
	next := (math.Floor(seconds/lengthStep) + 1) * lengthStep
	if dir < 0 {
		next = (math.Ceil(seconds/lengthStep) - 1) * lengthStep
	}
	return min(max(next, minLength), maxLength)
}

// parseSeed reads the Seed field: blank is unpinned, a new random seed
// each render; a number pins it.
func parseSeed(s string) (seed int64, pinned bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false, errors.New("seed must be a whole number of 0 or more, or blank for a new random seed each render")
	}
	return n, true, nil
}

// song builds the edited song from the form. A blank seed keeps the stored
// one unpinned, so a render waiting to resume still matches it.
func (c *composeModel) song() (library.Song, error) {
	s := c.base
	seed, pinned, err := parseSeed(c.seed.Value())
	if err != nil {
		return s, err
	}
	s.SeedPinned = pinned
	if pinned {
		s.Seed = seed
	}
	s.Idea = strings.TrimSpace(c.idea.Value())
	s.Title = cmp.Or(strings.TrimSpace(c.title.Value()), "Untitled")
	s.Seconds = c.seconds
	s.Instrumental = c.instrumental
	s.Lyrics = strings.TrimSpace(c.texts[0])
	s.Caption = writer.Caption{
		GlobalMetadata: strings.TrimSpace(c.texts[1]),
		VocalDetails:   strings.TrimSpace(c.texts[2]),
		Arrangement:    strings.TrimSpace(c.texts[3]),
	}
	return s, nil
}

var fieldRows = map[string]formRow{
	writer.FieldLyrics:         rowLyrics,
	writer.FieldGlobalMetadata: rowGlobal,
	writer.FieldVocalDetails:   rowVocal,
	writer.FieldArrangement:    rowArrangement,
	writer.FieldSeconds:        rowLength,
}

// writerUnavailable says why drafting cannot work, from the latest checks.
// Before any check has finished the writer is assumed to work.
func (a app) writerUnavailable() (string, bool) {
	w, ok := health.Find(a.health.checks, health.IDWriter)
	if !a.health.done || !ok || w.Result == health.OK {
		return "", false
	}
	cfg := a.cfg().Ollama
	if w.Fix == health.FixWriterModel {
		return "the model " + cfg.Model + " is not installed in Ollama", true
	}
	return "Ollama is not reachable at " + cfg.URL, true
}

// leaveCompose returns to the screen that opened the form.
func (a app) leaveCompose() (app, tea.Cmd) {
	c := &a.compose
	if c.cancel != nil {
		c.cancel()
	}
	c.cancel, c.drafting, c.editor = nil, false, nil
	return a.show(a.back())
}

func (a app) updateCompose(msg tea.Msg) (app, tea.Cmd) {
	c := &a.compose
	switch msg := msg.(type) {
	case draftDoneMsg:
		return a.draftDone(msg)
	case editorDoneMsg:
		return a.externalDone(msg)
	case spinner.TickMsg:
		if !c.drafting {
			return a, nil
		}
		var cmd tea.Cmd
		c.spinner, cmd = c.spinner.Update(msg)
		return a, cmd
	case tea.KeyMsg:
		if c.editor != nil {
			return a.editorKey(msg)
		}
		return a.formKey(msg)
	}
	return a, c.updateInput(msg)
}

// updateInput sends msg to the focused one-line field.
func (c *composeModel) updateInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch c.focus {
	case rowIdea:
		c.idea, cmd = c.idea.Update(msg)
		c.fitIdea()
	case rowTitle:
		c.title, cmd = c.title.Update(msg)
	case rowSeed:
		c.seed, cmd = c.seed.Update(msg)
	}
	return cmd
}

const (
	unsavedChanges = "Unsaved changes: ctrl+d saves the draft, esc again discards"
	unsavedQuit    = "Unsaved changes: ctrl+d saves the draft, ctrl+c again quits"
)

func (a app) formKey(key tea.KeyMsg) (app, tea.Cmd) {
	c := &a.compose
	if key.Paste {
		key.Runes = []rune(normalizeNewlines(string(key.Runes)))
	}
	if key.Paste && c.focus == rowIdea {
		key.Runes = []rune(strings.ReplaceAll(string(key.Runes), "\n", " "))
	}
	a.refreshFormSkips()
	k := key.String()
	if k == "ctrl+c" {
		if c.dirty() && c.armed != "ctrl+c" {
			c.armed = "ctrl+c"
			c.setMsg(toneWarn, unsavedQuit)
			return a, nil
		}
		return a.quit()
	}
	if c.drafting {
		c.armed = ""
		if k == "esc" {
			c.cancel()
			c.cancel, c.drafting = nil, false
			c.draftSeq++
			c.setMsg(toneWarn, "Draft cancelled.")
		}
		return a, nil
	}
	armed := c.armed
	c.armed, c.msg = "", ""
	switch k {
	case "ctrl+s", "ctrl+d":
		return a.saveCompose(k == "ctrl+s")
	case "esc":
		if c.dirty() && armed != "esc" {
			c.armed = "esc"
			c.setMsg(toneWarn, unsavedChanges)
			return a, nil
		}
		return a.leaveCompose()
	case "up", "shift+tab":
		return a, c.move(-1)
	case "down", "tab":
		return a, c.move(1)
	case "ctrl+g":
		return a.startDraft(armed == "draft")
	case "enter":
		switch {
		case c.focus == rowIdea || c.focus == rowDraft:
			return a.startDraft(armed == "draft")
		case c.focus.isText():
			c.openEditor(c.focus)
			return a, nil
		}
		return a, c.move(1)
	case "ctrl+e":
		if c.focus.isText() {
			return a.externalEdit(c.focus, c.texts[c.focus.textIndex()])
		}
		return a, nil
	case "left", "right":
		dir := 1
		if k == "left" {
			dir = -1
		}
		switch c.focus {
		case rowWriter:
			return a.cycleWriter(dir)
		case rowLength:
			c.stepLength(a.defaultSeconds(), dir)
			return a, nil
		case rowVocals:
			c.instrumental = !c.instrumental
			return a, nil
		}
	}
	return a, c.updateInput(key)
}

// stepLength changes the length by one step. The first step away from auto
// lands on the default length.
func (c *composeModel) stepLength(def float64, dir int) {
	if c.autoLength && !c.draftedLength {
		c.seconds = min(max(def, minLength), maxLength)
	} else {
		c.seconds = stepLength(c.seconds, dir)
	}
	c.autoLength, c.draftedLength = false, false
}

// draftReplaces names the filled fields a draft would replace.
func (c *composeModel) draftReplaces() []string {
	var names []string
	if strings.TrimSpace(c.title.Value()) != "" {
		names = append(names, "title")
	}
	for i, name := range []string{"lyrics", "global metadata", "vocal details", "arrangement"} {
		if strings.TrimSpace(c.texts[i]) != "" {
			names = append(names, name)
		}
	}
	return names
}

// startDraft asks the writer for lyrics and a style from the idea.
// confirmed is set by a second enter when drafting would replace fields.
func (a app) startDraft(confirmed bool) (app, tea.Cmd) {
	a.compose.keepTitle = false
	return a.draft(confirmed)
}

// draftKeepingTitle drafts the lyrics and style of a song that has none,
// keeping the title it was given.
func (a app) draftKeepingTitle() (app, tea.Cmd) {
	a.compose.keepTitle = strings.TrimSpace(a.compose.title.Value()) != ""
	return a.draft(len(a.compose.draftReplaces()) == 0 || a.compose.keepTitle && len(a.compose.draftReplaces()) == 1)
}

func (a app) draft(confirmed bool) (app, tea.Cmd) {
	c := &a.compose
	if why, down := a.writerUnavailable(); down {
		c.setMsg(toneWarn, "No draft: %s. Write the song in the fields below by hand.", why)
		if c.focus == rowIdea {
			return a, c.move(1)
		}
		return a, nil
	}
	idea := strings.TrimSpace(c.idea.Value())
	if idea == "" {
		c.setMsg(toneWarn, "Describe the song first: genre, mood, subject, anything.")
		return a, c.focusOn(rowIdea)
	}
	if names := c.draftReplaces(); len(names) > 0 && !confirmed {
		c.armed = "draft"
		c.setMsg(toneWarn, "Drafting replaces the %s below: ctrl+g or enter again drafts, any other key keeps them", joinAnd(names))
		return a, nil
	}
	ctx, cancel := context.WithCancel(a.ctx)
	c.cancel, c.drafting = cancel, true
	c.draftSeq++
	seq, drafter, model := c.draftSeq, a.d.Drafter, a.cfg().Ollama.Model
	opts := writer.Options{Instrumental: c.instrumental}
	if !c.autoLength {
		opts.Seconds = c.seconds
	}
	return a, tea.Batch(c.spinner.Tick, func() tea.Msg {
		d, err := drafter.Write(ctx, idea, opts)
		if ctx.Err() != nil {
			return nil
		}
		return draftDoneMsg{seq: seq, idea: idea, model: model, draft: d, err: err}
	})
}

// draftDone fills the form from a draft. A failed draft still fills in
// whatever came back, and never clears a field with nothing.
func (a app) draftDone(msg draftDoneMsg) (app, tea.Cmd) {
	c := &a.compose
	if !c.drafting || msg.seq != c.draftSeq {
		return a, nil
	}
	c.drafting, c.cancel = false, nil
	d := msg.draft
	set := func(dst *string, v string) {
		if strings.TrimSpace(v) != "" {
			*dst = v
		}
	}
	if d.Title != "" && !c.keepTitle {
		c.title.SetValue(d.Title)
	}
	set(&c.texts[0], d.Lyrics)
	set(&c.texts[1], d.Caption.GlobalMetadata)
	set(&c.texts[2], d.Caption.VocalDetails)
	set(&c.texts[3], d.Caption.Arrangement)
	if d.Seconds > 0 && c.autoLength {
		c.seconds, c.draftedLength = d.Seconds, true
	}
	cmd := c.focusOn(rowLyrics)
	if msg.err != nil {
		what := "Draft failed"
		if msg.model != "" && !strings.Contains(msg.err.Error(), msg.model) {
			what = "Draft with " + msg.model + " failed"
		}
		c.setMsg(toneBad, "%s: %v. ctrl+g tries again; or write the lyrics and style yourself.", what, msg.err)
		return a, cmd
	}
	c.setMsg(toneOK, "Drafted. Edit anything, then ctrl+s saves and queues or ctrl+d saves a draft.")
	return a, cmd
}

func (c *composeModel) openEditor(r formRow) {
	c.editor = newTextEditor(r.name(), c.texts[r.textIndex()], r == rowLyrics)
	c.editorRow = r
	c.applyFocus()
	c.layoutEditor()
}

func (c *composeModel) closeEditor(keep bool) {
	if keep {
		c.texts[c.editorRow.textIndex()] = c.editor.text()
	}
	c.editor = nil
	c.focusOn(c.editorRow)
}

func (a app) editorKey(key tea.KeyMsg) (app, tea.Cmd) {
	c := &a.compose
	c.msg = ""
	ev := c.editor.update(key)
	c.layoutEditor()
	name := c.editorRow.name()
	switch ev {
	case editorKeep:
		c.closeEditor(true)
	case editorDiscard:
		changed := c.editor.text() != c.editor.original
		c.closeEditor(false)
		if changed {
			c.setMsg(toneWarn, "Discarded your changes to %s.", name)
		}
	case editorSaveDraft, editorSaveQueue:
		c.closeEditor(true)
		return a.saveCompose(ev == editorSaveQueue)
	case editorExternal:
		return a.externalEdit(c.editorRow, c.editor.text())
	case editorIdleEsc:
		c.setMsg(toneWarn, idleEscHint)
	default:
		if c.editor.note != "" {
			c.setMsg(toneWarn, "%s", c.editor.note)
		}
	}
	return a, nil
}

// externalEdit hands one field to $VISUAL or $EDITOR on a temp file.
func (a app) externalEdit(field formRow, text string) (app, tea.Cmd) {
	c := &a.compose
	if !externalEditorSet() {
		c.setMsg(toneWarn, "ctrl+e opens $VISUAL or $EDITOR, and neither is set. Set one in your shell, or keep editing here.")
		return a, nil
	}
	f, err := os.CreateTemp("", "songsmith-*.txt")
	if err != nil {
		c.setMsg(toneBad, "%v", err)
		return a, nil
	}
	_, werr := f.WriteString(text + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		c.setMsg(toneBad, "could not write a temp file for $EDITOR")
		return a, nil
	}
	path := f.Name()
	return a, tea.ExecProcess(editorCommand(path), func(err error) tea.Msg {
		return editorDoneMsg{field: field, path: path, err: err}
	})
}

func (a app) externalDone(msg editorDoneMsg) (app, tea.Cmd) {
	c := &a.compose
	defer os.Remove(msg.path)
	if msg.err != nil {
		c.setMsg(toneBad, "Editor: %v", msg.err)
		return a, nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		c.setMsg(toneBad, "%v", err)
		return a, nil
	}
	text := strings.TrimRight(string(data), "\n")
	if c.editor != nil && c.editorRow == msg.field {
		c.editor.setText(text)
	} else {
		c.texts[msg.field.textIndex()] = text
	}
	c.setMsg(toneOK, "Read %s back from your editor.", msg.field.name())
	return a, nil
}

// saveCompose saves the form, queues it when render is set, and goes back to
// the screen that opened the form with the song selected in the library.
// Queueing checks the fields first; a draft saves whatever is there.
func (a app) saveCompose(render bool) (app, tea.Cmd) {
	c := &a.compose
	song, err := c.song()
	if err != nil {
		c.setMsg(toneBad, "%v", err)
		return a, c.focusOn(rowSeed)
	}
	if c.empty() {
		c.setMsg(toneWarn, "Nothing to save yet: write an idea, a title, lyrics or a style first.")
		return a, nil
	}
	if render && strings.TrimSpace(c.idea.Value()) != "" && strings.TrimSpace(strings.Join(c.texts[:], "")) == "" {
		if _, down := a.writerUnavailable(); !down {
			c.setMsg(toneWarn, "Nothing to render yet: ctrl+g drafts lyrics and style from your idea")
			return a, c.focusOn(rowDraft)
		}
		c.setMsg(toneWarn, "Nothing to render yet: write the lyrics and style below, or ctrl+d saves the idea as a draft")
		return a, c.focusOn(rowLyrics)
	}
	if render {
		var fe *writer.FieldError
		if err := song.CheckRender(); errors.As(err, &fe) {
			c.setMsg(toneBad, "Can't queue yet: %s.", fieldProblem(fe))
			return a, c.focusOn(fieldRows[fe.Field])
		}
	}
	blocked, fix := "", ""
	if render {
		if b, ok := health.Blocker(a.health.checks); ok {
			render = false
			blocked, fix = missingForRender(a.health.checks, b)
		}
	}
	song.Status = library.StatusDraft
	if a.d.Store.HasAudio(song) {
		song.Status = library.StatusDone
	}
	copied := false
	if song.ID != "" {
		err = a.d.Queue.SaveEdit(song)
		if errors.Is(err, queue.ErrBusy) {
			// It started rendering while being edited: keep the edits, and a
			// typed seed, as a new song.
			song = asCopy(song)
			copied = true
		}
	}
	if song.ID == "" {
		song, err = a.d.Store.Create(song)
	}
	if err != nil {
		c.setMsg(toneBad, "%v", err)
		return a, nil
	}
	a.notice = "Saved " + song.Title + "."
	if copied {
		a.notice = "That song started rendering, so your edits were saved as a new song."
	}
	switch {
	case blocked != "":
		a.notice = "Saved " + song.Title + " as a draft instead of queueing it: rendering needs " + blocked + ". " + fix
	case render:
		if _, err := a.d.Queue.Enqueue(song); err != nil {
			a.err = "queue " + song.Title + ": " + err.Error()
		} else {
			a.notice = "Queued " + song.Title + ". Rendering takes several minutes per minute of song."
		}
	}
	a.library.selectID = song.ID
	a, cmd := a.leaveCompose()
	return a, tea.Batch(cmd, a.loadSongs())
}

// fieldProblem says what is wrong with a field in the words of the form: an
// empty style part names its box.
func fieldProblem(fe *writer.FieldError) string {
	switch fe.Field {
	case writer.FieldGlobalMetadata, writer.FieldVocalDetails, writer.FieldArrangement:
		return "the " + fieldRows[fe.Field].name() + " box is empty"
	}
	return fe.Msg
}

// missingForRender names what stops rendering and where it gets fixed.
func missingForRender(checks []health.Check, blocker health.Check) (what, fix string) {
	var blocking []health.Check
	for _, ch := range checks {
		if ch.Blocking && ch.Result == health.Fail {
			blocking = append(blocking, ch)
		}
	}
	fix = "Finish setup on the home screen says how to fix it."
	in, out := health.Missing(blocking)
	if len(in) > 0 && len(out) == 0 {
		fix = "Finish setup on the home screen installs it."
	}
	if all := append(in, out...); len(all) > 0 {
		return joinAnd(all), fix
	}
	return blocker.Name, fix
}

// refreshFormSkips marks the rows focus cannot land on now.
func (a *app) refreshFormSkips() {
	c := &a.compose
	_, _, ok := a.formWriterChoice()
	_, down := a.writerUnavailable()
	c.skip[rowWriter] = !ok
	c.skip[rowDraft] = down
}

// formWriterChoice is the models the form's Writer row cycles through, the
// current one's index, and whether there is a choice to make.
func (a app) formWriterChoice() (names []string, cur int, ok bool) {
	if !a.modelsCurrent() || a.settings.models.err != nil {
		return nil, -1, false
	}
	chat, _ := a.chatModels()
	model := setup.ModelTag(a.cfg().Ollama.Model)
	cur = -1
	for i, m := range chat {
		names = append(names, m.Name)
		if setup.ModelTag(m.Name) == model {
			cur = i
		}
	}
	return names, cur, len(names) > 1 || len(names) == 1 && cur < 0
}

// formWriterState is what the Writer row says when there is no choice.
func (a app) formWriterState() string {
	m := a.settings.models
	switch {
	case !a.modelsCurrent():
		return "loading installed models…"
	case m.err != nil:
		return "Ollama is not reachable at " + a.cfg().Ollama.URL
	}
	names, _, _ := a.formWriterChoice()
	if len(names) == 0 {
		return "no installed model can draft; Settings → Writer"
	}
	return names[0] + " · the only installed model"
}

// cycleWriter makes the next or previous installed model the writer. It is
// saved as Settings saves it, and the song itself does not change.
func (a app) cycleWriter(dir int) (app, tea.Cmd) {
	c := &a.compose
	names, cur, ok := a.formWriterChoice()
	if !ok {
		return a, nil
	}
	next := 0
	if cur >= 0 {
		next = (cur + dir + len(names)) % len(names)
	} else if dir < 0 {
		next = len(names) - 1
	}
	if err := a.saveConfig("ollama.model", names[next]); err != nil {
		c.setMsg(toneBad, "Not saved: %v", err)
		return a, nil
	}
	c.setMsg(toneOK, "%s · Writer model: %s", capitalize(appliesNextDraft), names[next])
	return a, a.recheckIfChanged()
}
