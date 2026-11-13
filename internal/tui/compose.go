package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/queue"
	"github.com/lancekrogers/songsmith/internal/writer"
)

const (
	fieldTitle = iota
	fieldSeconds
	fieldLyrics
	fieldCaption
	fieldCount
)

type composeStep int

const (
	stepIdea composeStep = iota
	stepEdit
)

type composeModel struct {
	step composeStep
	// returnTo is the screen that opened the form; leaving goes back there.
	returnTo screen

	idea     textinput.Model
	spinner  spinner.Model
	drafting bool
	cancel   context.CancelFunc

	// base is the song being edited; base.ID is empty for a new song.
	base    library.Song
	title   textinput.Model
	seconds textinput.Model
	lyrics  textarea.Model
	caption textarea.Model
	focus   int
	err     string
}

type (
	draftDoneMsg struct {
		idea  string
		draft writer.Draft
		err   error
	}
	editorDoneMsg struct {
		field int
		path  string
		err   error
	}
)

func newCompose() composeModel {
	idea := textinput.New()
	idea.Placeholder = "a road-trip anthem about leaving a small town"
	idea.Prompt = "› "
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	title := textinput.New()
	title.Prompt = ""
	seconds := textinput.New()
	seconds.Prompt = ""
	seconds.CharLimit = 3
	newArea := func() textarea.Model {
		ta := textarea.New()
		ta.ShowLineNumbers = false
		ta.MaxHeight = 0
		ta.Prompt = ""
		return ta
	}
	return composeModel{idea: idea, spinner: sp, title: title, seconds: seconds, lyrics: newArea(), caption: newArea()}
}

func (c *composeModel) resize(w, h int) {
	c.idea.Width = max(20, w-6)
	c.title.Width = max(20, w-20)
	areaH := max(3, (h-12)/2)
	c.lyrics.SetWidth(max(20, w-4))
	c.lyrics.SetHeight(areaH)
	c.caption.SetWidth(max(20, w-4))
	c.caption.SetHeight(areaH)
}

func (c *composeModel) startIdea() tea.Cmd {
	c.step = stepIdea
	c.err = ""
	c.drafting = false
	c.idea.SetValue("")
	return c.idea.Focus()
}

// startEdit fills the form from song; newSeconds is the length a song
// without one starts with.
func (c *composeModel) startEdit(song library.Song, newSeconds float64) tea.Cmd {
	c.step = stepEdit
	c.err = ""
	c.drafting = false
	if song.Status == library.StatusDone || song.Status == library.StatusRendering {
		// Keep finished audio: edits become a new song.
		song = asCopy(song)
	}
	c.base = song
	c.title.SetValue(song.Title)
	c.seconds.SetValue(strconv.Itoa(int(cmp.Or(song.Seconds, newSeconds))))
	c.lyrics.SetValue(song.Lyrics)
	c.caption.SetValue(song.Caption.String())
	c.focus = fieldLyrics
	return c.applyFocus()
}

// asCopy turns song into an unsaved new song with the same words and style.
func asCopy(song library.Song) library.Song {
	song.ID, song.Status, song.Audio, song.Duration, song.Seed, song.Error, song.JobID = "", "", "", 0, 0, "", ""
	song.Started, song.Finished, song.Timings = time.Time{}, time.Time{}, nil
	return song
}

func (c *composeModel) applyFocus() tea.Cmd {
	c.title.Blur()
	c.seconds.Blur()
	c.lyrics.Blur()
	c.caption.Blur()
	switch c.focus {
	case fieldTitle:
		return c.title.Focus()
	case fieldSeconds:
		return c.seconds.Focus()
	case fieldLyrics:
		return c.lyrics.Focus()
	default:
		return c.caption.Focus()
	}
}

// song builds the edited song from the form.
func (c *composeModel) song() (library.Song, error) {
	s := c.base
	s.Title = strings.TrimSpace(c.title.Value())
	if s.Title == "" {
		s.Title = "Untitled"
	}
	secs, err := strconv.Atoi(strings.TrimSpace(c.seconds.Value()))
	if err != nil {
		return s, fmt.Errorf("length must be a whole number of seconds")
	}
	s.Seconds = float64(secs)
	s.Lyrics = strings.TrimSpace(c.lyrics.Value())
	s.Caption = writer.ParseCaption(c.caption.Value())
	return s, (writer.Draft{Lyrics: s.Lyrics, Caption: s.Caption, Seconds: s.Seconds}).Validate()
}

// leaveCompose returns to the screen that opened the form.
func (a app) leaveCompose() (app, tea.Cmd) {
	return a.show(a.back())
}

func (a app) updateCompose(msg tea.Msg) (app, tea.Cmd) {
	c := &a.compose
	switch msg := msg.(type) {
	case draftDoneMsg:
		c.drafting = false
		c.cancel = nil
		song := library.Song{Idea: msg.idea}
		if msg.err != nil {
			cmd := c.startEdit(song, a.defaultSeconds())
			c.err = "Draft failed: " + msg.err.Error() + ". Write the lyrics and style yourself, or esc to go back."
			if msg.draft.Lyrics != "" {
				c.lyrics.SetValue(msg.draft.Lyrics)
				c.caption.SetValue(msg.draft.Caption.String())
				c.title.SetValue(msg.draft.Title)
			}
			return a, cmd
		}
		song.Title, song.Lyrics, song.Caption, song.Seconds = msg.draft.Title, msg.draft.Lyrics, msg.draft.Caption, msg.draft.Seconds
		return a, c.startEdit(song, a.defaultSeconds())
	case editorDoneMsg:
		defer os.Remove(msg.path)
		if msg.err != nil {
			c.err = "Editor: " + msg.err.Error()
			return a, nil
		}
		data, err := os.ReadFile(msg.path)
		if err != nil {
			c.err = err.Error()
			return a, nil
		}
		text := strings.TrimRight(string(data), "\n")
		if msg.field == fieldLyrics {
			c.lyrics.SetValue(text)
		} else {
			c.caption.SetValue(text)
		}
		return a, nil
	case spinner.TickMsg:
		if !c.drafting {
			return a, nil
		}
		var cmd tea.Cmd
		c.spinner, cmd = c.spinner.Update(msg)
		return a, cmd
	case tea.KeyMsg:
		if c.step == stepIdea {
			return a.ideaKey(msg)
		}
		return a.editKey(msg)
	}
	return a.forwardCompose(msg)
}

func (a app) ideaKey(key tea.KeyMsg) (app, tea.Cmd) {
	c := &a.compose
	switch key.String() {
	case "esc":
		if c.cancel != nil {
			c.cancel()
			c.cancel = nil
		}
		c.drafting = false
		return a.leaveCompose()
	case "tab":
		if c.drafting {
			return a, nil
		}
		return a, c.startEdit(library.Song{Idea: strings.TrimSpace(c.idea.Value())}, a.defaultSeconds())
	case "enter":
		idea := strings.TrimSpace(c.idea.Value())
		if c.drafting || idea == "" {
			return a, nil
		}
		ctx, cancel := context.WithCancel(a.ctx)
		c.cancel = cancel
		c.drafting = true
		c.err = ""
		drafter := a.d.Drafter
		return a, tea.Batch(c.spinner.Tick, func() tea.Msg {
			d, err := drafter.Write(ctx, idea, writer.Options{})
			if ctx.Err() != nil {
				return nil
			}
			return draftDoneMsg{idea: idea, draft: d, err: err}
		})
	}
	if c.drafting {
		return a, nil
	}
	var cmd tea.Cmd
	c.idea, cmd = c.idea.Update(key)
	return a, cmd
}

func (a app) editKey(key tea.KeyMsg) (app, tea.Cmd) {
	c := &a.compose
	switch key.String() {
	case "esc":
		return a.leaveCompose()
	case "tab":
		c.focus = (c.focus + 1) % fieldCount
		return a, c.applyFocus()
	case "shift+tab":
		c.focus = (c.focus + fieldCount - 1) % fieldCount
		return a, c.applyFocus()
	case "ctrl+e":
		if c.focus != fieldLyrics && c.focus != fieldCaption {
			c.err = "Move to Lyrics or Style (tab) to open them in $EDITOR."
			return a, nil
		}
		return a, c.openEditor()
	case "ctrl+s", "ctrl+d":
		return a.saveCompose(key.String() == "ctrl+s")
	}
	return a.forwardCompose(key)
}

// saveCompose saves the form, queues it when render is set, and goes back to
// the screen that opened the form with the song selected in the library.
func (a app) saveCompose(render bool) (app, tea.Cmd) {
	c := &a.compose
	song, err := c.song()
	if err != nil {
		c.err = err.Error()
		return a, nil
	}
	song.Status = library.StatusDraft
	if a.d.Store.HasAudio(song) {
		song.Status = library.StatusDone
	}
	copied := false
	if song.ID != "" {
		err = a.d.Queue.SaveEdit(song)
		if errors.Is(err, queue.ErrBusy) {
			// It started rendering while being edited: keep the edits as a new song.
			song = asCopy(song)
			copied = true
		}
	}
	if song.ID == "" {
		song, err = a.d.Store.Create(song)
	}
	if err != nil {
		c.err = err.Error()
		return a, nil
	}
	a.notice = "Saved " + song.Title + "."
	if copied {
		a.notice = "That song started rendering, so your edits were saved as a new song."
	}
	if render {
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

func (a app) forwardCompose(msg tea.Msg) (app, tea.Cmd) {
	c := &a.compose
	var cmd tea.Cmd
	if c.step == stepIdea {
		c.idea, cmd = c.idea.Update(msg)
		return a, cmd
	}
	switch c.focus {
	case fieldTitle:
		c.title, cmd = c.title.Update(msg)
	case fieldSeconds:
		c.seconds, cmd = c.seconds.Update(msg)
	case fieldLyrics:
		c.lyrics, cmd = c.lyrics.Update(msg)
	default:
		c.caption, cmd = c.caption.Update(msg)
	}
	return a, cmd
}

func (c *composeModel) openEditor() tea.Cmd {
	field := c.focus
	text := c.lyrics.Value()
	if field == fieldCaption {
		text = c.caption.Value()
	}
	f, err := os.CreateTemp("", "songsmith-*.txt")
	if err != nil {
		c.err = err.Error()
		return nil
	}
	_, werr := f.WriteString(text + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		c.err = "could not write temp file for $EDITOR"
		return nil
	}
	cmd := editorCommand(f.Name())
	path := f.Name()
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{field: field, path: path, err: err} })
}

func (c composeModel) view(w int) string {
	var b strings.Builder
	if c.step == stepIdea {
		b.WriteString(headerStyle.Render("♪ New song") + "\n\n")
		b.WriteString("Describe the song: genre, mood, subject, anything.\n\n")
		b.WriteString(c.idea.View() + "\n\n")
		if c.drafting {
			b.WriteString(c.spinner.View() + " Drafting lyrics and style… (esc cancels)\n")
		} else {
			b.WriteString(dimStyle.Render("enter draft with the local LLM · tab write it yourself · esc back") + "\n")
		}
		return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
	}

	label := func(field int, name string) string {
		if c.focus == field {
			return selectedStyle.Render("› " + name)
		}
		return labelStyle.Render("  " + name)
	}
	header := "♪ Edit song"
	if c.base.ID == "" {
		header = "♪ New song"
	}
	b.WriteString(headerStyle.Render(header) + "\n")
	b.WriteString(label(fieldTitle, "Title  ") + " " + c.title.View() + "\n")
	b.WriteString(label(fieldSeconds, "Seconds") + " " + c.seconds.View() + dimStyle.Render("  max length; the model may end sooner") + "\n")
	b.WriteString(label(fieldLyrics, "Lyrics") + dimStyle.Render("  tags like [Verse] [Chorus] on their own lines") + "\n")
	b.WriteString(c.lyrics.View() + "\n")
	b.WriteString(label(fieldCaption, "Style") + dimStyle.Render("  Global Metadata: … / Vocal Details: … / Arrangement: …") + "\n")
	b.WriteString(c.caption.View() + "\n")
	if c.err != "" {
		b.WriteString(lipgloss.NewStyle().Width(w-2).Render(errorStyle.Render(c.err)) + "\n")
	}
	b.WriteString(dimStyle.Render("ctrl+s save & queue · ctrl+d save draft · tab next field · ctrl+e open in $EDITOR · esc discard"))
	return b.String()
}

// editorCommand opens path in $VISUAL, then $EDITOR, then vi.
func editorCommand(path string) *exec.Cmd {
	parts := strings.Fields(cmp.Or(strings.TrimSpace(os.Getenv("VISUAL")), strings.TrimSpace(os.Getenv("EDITOR")), "vi"))
	return exec.Command(parts[0], append(parts[1:], path)...)
}
