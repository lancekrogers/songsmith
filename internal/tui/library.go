package tui

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

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
	// takes are the takes of each finished song.
	takes map[string][]library.Take

	// folders are the library's folders; open are those showing what is
	// in them; rows are the lines of the list, which cursor indexes.
	folders []string
	open    map[string]bool
	rows    []libRow
	// selectFolder is the folder to select once a reload contains it.
	selectFolder string
	// confirm, picker and naming are a delete waiting for y, a folder
	// being chosen and a folder name being typed.
	confirm *libConfirm
	picker  *folderPicker
	naming  *folderNaming
}

func newLibrary() libraryModel {
	return libraryModel{progress: map[string]backend.Progress{}, detail: viewport.New(0, 0), open: map[string]bool{}}
}

// selected is the song on the selected row; a folder row has none.
func (a app) selected() (library.Song, bool) {
	r, ok := a.selectedRow()
	if !ok || r.isFolder() || r.song >= len(a.library.songs) {
		return library.Song{}, false
	}
	return a.library.songs[r.song], true
}

func (a *app) applySongs(msg songsMsg) {
	keepSong, keepFolder := a.selectedKey()
	a.library.songs = msg.songs
	a.library.folders = msg.folders
	if msg.takes != nil {
		a.library.takes = msg.takes
	}
	if msg.err != nil {
		a.err = msg.err.Error()
	}
	a.rebuildRows(keepSong, keepFolder)
	a.layoutDetail()
}

// selectPending moves the cursor to selectID if the list already has it.
func (a *app) selectPending() {
	a.rebuildRows(a.selectedKey())
}

func (a app) updateLibrary(msg tea.Msg) (app, tea.Cmd) {
	if a.library.naming != nil {
		return a.updateNaming(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		a.library.detail, cmd = a.library.detail.Update(msg)
		return a, cmd
	}
	switch {
	case a.library.confirm != nil:
		return a.answerDelete(key)
	case a.library.picker != nil:
		return a.updatePicker(key)
	}
	a.notice = ""
	if folder, ok := a.selectedFolder(); ok {
		if next, cmd, done := a.updateFolderRow(key, folder); done {
			return next, cmd
		}
	}
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
		if a.library.cursor < len(a.library.rows)-1 {
			a.library.cursor++
			a.layoutDetail()
		}
	case "left", "h":
		if ok && song.Folder != "" {
			a.rebuildRows("", song.Folder)
			a.layoutDetail()
		}
	case "f":
		return a.openPicker(), nil
	case "d":
		return a.askDelete(), nil
	case "n":
		if ok && song.Folder != "" {
			return a, func() tea.Msg { return openComposeMsg{song: &library.Song{Folder: song.Folder}} }
		}
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
		queued, err := a.d.Queue.Enqueue(song)
		switch {
		case err != nil:
			a.err = "queue " + song.Title + ": " + err.Error()
		case queued.ID == song.ID:
			a.notice = "Queued " + song.Title + "."
		default:
			a.library.selectID = queued.ID
			a.notice = fmt.Sprintf("Queued %s; %s stays as it is.", queued.Title, song.Title)
			if queued.SeedPinned {
				a.notice = fmt.Sprintf("Queued %s with seed %d; it will be identical to %s unless the lyrics, style or seed change.", queued.Title, queued.Seed, song.Title)
			}
		}
		return a, a.loadSongs()
	case "m":
		if ok && a.canMaster(song) {
			return a, func() tea.Msg { return openMasterMsg{song: &song} }
		}
		if ok {
			a.notice = "m masters a finished song; render this one first."
		}
	case "t":
		if !ok {
			break
		}
		return a.cycleTake(song)
	case "v":
		if !ok {
			break
		}
		if strings.TrimSpace(song.Lyrics) == "" {
			a.notice = "v revises a song that has lyrics; e edits this one."
			if needsDraft(song) {
				a.notice = "v revises a song that has lyrics; g drafts them from the idea, e edits."
			}
			break
		}
		return a, func() tea.Msg { return openComposeMsg{song: &song, revise: true} }
	case "s":
		if !ok {
			break
		}
		if !hasSeed(song) {
			a.notice = "No seed yet: one is chosen when the song renders."
			break
		}
		if err := a.copyText(strconv.FormatInt(song.Seed, 10)); err != nil {
			a.err = "copy the seed: " + err.Error()
			break
		}
		a.notice = fmt.Sprintf("Seed copied: %d.", song.Seed)
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

// takeLine says which take the library plays, such as "take 2 of 3 ·
// mastered (Streaming)", for a song with takes.
func (a *app) takeLine(song library.Song) string {
	takes := a.library.takes[song.ID]
	i := library.TakeIndex(takes, song.Audio)
	if i == 0 {
		return ""
	}
	label := takes[i-1].Label()
	if where := takeWhere(takes, takes[i-1], len(a.cfg().Machines) > 0); where != "" {
		label += " " + where
	}
	return labelStyle.Render(fmt.Sprintf("take %d of %d", i, len(takes))) + dimStyle.Render(" · "+label)
}

// cycleTake makes the next take of a finished song the one the library
// plays.
func (a app) cycleTake(song library.Song) (app, tea.Cmd) {
	takes := a.library.takes[song.ID]
	if song.Status != library.StatusDone || len(takes) < 2 {
		a.notice = "t switches between the takes of a finished song with more than one."
		return a, nil
	}
	next := takes[library.TakeIndex(takes, song.Audio)%len(takes)]
	wasPlaying := a.d.Player.Current() == a.d.Store.AudioPath(song)
	saved, err := a.d.Store.SetCurrent(song.ID, next.File)
	if err != nil {
		a.err = "switch take: " + err.Error()
		return a, nil
	}
	if wasPlaying {
		a.d.Player.Stop()
	}
	a.replaceSong(saved)
	a.notice = fmt.Sprintf("Current take: %d of %d, %s · %s.", library.TakeIndex(takes, next.File), len(takes), next.File, next.Label())
	a.layoutDetail()
	return a, a.loadSongs()
}

// hasSeed reports whether a song has a seed to show or copy: a pinned one,
// or the one its latest render drew.
func hasSeed(song library.Song) bool { return song.SeedPinned || song.Seed != 0 }

// copyText puts text on the clipboard: the system's, or failing that the
// terminal's through OSC 52.
func (a app) copyText(text string) error {
	if a.d.Clipboard != nil {
		return a.d.Clipboard(text)
	}
	if err := clipboard.WriteAll(text); err != nil {
		termenv.Copy(text)
	}
	return nil
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
	return a.libraryKeysFor(a.library.detail.TotalLineCount() > a.library.detail.Height)
}

// libraryKeysFor is libraryKeys with the scroll key given.
func (a app) libraryKeysFor(scroll bool) []string {
	l := a.library
	switch {
	case l.confirm != nil:
		return []string{"y delete", "any other key keeps it"}
	case l.picker != nil:
		return []string{"↑/↓ choose", "enter move here", "esc cancel"}
	case l.naming != nil && l.naming.rename:
		return []string{"enter rename", "esc cancel"}
	case l.naming != nil:
		return []string{"enter make the folder and move", "esc cancel"}
	}
	var keys []string
	if len(l.rows) > 1 {
		keys = append(keys, "↑/↓ select")
	}
	if folder, ok := a.selectedFolder(); ok {
		if l.open[folder] {
			keys = append(keys, "enter close")
		} else {
			keys = append(keys, "enter open")
		}
		keys = append(keys, "n new song here", "f move", "e rename", "d delete")
		if parentFolder(folder) != "" {
			keys = append(keys, "← up")
		}
		return append(keys, "esc back")
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
			keys = append(keys, "r new version")
			if a.canMaster(song) {
				keys = append(keys, "m master")
			}
			if len(a.library.takes[song.ID]) > 1 {
				keys = append(keys, "t next take")
			}
		case library.StatusDraft, library.StatusFailed:
			if needsDraft(song) {
				keys = append(keys, "g draft lyrics and style")
			}
			keys = append(keys, "r render")
		case library.StatusQueued, library.StatusRendering:
			keys = append(keys, "x cancel")
		}
		keys = append(keys, "e edit")
		if strings.TrimSpace(song.Lyrics) != "" {
			keys = append(keys, "v revise / extend")
		}
		if hasSeed(song) {
			keys = append(keys, "s copy seed")
		}
		keys = append(keys, "f move to folder", "d delete")
		if song.Folder != "" {
			keys = append(keys, "← up")
		}
	}
	keys = append(keys, "n new song")
	if scroll {
		keys = append(keys, "pgup/pgdn scroll")
	}
	return append(keys, "esc back")
}

func (a *app) listWidth() int  { return max(36, a.width*2/5) }
func (a *app) bodyHeight() int { return max(3, a.height-2-messageRows-a.libraryFooterRows()) }

// libraryFooterRows is the lines the key footer takes, counting the scroll
// key so the layout does not depend on itself.
func (a *app) libraryFooterRows() int {
	return len(footerRows(a.width, "  ", a.libraryKeysFor(true)...))
}

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
	switch {
	case a.library.picker != nil:
		return a.pickerText(width)
	case a.library.naming != nil:
		return a.namingText(width)
	}
	if folder, ok := a.selectedFolder(); ok {
		return a.folderText(folder, width)
	}
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
	if song.DerivedFrom != "" {
		b.WriteString(wrap.Render(dimStyle.Render("from "+a.songTitle(song.DerivedFrom))) + "\n")
	}
	_, lost := lostMachine(song)
	_, _, stuck := a.noMachine(song)
	if where := a.whereText(song); where != "" {
		style := dimStyle
		if song.Status == library.StatusRendering || lost || stuck {
			style = warningStyle
		}
		b.WriteString(wrap.Render(style.Render(where)) + "\n")
	}
	if song.Status == library.StatusRendering && !lost {
		b.WriteString(renderProgress(a.library.progress[song.ID], song.Started, max(10, width-2)) + "\n")
	}
	if j, ok := a.mastering.running[song.ID]; ok {
		label := fmt.Sprintf(" mastering %d%%", int(j.done*100))
		if j.stopping {
			label = " stopping…"
		}
		b.WriteString(progressBar(j.done, max(5, width-2-len(label))) + warningStyle.Render(label) + "\n")
	}
	if line := a.takeLine(song); line != "" {
		b.WriteString(wrap.Render(line) + "\n")
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
	if !song.Started.IsZero() && !song.Finished.IsZero() {
		meta = append(meta, "rendered in "+song.Finished.Sub(song.Started).Round(time.Second).String())
	}
	b.WriteString(dimStyle.Render(strings.Join(meta, " · ")) + "\n")
	b.WriteString(wrap.Render(a.seedLine(song)) + "\n")
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

// songTitle names a library song by ID, or says it is gone.
func (a *app) songTitle(id string) string {
	if s, ok := a.songByID(id); ok {
		return s.Title
	}
	return "a song no longer in the library"
}

func (a *app) songByID(id string) (library.Song, bool) {
	for _, s := range a.library.songs {
		if s.ID == id {
			return s, true
		}
	}
	return library.Song{}, false
}

// seedLine says which seed the song renders with, and whether it is the
// seed of the song it was copied from.
func (a *app) seedLine(song library.Song) string {
	value, how := "", "chosen at render"
	switch {
	case song.SeedPinned:
		value, how = strconv.FormatInt(song.Seed, 10), "pinned"
	case song.Seed != 0:
		value, how = strconv.FormatInt(song.Seed, 10), "random"
	}
	line := labelStyle.Render("Seed")
	if value != "" {
		line += " " + value
	}
	line += dimStyle.Render(" · " + how)
	if src, ok := a.songByID(song.DerivedFrom); ok && value != "" && hasSeed(src) && src.Seed == song.Seed {
		line += dimStyle.Render(" · from " + src.Title)
	}
	return line
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
	header := headerStyle.Render("♪ Library") + dimStyle.Render("  "+count(len(a.library.songs), "song")+" · "+a.d.Store.Dir())
	if a.d.WorkerNote != nil {
		if note := a.d.WorkerNote(); note != "" {
			header += "  " + errorStyle.Render(note)
		}
	}

	listW := a.listWidth()
	var rows []string
	// first is each list row's first line; a busy song adds lines under
	// its title saying where it renders.
	var first []int
	// Row: "› " + indent + badge(11) + " " + title + " " + length(5) + " " + age(4), inside 2 columns of padding.
	fullW := max(8, listW-2-2-11-1-1-5-1-4)
	for i, r := range a.library.rows {
		// Deep folders stop indenting where a title would no longer fit.
		indent := strings.Repeat("  ", min(r.depth, 6, (fullW-4)/2))
		titleW := max(4, fullW-len(indent))
		var line string
		if r.isFolder() {
			icon := "▸ "
			if a.library.open[r.folder] {
				icon = "▾ "
			}
			songs := a.songsUnder(r.folder)
			n := count(len(songs), "song")
			nStyle := dimStyle
			if slices.ContainsFunc(songs, songBusy) {
				nStyle = warningStyle
			}
			nameW := titleW + 11 + 1 + 5 + 1 + 4 - 2 - 1 - len(n)
			name := ansi.Truncate(path.Base(r.folder)+"/", max(4, nameW), "…")
			name += strings.Repeat(" ", max(0, nameW-ansi.StringWidth(name)))
			line = indent + labelStyle.Render(icon+name) + " " + nStyle.Render(n)
		} else {
			s := a.library.songs[r.song]
			title := ansi.Truncate(s.Title, titleW, "…")
			title += strings.Repeat(" ", max(0, titleW-ansi.StringWidth(title)))
			length := "    "
			if s.Duration > 0 {
				length = fmtDuration(s.Duration)
			}
			line = indent + fmt.Sprintf("%s %s %5s %4s", statusBadge(s.Status), title, length, age(s.Created))
		}
		if i == a.library.cursor {
			line = selectedStyle.Render("› ") + line
		} else {
			line = "  " + line
		}
		first = append(first, len(rows))
		rows = append(rows, line)
		if !r.isFolder() {
			if where := a.whereLine(a.library.songs[r.song]); where != "" {
				pad := strings.Repeat(" ", min(2+len(indent)+12, listW-2-16))
				lines := []string{warningStyle.Render("↳ " + where)}
				if w := listW - 2 - len(pad); ansi.StringWidth("↳ "+where) > w {
					lines = hangLines("↳ "+where, warningStyle, w)
				}
				for _, l := range lines {
					rows = append(rows, pad+l)
				}
			}
		}
	}
	if len(rows) == 0 {
		rows = append(rows, dimStyle.Render("  (empty)"))
	}
	bodyH := a.bodyHeight()
	n := bodyH - 2
	top, bottom := 0, 0
	if c := a.library.cursor; c < len(first) {
		top, bottom = first[c], len(rows)-1
		if c+1 < len(first) {
			bottom = first[c+1] - 1
		}
	}
	start := max(0, min(top-bodyH/2, len(rows)-n))
	if bottom >= start+n {
		start = bottom - n + 1
	}
	start = max(0, min(start, top))
	visible := rows[start:min(len(rows), start+n)]
	list := paneStyle.Width(listW).Height(bodyH - 2).Render(strings.Join(visible, "\n"))
	detail := focusPane.Width(a.detailPaneWidth()).Height(bodyH - 2).Render(a.library.detail.View())
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, detail)

	msg := launcherState{err: a.err, notice: a.notice}.messageLines(max(a.width-2, 10))
	if c := a.library.confirm; c != nil {
		msg = wrapMessage(c.question, warningStyle, max(a.width-2, 10))
	}
	for i := range msg {
		msg[i] = " " + msg[i]
	}
	msg = fitLines(msg, messageRows)
	return lipgloss.JoinVertical(lipgloss.Left,
		ansi.Truncate(header, a.width, "…"),
		body,
		strings.Join(msg, "\n"),
		strings.Join(footerRows(a.width, "  ", a.libraryKeys()...), "\n"),
	)
}
