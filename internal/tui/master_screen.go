package tui

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

// The Master screen post-processes one take of a finished song. The take is
// judged by ear: space plays a short excerpt of the master, o the same
// excerpt of the original, and every change prepares a new excerpt. Opened
// from Settings without a song, the same rows edit the saved default chain.

type excerptKind int

const (
	noExcerpt excerptKind = iota
	excerptMaster
	excerptOriginal
)

// kindOf tells a master excerpt from an original one by its file name.
func kindOf(path string) excerptKind {
	if strings.HasPrefix(filepath.Base(path), "original-") {
		return excerptOriginal
	}
	return excerptMaster
}

type meterScope int

const (
	scopeNone meterScope = iota
	// scopeExcerpt compares the original excerpt with the master excerpt.
	scopeExcerpt
	// scopeWhole compares the whole take with the whole master.
	scopeWhole
)

type previewState struct {
	// seq is the newest preview asked for; cancel stops the one running.
	seq       int
	cancel    func()
	preparing bool
	// file and orig are the ready excerpts, "" until then.
	file, orig string
	err        string
	// pending is what space or o asked to play while preparing.
	pending excerptKind
}

type masterModel struct {
	rowCursor
	mark lineAnchor
	edit settingsEditor
	// message is the latest outcome; failed draws it as an error.
	message string
	failed  bool

	// defaults is set when the rows edit the saved default chain, opened
	// from Settings; returnTo is the screen esc goes back to.
	defaults bool
	returnTo screen

	songID string
	// take is the file being worked on; made is a master made here, offered
	// as the current take.
	take, made string
	settings   master.Settings
	// position is where previews start, in seconds of the output.
	position float64

	measureSeq          int
	measuring, measured bool
	measureErr          string
	before              master.Measure
	// after is the latest master excerpt or whole master, and afterOrig
	// the original excerpt it is compared with.
	after, afterOrig master.Measure
	afterScope       meterScope
	afterAt          float64

	preview     previewState
	scratch     *scratch
	unavailable error
}

// openMasterMsg opens the Master screen on a song, or on the default chain
// when song is nil.
type openMasterMsg struct{ song *library.Song }

// canMaster reports whether song has a take to master.
func (a app) canMaster(song library.Song) bool {
	return song.Status == library.StatusDone && a.d.Store.HasAudio(song)
}

func (a app) openMaster(msg openMasterMsg) (app, tea.Cmd) {
	cfg := a.cfg()
	m := masterModel{returnTo: a.screen, settings: cfg.Master.Default, unavailable: a.d.Master.Check()}
	if msg.song == nil {
		m.defaults = true
		a.master, a.screen = m, screenMaster
		a.syncMaster()
		return a, nil
	}
	song := *msg.song
	if !a.canMaster(song) {
		a.notice = "Only a finished song with audio can be mastered."
		return a, nil
	}
	m.songID, m.take = song.ID, song.Audio
	// A master is reworked from its source with the chain it was made with,
	// so trying again never masters a master.
	takes := a.library.takes[song.ID]
	if i := library.TakeIndex(takes, song.Audio); i > 0 {
		if t := takes[i-1]; t.Kind == library.TakeMaster && library.TakeIndex(takes, t.Source) > 0 {
			m.take = t.Source
			if t.Master != nil {
				m.settings = *t.Master
			}
		}
	}
	if song.Duration > 60 {
		m.position = 30
	}
	a.err, a.notice = "", ""
	a.master, a.screen = m, screenMaster
	a.syncMaster()
	if m.unavailable != nil {
		return a, nil
	}
	sc, err := newScratch(filepath.Join(cfg.StateDir, "master-preview", song.ID))
	if err != nil {
		a.masterError(err.Error())
		return a, nil
	}
	a.master.scratch = sc
	return a, tea.Batch(a.startMeasure(), a.schedulePreview())
}

// leaveMaster stops what the screen started: playback of its excerpts and
// the preview being prepared. A master being rendered keeps going.
func (a *app) leaveMaster() {
	m := &a.master
	if m.scratch.owns(a.d.Player.Current()) {
		a.d.Player.Stop()
	}
	if m.preview.cancel != nil {
		m.preview.cancel()
	}
	m.preview = previewState{seq: m.preview.seq + 1}
	m.scratch.clean()
	m.scratch = nil
}

func (a app) masterSong() (library.Song, bool) { return a.songByID(a.master.songID) }

func (a app) masterTakePath() string {
	song, _ := a.masterSong()
	song.ID = a.master.songID
	return a.d.Store.TakePath(song, a.master.take)
}

func (a app) currentTakeName() string {
	if song, ok := a.masterSong(); ok {
		return song.Audio
	}
	return ""
}

func (a app) masterTakes() []library.Take {
	return a.library.takes[a.master.songID]
}

func (a *app) masterNote(msg string)  { a.master.message, a.master.failed = msg, false }
func (a *app) masterError(msg string) { a.master.message, a.master.failed = msg, true }

// setMaster changes the chain being worked on and prepares a preview of it,
// or saves it at once when the rows edit the default chain.
func (a *app) setMaster(s master.Settings, what string) tea.Cmd {
	if err := s.Check(); err != nil {
		a.masterError(capitalize(err.Error()))
		return nil
	}
	a.master.settings = s
	if a.master.defaults {
		if err := a.saveConfig("master.default", s); err != nil {
			a.masterError("Not saved: " + err.Error())
			return nil
		}
		a.masterNote("Saved as the default · " + what)
		return nil
	}
	a.masterNote(what)
	return a.schedulePreview()
}

// moveTo moves the preview position, keeping it inside the master.
func (a *app) moveTo(pos float64) tea.Cmd {
	m := &a.master
	length := m.settings.Length(m.before.Info)
	if !m.measured {
		if song, ok := a.masterSong(); ok {
			length = song.Duration
		}
	}
	pos = min(max(pos, 0), max(length-1, 0))
	if pos == m.position {
		a.masterNote("The preview is already at " + fmtDuration(pos))
		return nil
	}
	m.position = pos
	a.masterNote("Preview from " + fmtDuration(pos))
	return a.schedulePreview()
}

// playExcerpt plays the master or the original excerpt at the preview
// position. One thing plays at a time.
func (a *app) playExcerpt(kind excerptKind) {
	m := &a.master
	file := m.preview.file
	name := "the master"
	if kind == excerptOriginal {
		file, name = m.preview.orig, "the original"
	}
	if err := a.d.Player.Play(file); err != nil {
		a.masterError("Can't play: " + err.Error())
		return
	}
	a.masterNote("Playing " + name + " from " + fmtDuration(m.position) + "; " + map[excerptKind]string{
		excerptMaster: "o switches to the original, space stops", excerptOriginal: "space switches to the master, o stops"}[kind])
}

// listen is space (the master) and o (the original): play the excerpt, or
// stop it when it is the one playing.
func (a *app) listen(kind excerptKind) {
	m := &a.master
	if m.unavailable != nil || m.defaults {
		return
	}
	cur := a.d.Player.Current()
	if m.scratch.owns(cur) && kindOf(cur) == kind {
		a.d.Player.Stop()
		m.preview.pending = noExcerpt
		a.masterNote("Stopped.")
		return
	}
	if m.preview.preparing {
		if m.scratch.owns(cur) {
			a.d.Player.Stop()
		}
		m.preview.pending = kind
		a.masterNote("Preparing the excerpt; it plays as soon as it is ready.")
		return
	}
	if m.preview.file == "" {
		a.masterNote("No excerpt yet: " + m.preview.err)
		return
	}
	a.playExcerpt(kind)
}

// masterPlaying is what plays from this screen.
func (a app) masterPlaying() excerptKind {
	if cur := a.d.Player.Current(); a.master.scratch.owns(cur) {
		return kindOf(cur)
	}
	return noExcerpt
}

// One key, one verb: ↑/↓ move, enter acts on the row, +/- adjust, ←/→ move
// the preview on its row, [ and ] jump it 15 s, space plays the master
// excerpt, o the original, c makes a take current, ctrl+s masters, esc leaves.
func (a app) updateMaster(msg tea.Msg) (app, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	if a.master.edit.active {
		return a.updateMasterEditor(key)
	}
	a.syncMaster()
	m := &a.master
	row, line, focused := a.focusedMaster()
	var cmd tea.Cmd
	switch k := key.String(); k {
	case "esc":
		back := m.returnTo
		a.leaveMaster()
		if back == screenSettings {
			return a.openSettings(sectionMaster, false)
		}
		a.library.selectID = m.songID
		a.selectPending()
		return a.show(screenLibrary)
	case "up", "k":
		a.clearMasterNote()
		a.moveMasterCursor(-1)
	case "down", "j":
		a.clearMasterNote()
		a.moveMasterCursor(1)
	case "pgup", "pgdown":
		a.clearMasterNote()
		n := a.masterFrame().height
		if k == "pgup" {
			n = -n
		}
		a.moveMasterCursor(n)
	case "home", "g", "end", "G":
		a.clearMasterNote()
		rows, lines := a.masterLayout()
		at := 0
		if k == "end" || k == "G" {
			at = len(lines) - 1
		}
		a.placeMasterCursor(rows, lines, at)
	case "enter":
		if focused {
			cmd = a.activateSetting(row, line)
		}
	case "+", "=", "-", "_":
		if focused {
			d := 1
			if k == "-" || k == "_" {
				d = -1
			}
			cmd = a.adjustSetting(row, line, d)
		}
	case "left", "right", "h", "l":
		if focused && row.anchor == anchorPosition {
			d := 1
			if k == "left" || k == "h" {
				d = -1
			}
			cmd = a.moveTo(m.position + float64(d)*positionStep)
		}
	case "[", "]":
		if !m.defaults && m.unavailable == nil {
			d := 1.0
			if k == "[" {
				d = -1
			}
			cmd = a.moveTo(m.position + d*positionJump)
		}
	case " ":
		a.listen(excerptMaster)
	case "o":
		a.listen(excerptOriginal)
	case "c":
		if focused && row.anchor == anchorTakes && line.option >= 0 {
			cmd = a.makeCurrent(a.masterTakes()[line.option].File)
		}
	case "ctrl+s":
		if a.canStartMastering() {
			cmd = a.startMastering()
		}
	}
	a.syncMaster()
	return a, cmd
}

const (
	positionStep = 5.0
	positionJump = 15.0
)

func (a app) canStartMastering() bool {
	m := a.master
	_, running := a.mastering.running[m.songID]
	return !m.defaults && m.unavailable == nil && !running
}

// makeCurrent makes file the take the library plays.
func (a *app) makeCurrent(file string) tea.Cmd {
	song, err := a.d.Store.SetCurrent(a.master.songID, file)
	if err != nil {
		a.masterError("Not changed: " + err.Error())
		return nil
	}
	if a.master.made == file {
		a.master.made = ""
	}
	a.masterNote(file + " is now the current take; the library plays it.")
	a.replaceSong(song)
	return a.loadSongs()
}

// replaceSong puts a saved song into the list at once, so the screen does
// not wait for the reload to show it.
func (a *app) replaceSong(song library.Song) {
	for i, s := range a.library.songs {
		if s.ID == song.ID {
			a.library.songs[i] = song
		}
	}
}

func (a *app) clearMasterNote() { a.master.message, a.master.failed = "", false }

// updateMasterEditor owns every key while a value is typed, so space, o,
// c, [ and ] type rather than play or move.
func (a app) updateMasterEditor(key tea.KeyMsg) (app, tea.Cmd) {
	e := &a.master.edit
	switch key.String() {
	case "esc":
		a.master.edit = settingsEditor{}
		a.masterNote("Cancelled; nothing changed")
		return a, nil
	case "enter":
		cmd, err := e.commit(&a, strings.TrimSpace(e.input.Value()))
		if err != nil {
			e.note, e.noteFailed = err.Error(), true
			return a, nil
		}
		a.master.edit = settingsEditor{}
		a.syncMaster()
		return a, cmd
	}
	var cmd tea.Cmd
	before := e.input.Value()
	e.input, cmd = e.input.Update(key)
	if e.input.Value() != before {
		e.note, e.noteFailed = "", false
	}
	return a, cmd
}

// beginMasterEdit opens the one-line editor above the rows.
func (a *app) beginMasterEdit(label, initial string, commit func(a *app, value string) (tea.Cmd, error)) tea.Cmd {
	a.master.edit = newEditor(label, initial, a.masterWidth(), commit)
	a.clearMasterNote()
	return nil
}

// Cursor and layout, through the same row engine as Settings.

func (a app) masterLayout() ([]settingRow, []settingLine) {
	rows := a.masterRows()
	return rows, linesFor(rows, valueWidths(rows, a.masterWidth()), settingsFilter{})
}

func (a app) focusedMaster() (settingRow, settingLine, bool) {
	rows, lines := a.masterLayout()
	c := a.master.cursor
	if c < 0 || c >= len(lines) || !lines[c].focusable() {
		return settingRow{}, settingLine{}, false
	}
	return rows[lines[c].row], lines[c], true
}

func (a *app) placeMasterCursor(rows []settingRow, lines []settingLine, i int) {
	a.master.rowCursor.place(rows, lines, i, a.masterFrame())
	a.master.mark = anchorAt(rows, lines, a.master.cursor)
}

func (a *app) moveMasterCursor(delta int) {
	rows, lines := a.masterLayout()
	a.placeMasterCursor(rows, lines, a.master.rowCursor.step(rows, lines, delta))
}

// syncMaster keeps the cursor on its line as rows come and go.
func (a *app) syncMaster() {
	rows, lines := a.masterLayout()
	i, ok := findAnchor(rows, lines, a.master.mark)
	if !ok {
		i = homeLine(rows, lines)
		if a.master.mark.set {
			i = nearestFocusable(lines, a.master.cursor)
		}
	}
	a.placeMasterCursor(rows, lines, i)
}
