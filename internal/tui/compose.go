package tui

import (
	"cmp"
	"context"
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
	"github.com/lancekrogers/songsmith/internal/writer"
)

const (
	fieldTitle = iota
	fieldSeconds
	fieldLyrics
	fieldCaption
	fieldCount
)

type composeModel struct {
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
	c.err = ""
	c.drafting = false
	c.idea.SetValue("")
	return c.idea.Focus()
}

func (c *composeModel) startEdit(song library.Song) tea.Cmd {
	c.err = ""
	c.drafting = false
	if song.Status == library.StatusDone || song.Status == library.StatusRendering {
		// Keep finished audio: edits become a new song.
		song.ID, song.Status, song.Audio, song.Duration, song.Seed, song.Error = "", "", "", 0, 0, ""
		song.Started, song.Finished, song.Timings = time.Time{}, time.Time{}, nil
	}
	c.base = song
	c.title.SetValue(song.Title)
	c.seconds.SetValue(strconv.Itoa(int(cmp.Or(song.Seconds, 120))))
	c.lyrics.SetValue(song.Lyrics)
	caption := song.Caption.String()
	c.caption.SetValue(caption)
	c.focus = fieldLyrics
	return c.applyFocus()
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

func (m model) updateCompose(msg tea.Msg) (tea.Model, tea.Cmd) {
	c := &m.compose
	switch msg := msg.(type) {
	case draftDoneMsg:
		c.drafting = false
		c.cancel = nil
		song := library.Song{Idea: msg.idea, Seconds: 120}
		if msg.err != nil {
			cmd := c.startEdit(song)
			c.err = "Draft failed: " + msg.err.Error() + ". Write the lyrics and style yourself, or esc to go back."
			if msg.draft.Lyrics != "" {
				c.lyrics.SetValue(msg.draft.Lyrics)
				c.caption.SetValue(msg.draft.Caption.String())
				c.title.SetValue(msg.draft.Title)
			}
			m.mode = modeEdit
			return m, cmd
		}
		song.Title, song.Lyrics, song.Caption, song.Seconds = msg.draft.Title, msg.draft.Lyrics, msg.draft.Caption, msg.draft.Seconds
		m.mode = modeEdit
		return m, c.startEdit(song)
	case editorDoneMsg:
		defer os.Remove(msg.path)
		if msg.err != nil {
			c.err = "Editor: " + msg.err.Error()
			return m, nil
		}
		data, err := os.ReadFile(msg.path)
		if err != nil {
			c.err = err.Error()
			return m, nil
		}
		text := strings.TrimRight(string(data), "\n")
		if msg.field == fieldLyrics {
			c.lyrics.SetValue(text)
		} else {
			c.caption.SetValue(text)
		}
		return m, nil
	case spinner.TickMsg:
		if !c.drafting {
			return m, nil
		}
		var cmd tea.Cmd
		c.spinner, cmd = c.spinner.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if m.mode == modeIdea {
			return m.ideaKey(msg)
		}
		return m.editKey(msg)
	}
	return m.forwardCompose(msg)
}

func (m model) ideaKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := &m.compose
	switch key.String() {
	case "esc", "ctrl+c":
		if c.cancel != nil {
			c.cancel()
		}
		c.drafting = false
		m.mode = modeLibrary
		return m, nil
	case "tab":
		if c.drafting {
			return m, nil
		}
		m.mode = modeEdit
		return m, c.startEdit(library.Song{Idea: strings.TrimSpace(c.idea.Value()), Seconds: 120})
	case "enter":
		idea := strings.TrimSpace(c.idea.Value())
		if c.drafting || idea == "" {
			return m, nil
		}
		ctx, cancel := context.WithCancel(m.ctx)
		c.cancel = cancel
		c.drafting = true
		c.err = ""
		drafter := m.d.Drafter
		return m, tea.Batch(c.spinner.Tick, func() tea.Msg {
			d, err := drafter.Write(ctx, idea, writer.Options{})
			if ctx.Err() != nil {
				return nil
			}
			return draftDoneMsg{idea: idea, draft: d, err: err}
		})
	}
	if c.drafting {
		return m, nil
	}
	var cmd tea.Cmd
	c.idea, cmd = c.idea.Update(key)
	return m, cmd
}

func (m model) editKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := &m.compose
	switch key.String() {
	case "esc":
		m.mode = modeLibrary
		return m, nil
	case "tab":
		c.focus = (c.focus + 1) % fieldCount
		return m, c.applyFocus()
	case "shift+tab":
		c.focus = (c.focus + fieldCount - 1) % fieldCount
		return m, c.applyFocus()
	case "ctrl+e":
		if c.focus != fieldLyrics && c.focus != fieldCaption {
			c.err = "Move to Lyrics or Style (tab) to open them in $EDITOR."
			return m, nil
		}
		return m, c.openEditor()
	case "ctrl+s", "ctrl+d":
		song, err := c.song()
		if err != nil {
			c.err = err.Error()
			return m, nil
		}
		song.Status = library.StatusDraft
		if song.ID == "" {
			song, err = m.d.Store.Create(song)
		} else {
			err = m.d.Store.Save(song)
		}
		if err != nil {
			c.err = err.Error()
			return m, nil
		}
		m.notice = "Saved draft " + song.Title + "."
		if key.String() == "ctrl+s" {
			if _, err := m.d.Queue.Enqueue(song); err != nil {
				m.err = err.Error()
			} else {
				m.notice = "Queued " + song.Title + ". Rendering takes several minutes per minute of song."
			}
		}
		m.mode = modeLibrary
		m.selectID = song.ID
		return m, m.loadSongs()
	}
	return m.forwardCompose(key)
}

func (m model) forwardCompose(msg tea.Msg) (tea.Model, tea.Cmd) {
	c := &m.compose
	var cmd tea.Cmd
	if m.mode == modeIdea {
		c.idea, cmd = c.idea.Update(msg)
		return m, cmd
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
	return m, cmd
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
	editor := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], f.Name())...)
	path := f.Name()
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{field: field, path: path, err: err} })
}

func (c composeModel) view(ideaMode bool, w int) string {
	var b strings.Builder
	if ideaMode {
		b.WriteString(titleStyle.Render("♪ New song") + "\n\n")
		b.WriteString("Describe the song: genre, mood, subject, anything.\n\n")
		b.WriteString(c.idea.View() + "\n\n")
		if c.drafting {
			b.WriteString(c.spinner.View() + " Drafting lyrics and style… (esc cancels)\n")
		} else {
			b.WriteString(mutedStyle.Render("enter draft with the local LLM · tab write it yourself · esc back") + "\n")
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
	b.WriteString(titleStyle.Render(header) + "\n")
	b.WriteString(label(fieldTitle, "Title  ") + " " + c.title.View() + "\n")
	b.WriteString(label(fieldSeconds, "Seconds") + " " + c.seconds.View() + mutedStyle.Render("  max length; the model may end sooner") + "\n")
	b.WriteString(label(fieldLyrics, "Lyrics") + mutedStyle.Render("  tags like [Verse] [Chorus] on their own lines") + "\n")
	b.WriteString(c.lyrics.View() + "\n")
	b.WriteString(label(fieldCaption, "Style") + mutedStyle.Render("  Global Metadata: … / Vocal Details: … / Arrangement: …") + "\n")
	b.WriteString(c.caption.View() + "\n")
	if c.err != "" {
		b.WriteString(lipgloss.NewStyle().Width(w-2).Render(errStyle.Render(c.err)) + "\n")
	}
	b.WriteString(mutedStyle.Render("ctrl+s save & queue · ctrl+d save draft · tab next field · ctrl+e open in $EDITOR · esc discard"))
	return b.String()
}
