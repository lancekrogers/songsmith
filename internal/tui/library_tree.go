package tui

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/library"
)

// libRow is one line of the library list: a folder, or a song.
type libRow struct {
	folder string
	// song is the index in libraryModel.songs, or -1 for a folder.
	song  int
	depth int
}

func (r libRow) isFolder() bool { return r.song < 0 }

// libConfirm is a delete waiting for y. It names what it deletes, so a
// reload of the list never changes the target.
type libConfirm struct {
	songID, folder string
	// count is how many songs the folder held when asked.
	count int
}

// folderPicker chooses where the selected song or folder goes.
type folderPicker struct {
	songID, folder string
	name           string
	// choices are the destinations: newFolderChoice, then "" for the top
	// level, then every folder the subject can go in.
	choices []string
	cursor  int
	current string
}

// newFolderChoice is the picker row that names a new folder.
const newFolderChoice = "\x00new"

// folderNaming is the name being typed for a new or renamed folder.
type folderNaming struct {
	input textinput.Model
	// songID or folder is what goes in the new folder; rename renames
	// folder in place instead.
	songID, folder string
	rename         bool
	err            string
}

func parentFolder(f string) string {
	if p := path.Dir(f); p != "." {
		return p
	}
	return ""
}

func folderLabel(f string) string {
	if f == "" {
		return "Library (top level)"
	}
	return f
}

// allFolders is every folder the list knows, with the parents of each
// song's folder, so a folder made by another process still shows.
func (l *libraryModel) allFolders() []string {
	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		for f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
			f = parentFolder(f)
		}
	}
	for _, f := range l.folders {
		add(f)
	}
	for _, s := range l.songs {
		add(s.Folder)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// buildRows lays out the list: in each folder, its folders by name and
// then its songs newest first, with closed folders' contents hidden.
func (l *libraryModel) buildRows() []libRow {
	children := map[string][]string{}
	for _, f := range l.allFolders() {
		p := parentFolder(f)
		children[p] = append(children[p], f)
	}
	songsIn := map[string][]int{}
	for i, s := range l.songs {
		songsIn[s.Folder] = append(songsIn[s.Folder], i)
	}
	var rows []libRow
	var add func(parent string, depth int)
	add = func(parent string, depth int) {
		for _, f := range children[parent] {
			rows = append(rows, libRow{folder: f, song: -1, depth: depth})
			if l.open[f] {
				add(f, depth+1)
			}
		}
		for _, i := range songsIn[parent] {
			rows = append(rows, libRow{song: i, depth: depth})
		}
	}
	add("", 0)
	return rows
}

// reveal opens folder and every folder it is in.
func (l *libraryModel) reveal(folder string) {
	for f := folder; f != ""; f = parentFolder(f) {
		l.open[f] = true
	}
}

func (a app) selectedRow() (libRow, bool) {
	l := a.library
	if l.cursor < 0 || l.cursor >= len(l.rows) {
		return libRow{}, false
	}
	return l.rows[l.cursor], true
}

func (a app) selectedFolder() (string, bool) {
	r, ok := a.selectedRow()
	return r.folder, ok && r.isFolder()
}

// selectedKey names the selected row: a song ID or a folder.
func (a app) selectedKey() (songID, folder string) {
	if s, ok := a.selected(); ok {
		return s.ID, ""
	}
	f, _ := a.selectedFolder()
	return "", f
}

// rowKeyAfter names the row the cursor should land on once the selected
// row leaves the list: the next row, or the one before when it is last.
func (a app) rowKeyAfter() (songID, folder string) {
	l := a.library
	skip := func(r libRow) bool {
		cur := l.rows[l.cursor]
		return cur.isFolder() && !r.isFolder() && library.InFolder(l.songs[r.song].Folder, cur.folder) ||
			cur.isFolder() && r.isFolder() && library.InFolder(r.folder, cur.folder)
	}
	pick := func(i int) (string, string) {
		r := l.rows[i]
		if r.isFolder() {
			return "", r.folder
		}
		return l.songs[r.song].ID, ""
	}
	for i := l.cursor + 1; i < len(l.rows); i++ {
		if !skip(l.rows[i]) {
			return pick(i)
		}
	}
	if l.cursor > 0 {
		return pick(l.cursor - 1)
	}
	return "", ""
}

// rebuildRows lays the list out again and puts the cursor on the song or
// folder given, on a pending selection once the list has it, or failing
// those on the closest visible folder holding the song.
func (a *app) rebuildRows(keepSong, keepFolder string) {
	l := &a.library
	if l.selectID != "" {
		if s, ok := a.songByID(l.selectID); ok {
			l.reveal(s.Folder)
			keepSong, keepFolder, l.selectID = s.ID, "", ""
		}
	}
	if l.selectFolder != "" && slices.Contains(l.allFolders(), l.selectFolder) {
		l.reveal(parentFolder(l.selectFolder))
		keepSong, keepFolder, l.selectFolder = "", l.selectFolder, ""
	}
	l.rows = l.buildRows()
	if s, ok := a.songByID(keepSong); ok && keepFolder == "" {
		// Should the song be in a closed folder, the outermost closed
		// folder holding it is selected instead.
		for f := s.Folder; f != ""; f = parentFolder(f) {
			if !l.open[f] {
				keepFolder = f
			}
		}
	}
	l.cursor = min(l.cursor, max(0, len(l.rows)-1))
	found := false
	for i, r := range l.rows {
		if !r.isFolder() && keepSong != "" && l.songs[r.song].ID == keepSong {
			l.cursor, found = i, true
			break
		}
	}
	if !found && keepFolder != "" {
		for i, r := range l.rows {
			if r.isFolder() && r.folder == keepFolder {
				l.cursor = i
				break
			}
		}
	}
}

// songsUnder lists the songs in folder and the folders inside it.
func (a app) songsUnder(folder string) []library.Song {
	var out []library.Song
	for _, s := range a.library.songs {
		if library.InFolder(s.Folder, folder) {
			out = append(out, s)
		}
	}
	return out
}

func songBusy(s library.Song) bool {
	return s.Status == library.StatusQueued || s.Status == library.StatusRendering
}

// cantChange says why a song cannot be moved or deleted now, or "".
func (a app) cantChange(s library.Song, verb string) string {
	switch {
	case songBusy(s):
		return fmt.Sprintf("%s is %s, so it can't be %s; x cancels it.", songName(s), s.Status, verb)
	case a.mastering.running[s.ID] != nil:
		return fmt.Sprintf("%s is being mastered, so it can't be %s until that finishes.", songName(s), verb)
	}
	return ""
}

// cantChangeFolder says why a folder cannot be moved or deleted now, or "".
func (a app) cantChangeFolder(folder, verb string) string {
	for _, s := range a.songsUnder(folder) {
		if why := a.cantChange(s, verb); why != "" {
			return folder + " holds " + why
		}
	}
	return ""
}

// stopPlayingUnder stops playback of any file in dir, which is about to
// move or go.
func (a app) stopPlayingUnder(dir string) {
	if cur := a.d.Player.Current(); cur != "" && strings.HasPrefix(cur, filepath.Clean(dir)+string(filepath.Separator)) {
		a.d.Player.Stop()
	}
}

func (a app) songFolderPath(s library.Song) string {
	return filepath.Dir(a.d.Store.AudioPath(s))
}

func (a app) folderPath(folder string) string {
	return filepath.Join(a.d.Store.Dir(), filepath.FromSlash(folder))
}

// askDelete asks to delete the selected song or folder.
func (a app) askDelete() app {
	if s, ok := a.selected(); ok {
		if why := a.cantChange(s, "deleted"); why != "" {
			a.notice = why
			return a
		}
		a.library.confirm = &libConfirm{songID: s.ID}
		takes := len(a.library.takes[s.ID])
		what := quote(songName(s))
		if takes > 0 {
			what = quote(songName(s)) + " and its " + count(takes, "take")
		}
		a.notice = "Delete " + what + " for good? y deletes it · any other key keeps it."
		return a
	}
	folder, ok := a.selectedFolder()
	if !ok {
		return a
	}
	if why := a.cantChangeFolder(folder, "deleted"); why != "" {
		a.notice = why
		return a
	}
	n := len(a.songsUnder(folder))
	a.library.confirm = &libConfirm{folder: folder, count: n}
	a.notice = "Delete the empty folder " + quote(folder) + "? y deletes it · any other key keeps it."
	if n > 0 {
		a.notice = "Delete the folder " + quote(folder) + " and the " + count(n, "song") + " in it, with all their takes, for good? y deletes it all · any other key keeps it."
	}
	return a
}

func quote(s string) string { return "“" + s + "”" }

// answerDelete handles the key pressed after askDelete: y deletes, and
// any other key keeps everything.
func (a app) answerDelete(key tea.KeyMsg) (app, tea.Cmd) {
	c := *a.library.confirm
	a.library.confirm, a.notice = nil, ""
	if key.String() != "y" {
		a.notice = "Kept it."
		return a, nil
	}
	nextSong, nextFolder := a.rowKeyAfter()
	if c.songID != "" {
		s, ok := a.songByID(c.songID)
		if !ok {
			a.err = "That song is no longer in the library."
			return a, a.loadSongs()
		}
		a.stopPlayingUnder(a.songFolderPath(s))
		if err := a.d.Queue.Delete(s.ID); err != nil {
			a.err = "delete " + songName(s) + ": " + err.Error()
			return a, a.loadSongs()
		}
		a.notice = "Deleted " + songName(s) + "."
	} else {
		if now, _ := a.d.Store.SongsIn(c.folder); len(now) != c.count {
			a.notice = "The folder " + quote(c.folder) + " changed since you asked, so nothing was deleted; press d again."
			return a, a.loadSongs()
		}
		a.stopPlayingUnder(a.folderPath(c.folder))
		n, err := a.d.Queue.DeleteFolder(c.folder)
		if err != nil {
			a.err = "delete " + c.folder + ": " + err.Error()
			return a, a.loadSongs()
		}
		a.notice = "Deleted the folder " + c.folder + "."
		if n > 0 {
			a.notice = "Deleted the folder " + c.folder + " and its " + count(n, "song") + "."
		}
	}
	a.library.selectID, a.library.selectFolder = nextSong, nextFolder
	return a, a.loadSongs()
}

// openPicker starts choosing where the selected song or folder goes.
func (a app) openPicker() app {
	p := &folderPicker{choices: []string{newFolderChoice, ""}}
	if s, ok := a.selected(); ok {
		if why := a.cantChange(s, "moved"); why != "" {
			a.notice = why
			return a
		}
		p.songID, p.name, p.current = s.ID, songName(s), s.Folder
		p.choices = append(p.choices, a.library.allFolders()...)
	} else if folder, ok := a.selectedFolder(); ok {
		if why := a.cantChangeFolder(folder, "moved"); why != "" {
			a.notice = why
			return a
		}
		p.folder, p.name, p.current = folder, path.Base(folder), parentFolder(folder)
		for _, f := range a.library.allFolders() {
			if !library.InFolder(f, folder) {
				p.choices = append(p.choices, f)
			}
		}
	} else {
		return a
	}
	p.cursor = max(1, slices.Index(p.choices, p.current))
	a.library.picker = p
	a.layoutDetail()
	return a
}

func (a app) updatePicker(key tea.KeyMsg) (app, tea.Cmd) {
	p := a.library.picker
	switch key.String() {
	case "esc", "q":
		a.library.picker = nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(len(p.choices)-1, p.cursor+1)
	case "enter", " ":
		choice := p.choices[p.cursor]
		a.library.picker = nil
		if choice == newFolderChoice {
			a.layoutDetail()
			return a.startNaming(p.songID, p.folder, "", false)
		}
		if choice == p.current {
			a.notice = p.name + " is already in " + folderLabel(choice) + "."
			break
		}
		a.layoutDetail()
		return a.moveInto(p.songID, p.folder, choice)
	}
	a.layoutDetail()
	return a, nil
}

// moveInto puts a song, or a folder, in dest.
func (a app) moveInto(songID, folder, dest string) (app, tea.Cmd) {
	if songID != "" {
		s, ok := a.songByID(songID)
		if !ok {
			a.err = "That song is no longer in the library."
			return a, a.loadSongs()
		}
		nextSong, nextFolder := a.rowKeyAfter()
		a.stopPlayingUnder(a.songFolderPath(s))
		moved, err := a.d.Queue.Move(s.ID, dest)
		if err != nil {
			a.err = "move " + songName(s) + ": " + err.Error()
			return a, a.loadSongs()
		}
		a.notice = "Moved " + songName(s) + " to " + folderLabel(moved.Folder) + "."
		a.library.selectID, a.library.selectFolder = nextSong, nextFolder
		return a, a.loadSongs()
	}
	to := path.Base(folder)
	if dest != "" {
		to = dest + "/" + to
	}
	return a.renameFolder(folder, to, func(string) string { return "Moved " + folder + " to " + folderLabel(dest) + "." })
}

func (a app) renameFolder(from, to string, notice func(got string) string) (app, tea.Cmd) {
	a.stopPlayingUnder(a.folderPath(from))
	got, err := a.d.Queue.MoveFolder(from, to)
	if err != nil {
		a.err = "move " + from + ": " + err.Error()
		return a, a.loadSongs()
	}
	for f := range a.library.open {
		if library.InFolder(f, from) {
			delete(a.library.open, f)
			a.library.open[got+strings.TrimPrefix(f, from)] = true
		}
	}
	a.notice = notice(got)
	a.library.selectFolder = got
	return a, a.loadSongs()
}

// startNaming asks for a folder name: a new folder for songID or folder
// to go in, or with rename a new name for folder.
func (a app) startNaming(songID, folder, value string, rename bool) (app, tea.Cmd) {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "Folder name, such as Stolen Future or Albums/Demos"
	in.CharLimit = 200
	in.Width = max(10, a.detailPaneWidth()-6)
	in.SetValue(value)
	cmd := in.Focus()
	a.library.naming = &folderNaming{input: in, songID: songID, folder: folder, rename: rename}
	a.layoutDetail()
	return a, cmd
}

func (a app) updateNaming(msg tea.Msg) (app, tea.Cmd) {
	n := a.library.naming
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			a.library.naming = nil
			a.layoutDetail()
			return a, nil
		case "enter":
			return a.finishNaming()
		}
	}
	var cmd tea.Cmd
	n.input, cmd = n.input.Update(msg)
	n.err = ""
	a.layoutDetail()
	return a, cmd
}

func (a app) finishNaming() (app, tea.Cmd) {
	n := a.library.naming
	name, err := library.CleanFolder(n.input.Value())
	if err == nil && name == "" {
		err = fmt.Errorf("type a name, or esc to cancel")
	}
	if err != nil {
		n.err = err.Error()
		a.layoutDetail()
		return a, nil
	}
	a.library.naming = nil
	a.layoutDetail()
	if n.rename {
		to := name
		if p := parentFolder(n.folder); p != "" {
			to = p + "/" + name
		}
		if to == n.folder {
			return a, nil
		}
		return a.renameFolder(n.folder, to, func(got string) string { return "Renamed " + n.folder + " to " + got + "." })
	}
	if n.songID != "" {
		a.library.reveal(parentFolder(name))
		return a.moveInto(n.songID, "", name)
	}
	if _, err := a.d.Store.MakeFolder(name); err != nil {
		a.err = err.Error()
		return a, nil
	}
	return a.moveInto("", n.folder, name)
}

// updateFolderRow handles the keys that act on a selected folder. It
// reports false for keys it leaves to the list.
func (a app) updateFolderRow(key tea.KeyMsg, folder string) (app, tea.Cmd, bool) {
	l := &a.library
	switch key.String() {
	case "enter", " ":
		l.open[folder] = !l.open[folder]
	case "right", "l":
		l.open[folder] = true
	case "left", "h":
		if l.open[folder] {
			l.open[folder] = false
		} else if p := parentFolder(folder); p != "" {
			folder = p
		}
	case "e":
		a, cmd := a.startNaming("", folder, path.Base(folder), true)
		return a, cmd, true
	case "n":
		return a, func() tea.Msg { return openComposeMsg{song: &library.Song{Folder: folder}} }, true
	default:
		return a, nil, false
	}
	a.rebuildRows("", folder)
	a.layoutDetail()
	return a, nil, true
}

// pickerText draws the folder picker in the detail pane.
func (a *app) pickerText(width int) string {
	p := a.library.picker
	wrap := lipgloss.NewStyle().Width(width)
	var b strings.Builder
	b.WriteString(headerStyle.Render("Move "+p.name+" to") + "\n\n")
	for i, c := range p.choices {
		label := folderLabel(c)
		indent := ""
		if c != "" && c != newFolderChoice {
			indent = strings.Repeat("  ", strings.Count(c, "/")+1)
			label = path.Base(c)
		}
		if c == newFolderChoice {
			label = "+ New folder…"
		}
		mark := "  "
		if c == p.current {
			mark = statusStyle.Render("✓ ")
		}
		line := "  " + indent + mark + label
		if i == p.cursor {
			line = selectedStyle.Render("› ") + indent + mark + selectedStyle.Render(label)
		}
		if c == p.current {
			line += dimStyle.Render("  (here now)")
		}
		b.WriteString(wrap.Render(line) + "\n")
	}
	return b.String()
}

// namingText draws the folder name being typed in the detail pane.
func (a *app) namingText(width int) string {
	n := a.library.naming
	wrap := lipgloss.NewStyle().Width(width)
	var b strings.Builder
	switch {
	case n.rename:
		b.WriteString(headerStyle.Render("Rename "+n.folder) + "\n\n")
	case n.songID != "":
		s, _ := a.songByID(n.songID)
		b.WriteString(headerStyle.Render("New folder for "+songName(s)) + "\n\n")
	default:
		b.WriteString(headerStyle.Render("New folder for "+n.folder) + "\n\n")
	}
	b.WriteString(n.input.View() + "\n\n")
	if n.err != "" {
		b.WriteString(wrap.Render(errorStyle.Render(n.err)) + "\n")
	}
	if !n.rename {
		b.WriteString(wrap.Render(dimStyle.Render("A / puts a folder inside another, such as Albums/Demos. The folder is made in the library at "+a.d.Store.Dir()+".")) + "\n")
	}
	return b.String()
}

// folderText describes the selected folder in the detail pane.
func (a *app) folderText(folder string, width int) string {
	wrap := lipgloss.NewStyle().Width(width)
	songs := a.songsUnder(folder)
	var b strings.Builder
	b.WriteString(headerStyle.Render("▸ "+path.Base(folder)) + "\n")
	b.WriteString(wrap.Render(dimStyle.Render(a.folderPath(folder))) + "\n\n")
	if len(songs) == 0 {
		b.WriteString(wrap.Render(dimStyle.Render("Empty. f on a song moves it here; n writes a new song in this folder.")) + "\n")
		return b.String()
	}
	b.WriteString(labelStyle.Render(count(len(songs), "song")) + "\n")
	for _, s := range songs {
		where := ""
		if s.Folder != folder {
			where = dimStyle.Render("  " + strings.TrimPrefix(s.Folder, folder+"/"))
		}
		b.WriteString(wrap.Render(statusBadge(s.Status)+" "+songName(s)+where) + "\n")
	}
	return b.String()
}

// count says how many of a thing there are, such as "3 songs".
func count(n int, one string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, one+"s"))
}

// songName names a song in a message, saying so when it has no title.
func songName(s library.Song) string {
	if strings.TrimSpace(s.Title) == "" {
		return "Untitled"
	}
	return s.Title
}
