// Package tui is songsmith's terminal interface: compose, queue, watch and
// play songs.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/player"
	"github.com/lancekrogers/songsmith/internal/queue"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// Drafter drafts a song from an idea.
type Drafter interface {
	Write(ctx context.Context, idea string, opts writer.Options) (writer.Draft, error)
}

type Deps struct {
	Store   *library.Store
	Queue   *queue.Queue
	Drafter Drafter
	Player  *player.Player
	// WorkerNote explains why this process is not rendering, if it is not.
	WorkerNote string
	// DraftModel names the LLM used for drafts.
	DraftModel string
}

type mode int

const (
	modeLibrary mode = iota
	modeIdea
	modeEdit
)

type (
	songsMsg struct {
		songs []library.Song
		err   error
	}
	updateMsg   queue.Update
	playDoneMsg string
	tickMsg     time.Time
)

type model struct {
	d       Deps
	ctx     context.Context
	updates <-chan queue.Update

	width, height int
	mode          mode

	songs    []library.Song
	cursor   int
	progress map[string]backend.Progress
	detail   viewport.Model
	notice   string
	err      string
	// selectID is the song to select after the next library reload.
	selectID string

	compose composeModel
}

func newModel(ctx context.Context, d Deps, updates <-chan queue.Update) model {
	return model{
		d:        d,
		ctx:      ctx,
		updates:  updates,
		progress: map[string]backend.Progress{},
		detail:   viewport.New(0, 0),
		compose:  newCompose(),
	}
}

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, d Deps) error {
	updates, stop := d.Queue.Subscribe()
	defer stop()
	defer d.Player.Stop()
	p := tea.NewProgram(newModel(ctx, d, updates), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadSongs(), waitUpdate(m.updates), waitPlayDone(m.d.Player), tick())
}

func (m model) loadSongs() tea.Cmd {
	return func() tea.Msg {
		songs, err := m.d.Store.List()
		return songsMsg{songs, err}
	}
}

func waitUpdate(ch <-chan queue.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return updateMsg(u)
	}
}

func waitPlayDone(p *player.Player) tea.Cmd {
	return func() tea.Msg { return playDoneMsg(<-p.Done) }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) selected() (library.Song, bool) {
	if m.cursor < 0 || m.cursor >= len(m.songs) {
		return library.Song{}, false
	}
	return m.songs[m.cursor], true
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.compose.resize(msg.Width, msg.Height)
		m.layoutDetail()
		return m, nil
	case songsMsg:
		keep := m.selectID
		m.selectID = ""
		if s, ok := m.selected(); ok && keep == "" {
			keep = s.ID
		}
		m.songs = msg.songs
		if msg.err != nil {
			m.err = msg.err.Error()
		}
		m.cursor = 0
		for i, s := range m.songs {
			if s.ID == keep {
				m.cursor = i
			}
		}
		m.layoutDetail()
		return m, nil
	case updateMsg:
		cmds := []tea.Cmd{waitUpdate(m.updates)}
		if msg.Status == library.StatusRendering && msg.Progress.Stage != "" {
			m.progress[msg.SongID] = msg.Progress
			m.layoutDetail()
		} else {
			delete(m.progress, msg.SongID)
			if msg.Err != "" && msg.Status == library.StatusFailed {
				m.err = "render failed: " + msg.Err
			}
			cmds = append(cmds, m.loadSongs())
		}
		return m, tea.Batch(cmds...)
	case playDoneMsg:
		m.layoutDetail()
		return m, waitPlayDone(m.d.Player)
	case tickMsg:
		if m.d.Queue.Current() != "" {
			m.layoutDetail()
		}
		return m, tick()
	}

	if m.mode != modeLibrary {
		return m.updateCompose(msg)
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		return m.libraryKey(key)
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}

func (m model) libraryKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	song, ok := m.selected()
	switch key.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.layoutDetail()
		}
	case "down", "j":
		if m.cursor < len(m.songs)-1 {
			m.cursor++
			m.layoutDetail()
		}
	case "n":
		m.err = ""
		m.mode = modeIdea
		return m, m.compose.startIdea()
	case "e":
		if ok {
			m.err = ""
			m.mode = modeEdit
			return m, m.compose.startEdit(song)
		}
	case "enter", " ":
		if !ok {
			break
		}
		if m.d.Player.Current() == m.d.Store.AudioPath(song) {
			m.d.Player.Stop()
			m.notice = "Stopped."
		} else if song.Status == library.StatusDone {
			if err := m.d.Player.Play(m.d.Store.AudioPath(song)); err != nil {
				m.err = err.Error()
			} else {
				m.notice = "Playing " + song.Title + " (enter to stop)."
			}
		} else {
			m.notice = "Only finished songs can play."
		}
		m.layoutDetail()
	case "r":
		if !ok {
			break
		}
		if song.Status == library.StatusQueued || song.Status == library.StatusRendering {
			m.notice = "Already " + string(song.Status) + "."
			break
		}
		if song.Status == library.StatusDone {
			song.Seed = 0
		}
		if _, err := m.d.Queue.Enqueue(song); err != nil {
			m.err = err.Error()
		} else {
			m.notice = "Queued " + song.Title + "."
		}
		return m, m.loadSongs()
	case "x":
		if ok {
			if err := m.d.Queue.Cancel(song.ID); err != nil {
				m.err = err.Error()
			} else {
				m.notice = "Cancelled " + song.Title + "."
			}
			return m, m.loadSongs()
		}
	case "esc":
		m.err = ""
	default:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(key)
		return m, cmd
	}
	return m, nil
}

func (m *model) listWidth() int  { return max(36, m.width*2/5) }
func (m *model) bodyHeight() int { return max(3, m.height-4) }

// detailPaneWidth is the detail pane's width inside its border; the
// viewport sits inside the pane's one-column padding on each side.
func (m *model) detailPaneWidth() int { return max(24, m.width-m.listWidth()-4) }

func (m *model) layoutDetail() {
	w := m.detailPaneWidth() - 2
	m.detail.Width = w
	m.detail.Height = max(1, m.bodyHeight()-2)
	m.detail.SetContent(m.detailText(w))
}

func (m *model) detailText(width int) string {
	song, ok := m.selected()
	if !ok {
		return mutedStyle.Render("No songs yet. Press n to write one.")
	}
	wrap := lipgloss.NewStyle().Width(width)
	var b strings.Builder
	b.WriteString(titleStyle.Render(song.Title) + "\n")
	b.WriteString(statusBadge(song.Status))
	if m.d.Player.Current() == m.d.Store.AudioPath(song) {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(okColor).Render("▶ playing"))
	}
	b.WriteString("\n")
	if song.Status == library.StatusRendering {
		b.WriteString(renderProgress(m.progress[song.ID], song.Started, max(10, width-2)) + "\n")
	}
	if song.Error != "" {
		b.WriteString(wrap.Render(errStyle.Render(song.Error)) + "\n")
	}
	var meta []string
	if song.Duration > 0 {
		meta = append(meta, "length "+fmtDuration(song.Duration))
	} else {
		meta = append(meta, fmt.Sprintf("up to %.0fs", song.Seconds))
	}
	if song.Seed != 0 {
		meta = append(meta, fmt.Sprintf("seed %d", song.Seed))
	}
	if !song.Started.IsZero() && !song.Finished.IsZero() {
		meta = append(meta, "rendered in "+song.Finished.Sub(song.Started).Round(time.Second).String())
	}
	b.WriteString(mutedStyle.Render(strings.Join(meta, " · ")) + "\n")
	if song.Idea != "" {
		b.WriteString(wrap.Render(mutedStyle.Render("Idea: "+song.Idea)) + "\n")
	}
	if song.Status == library.StatusDone {
		b.WriteString(wrap.Render(mutedStyle.Render(m.d.Store.AudioPath(song))) + "\n")
	}
	b.WriteString("\n" + headingStyle.Render("Lyrics") + "\n")
	b.WriteString(wrap.Render(song.Lyrics) + "\n\n")
	b.WriteString(headingStyle.Render("Style") + "\n")
	b.WriteString(wrap.Render(song.Caption.String()) + "\n")
	return b.String()
}

func renderProgress(p backend.Progress, started time.Time, width int) string {
	elapsed := ""
	if !started.IsZero() {
		elapsed = "  " + time.Since(started).Round(time.Second).String()
	}
	if p.Stage == "" {
		return mutedStyle.Render("starting…" + elapsed)
	}
	if p.Max == 0 {
		return lipgloss.NewStyle().Foreground(runColor).Render(string(p.Stage)+"…") + mutedStyle.Render(elapsed)
	}
	label := fmt.Sprintf("%s %3d%%", p.Stage, p.Value*100/p.Max)
	barW := max(5, width-len(label)-len(elapsed)-2)
	filled := min(barW, barW*p.Value/p.Max)
	bar := lipgloss.NewStyle().Foreground(runColor).Render(strings.Repeat("█", filled)) + mutedStyle.Render(strings.Repeat("░", barW-filled))
	return bar + " " + label + mutedStyle.Render(elapsed)
}

func fmtDuration(s float64) string {
	d := time.Duration(s * float64(time.Second)).Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.mode != modeLibrary {
		return m.compose.view(m.mode == modeIdea, m.width)
	}
	header := titleStyle.Render("♪ Songsmith") + mutedStyle.Render(fmt.Sprintf("  %d songs · %s", len(m.songs), m.d.Store.Dir()))
	if m.d.WorkerNote != "" {
		header += "  " + errStyle.Render(m.d.WorkerNote)
	}

	listW := m.listWidth()
	var rows []string
	// Row: "› " + badge(11) + " " + title + " " + length(5) + " " + age(4), inside 2 columns of padding.
	titleW := max(8, listW-2-2-11-1-1-5-1-4)
	for i, s := range m.songs {
		title := s.Title
		if lipgloss.Width(title) > titleW {
			title = string([]rune(title)[:titleW-1]) + "…"
		}
		length := "    "
		if s.Duration > 0 {
			length = fmtDuration(s.Duration)
		}
		line := fmt.Sprintf("%s %-*s %5s %4s", statusBadge(s.Status), titleW, title, length, age(s.Created))
		if i == m.cursor {
			line = selectedStyle.Render("› ") + line
		} else {
			line = "  " + line
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		rows = append(rows, mutedStyle.Render("  (empty)"))
	}
	bodyH := m.bodyHeight()
	start := max(0, min(m.cursor-bodyH/2, len(rows)-(bodyH-2)))
	visible := rows[start:min(len(rows), start+bodyH-2)]
	list := paneStyle.Width(listW).Height(bodyH - 2).Render(strings.Join(visible, "\n"))
	detail := focusPane.Width(m.detailPaneWidth()).Height(bodyH - 2).Render(m.detail.View())
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, detail)

	status := mutedStyle.Render("n new · e edit · enter play/stop · r queue · x cancel · ↑↓ select · pgup/pgdn scroll · q quit")
	if m.err != "" {
		status = errStyle.Render(m.err)
	} else if m.notice != "" {
		status = m.notice
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status)
}
