package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/library"
)

// libraryModel is the song list. It stays current on every screen, since the
// launcher's count, queue chip and Now rendering item read it.
type libraryModel struct {
	songs    []library.Song
	cursor   int
	progress map[string]backend.Progress
	detail   viewport.Model
	// selectID is the song to select once a reload contains it.
	selectID string
}

func newLibrary() libraryModel {
	return libraryModel{progress: map[string]backend.Progress{}, detail: viewport.New(0, 0)}
}

func (a app) selected() (library.Song, bool) {
	l := a.library
	if l.cursor < 0 || l.cursor >= len(l.songs) {
		return library.Song{}, false
	}
	return l.songs[l.cursor], true
}

func (a *app) applySongs(msg songsMsg) {
	keep := a.library.selectID
	if s, ok := a.selected(); ok && keep == "" {
		keep = s.ID
	}
	a.library.songs = msg.songs
	if msg.err != nil {
		a.err = msg.err.Error()
	}
	a.library.cursor = min(a.library.cursor, max(0, len(a.library.songs)-1))
	for i, s := range a.library.songs {
		if s.ID == keep {
			a.library.cursor = i
			if keep == a.library.selectID {
				a.library.selectID = ""
			}
		}
	}
	a.layoutDetail()
}

// selectPending moves the cursor to selectID if the list already has it.
func (a *app) selectPending() {
	for i, s := range a.library.songs {
		if s.ID == a.library.selectID {
			a.library.cursor, a.library.selectID = i, ""
		}
	}
}

func (a app) updateLibrary(msg tea.Msg) (app, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		a.library.detail, cmd = a.library.detail.Update(msg)
		return a, cmd
	}
	a.notice = ""
	song, ok := a.selected()
	switch key.String() {
	case "esc":
		a.err = ""
		return a, func() tea.Msg { return switchScreenMsg(screenLauncher) }
	case "up", "k":
		if a.library.cursor > 0 {
			a.library.cursor--
			a.layoutDetail()
		}
	case "down", "j":
		if a.library.cursor < len(a.library.songs)-1 {
			a.library.cursor++
			a.layoutDetail()
		}
	case "n":
		return a, func() tea.Msg { return openComposeMsg{} }
	case "e":
		if ok {
			return a, func() tea.Msg { return openComposeMsg{song: &song} }
		}
	case "g":
		if ok && needsDraft(song) {
			return a, func() tea.Msg { return openComposeMsg{song: &song, draft: true} }
		}
		if ok {
			a.notice = "g drafts lyrics and style for a song that has an idea and neither yet; e edits this one."
		}
	case "enter", " ":
		if !ok {
			break
		}
		if a.d.Player.Current() == a.d.Store.AudioPath(song) {
			a.d.Player.Stop()
			a.notice = "Stopped."
		} else if song.Status == library.StatusDone {
			if err := a.d.Player.Play(a.d.Store.AudioPath(song)); err != nil {
				a.err = err.Error()
			} else {
				a.notice = "Playing " + song.Title + " (enter to stop)."
			}
		} else {
			a.notice = "Only finished songs can play."
		}
		a.layoutDetail()
	case "r":
		if !ok {
			break
		}
		if song.Status == library.StatusQueued || song.Status == library.StatusRendering {
			a.notice = "Already " + string(song.Status) + "."
			break
		}
		if err := song.CheckRender(); err != nil {
			a.notice = cantRender(song, err)
			break
		}
		if _, err := a.d.Queue.Enqueue(song); err != nil {
			a.err = "queue " + song.Title + ": " + err.Error()
		} else {
			a.notice = "Queued " + song.Title + "."
			if song.Status == library.StatusDone && song.SeedPinned {
				a.notice = fmt.Sprintf("Queued a new take of %s with seed %d; it will be identical unless the lyrics, style or seed change.", song.Title, song.Seed)
			}
		}
		return a, a.loadSongs()
	case "x":
		if ok {
			if err := a.d.Queue.Cancel(song.ID); err != nil {
				a.err = err.Error()
			} else {
				a.notice = "Cancelled " + song.Title + "."
			}
			return a, a.loadSongs()
		}
	default:
		var cmd tea.Cmd
		a.library.detail, cmd = a.library.detail.Update(key)
		return a, cmd
	}
	return a, nil
}

// needsDraft reports whether a song has an idea and nothing written yet,
// so drafting it is the next step.
func needsDraft(song library.Song) bool {
	if song.Status == library.StatusQueued || song.Status == library.StatusRendering {
		return false
	}
	c := song.Caption
	return strings.TrimSpace(song.Idea) != "" && strings.TrimSpace(song.Lyrics+c.GlobalMetadata+c.VocalDetails+c.Arrangement) == ""
}

// cantRender says, in plain words, what a song lacks before it can render
// and the key that fixes it.
func cantRender(song library.Song, err error) string {
	c := song.Caption
	var missing []string
	if strings.TrimSpace(song.Lyrics) == "" {
		missing = append(missing, "no lyrics")
	}
	switch {
	case strings.TrimSpace(c.GlobalMetadata+c.VocalDetails+c.Arrangement) == "":
		missing = append(missing, "no style")
	case strings.TrimSpace(c.GlobalMetadata) == "" || strings.TrimSpace(c.VocalDetails) == "" || strings.TrimSpace(c.Arrangement) == "":
		missing = append(missing, "an unfinished style")
	}
	what := joinAnd(missing)
	if what == "" {
		what = renderProblems(err)
	}
	if needsDraft(song) {
		return "Can't render yet: " + what + ". g drafts them from the idea, e edits."
	}
	return "Can't render yet: " + what + ". e edits it."
}

// renderProblems lists what CheckRender found, in one line.
func renderProblems(err error) string {
	var msgs []string
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			msgs = append(msgs, e.Error())
		}
	} else {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

// libraryKeys lists the keys that do something for the selected song.
func (a app) libraryKeys() []string {
	var keys []string
	if len(a.library.songs) > 1 {
		keys = append(keys, "↑/↓ select")
	}
	if song, ok := a.selected(); ok {
		playing := a.d.Player.Current() == a.d.Store.AudioPath(song)
		switch {
		case playing:
			keys = append(keys, "enter stop")
		case song.Status == library.StatusDone:
			keys = append(keys, "enter play")
		}
		switch song.Status {
		case library.StatusDone:
			keys = append(keys, "r new take")
		case library.StatusDraft, library.StatusFailed:
			if needsDraft(song) {
				keys = append(keys, "g draft lyrics and style")
			}
			keys = append(keys, "r render")
		case library.StatusQueued, library.StatusRendering:
			keys = append(keys, "x cancel")
		}
		keys = append(keys, "e edit")
	}
	keys = append(keys, "n new song")
	if a.library.detail.TotalLineCount() > a.library.detail.Height {
		keys = append(keys, "pgup/pgdn scroll")
	}
	return append(keys, "esc back")
}

func (a *app) listWidth() int  { return max(36, a.width*2/5) }
func (a *app) bodyHeight() int { return max(3, a.height-3-messageRows) }

// detailPaneWidth is the detail pane's width inside its border; the
// viewport sits inside the pane's one-column padding on each side.
func (a *app) detailPaneWidth() int { return max(24, a.width-a.listWidth()-4) }

func (a *app) layoutDetail() {
	w := a.detailPaneWidth() - 2
	a.library.detail.Width = w
	a.library.detail.Height = max(1, a.bodyHeight()-2)
	a.library.detail.SetContent(a.detailText(w))
}

const emptyLibrary = "No songs yet. Press n to write one: describe an idea, and the writer drafts lyrics and a style for you to edit and render."

func (a *app) detailText(width int) string {
	wrap := lipgloss.NewStyle().Width(width)
	song, ok := a.selected()
	if !ok {
		return wrap.Render(dimStyle.Render(emptyLibrary))
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render(song.Title) + "\n")
	b.WriteString(statusBadge(song.Status))
	if a.d.Player.Current() == a.d.Store.AudioPath(song) {
		b.WriteString("  " + statusStyle.Render("▶ playing"))
	}
	b.WriteString("\n")
	if song.Status == library.StatusRendering {
		b.WriteString(renderProgress(a.library.progress[song.ID], song.Started, max(10, width-2)) + "\n")
	}
	if song.Error != "" {
		b.WriteString(wrap.Render(errorStyle.Render(song.Error)) + "\n")
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
	b.WriteString(dimStyle.Render(strings.Join(meta, " · ")) + "\n")
	if song.Idea != "" {
		b.WriteString(wrap.Render(dimStyle.Render("Idea: "+song.Idea)) + "\n")
	}
	if song.Status == library.StatusDone {
		b.WriteString(wrap.Render(dimStyle.Render(a.d.Store.AudioPath(song))) + "\n")
	}
	empty := "not written yet · e to write them yourself"
	if strings.TrimSpace(song.Idea) != "" {
		empty = "not written yet · g drafts them from the idea · e to write them yourself"
	}
	section := func(text string) string {
		if strings.TrimSpace(text) == "" {
			return wrap.Render(dimStyle.Render(empty))
		}
		return wrap.Render(text)
	}
	b.WriteString("\n" + headingStyle.Render("Lyrics") + "\n")
	b.WriteString(section(song.Lyrics) + "\n\n")
	b.WriteString(headingStyle.Render("Style") + "\n")
	b.WriteString(section(song.Caption.String()) + "\n")
	return b.String()
}

func renderProgress(p backend.Progress, started time.Time, width int) string {
	elapsed := ""
	if !started.IsZero() {
		elapsed = "  " + time.Since(started).Round(time.Second).String()
	}
	if p.Stage == "" {
		return dimStyle.Render("starting…" + elapsed)
	}
	if p.Max == 0 {
		return warningStyle.Render(string(p.Stage)+"…") + dimStyle.Render(elapsed)
	}
	label := fmt.Sprintf("%s %3d%%", p.Stage, p.Value*100/p.Max)
	barW := max(5, width-len(label)-len(elapsed)-2)
	filled := min(barW, barW*p.Value/p.Max)
	bar := warningStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", barW-filled))
	return bar + " " + label + dimStyle.Render(elapsed)
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

func (a app) libraryView() string {
	header := headerStyle.Render("♪ Library") + dimStyle.Render(fmt.Sprintf("  %d songs · %s", len(a.library.songs), a.d.Store.Dir()))
	if a.d.WorkerNote != nil {
		if note := a.d.WorkerNote(); note != "" {
			header += "  " + errorStyle.Render(note)
		}
	}

	listW := a.listWidth()
	var rows []string
	// Row: "› " + badge(11) + " " + title + " " + length(5) + " " + age(4), inside 2 columns of padding.
	titleW := max(8, listW-2-2-11-1-1-5-1-4)
	for i, s := range a.library.songs {
		title := ansi.Truncate(s.Title, titleW, "…")
		title += strings.Repeat(" ", max(0, titleW-ansi.StringWidth(title)))
		length := "    "
		if s.Duration > 0 {
			length = fmtDuration(s.Duration)
		}
		line := fmt.Sprintf("%s %s %5s %4s", statusBadge(s.Status), title, length, age(s.Created))
		if i == a.library.cursor {
			line = selectedStyle.Render("› ") + line
		} else {
			line = "  " + line
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		rows = append(rows, dimStyle.Render("  (empty)"))
	}
	bodyH := a.bodyHeight()
	start := max(0, min(a.library.cursor-bodyH/2, len(rows)-(bodyH-2)))
	visible := rows[start:min(len(rows), start+bodyH-2)]
	list := paneStyle.Width(listW).Height(bodyH - 2).Render(strings.Join(visible, "\n"))
	detail := focusPane.Width(a.detailPaneWidth()).Height(bodyH - 2).Render(a.library.detail.View())
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, detail)

	msg := launcherState{err: a.err, notice: a.notice}.messageLines(max(a.width-2, 10))
	for i := range msg {
		msg[i] = " " + msg[i]
	}
	msg = fitLines(msg, messageRows)
	return lipgloss.JoinVertical(lipgloss.Left,
		ansi.Truncate(header, a.width, "…"),
		body,
		strings.Join(msg, "\n"),
		footer(a.width, "  ", a.libraryKeys()...),
	)
}
