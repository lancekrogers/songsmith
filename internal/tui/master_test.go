package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

// masterSong is a finished song with one rendered take on disk.
func masterSong(t *testing.T, store *library.Store, takes ...string) library.Song {
	t.Helper()
	if len(takes) == 0 {
		takes = []string{"song.flac"}
	}
	song, err := store.Create(library.Song{Title: "Glass Harbor", Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption,
		Seconds: 150, Status: library.StatusDone, Audio: takes[len(takes)-1], Duration: 150})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	for i, f := range takes {
		p := store.TakePath(song, f)
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := at.Add(time.Duration(i) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	return song
}

// drive runs cmd and everything it leads to, ticks included, and feeds the
// results back into a, until nothing more comes.
func drive(t *testing.T, a app, cmd tea.Cmd) app {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for range 200 {
		if len(queue) == 0 {
			for {
				msg, ok := a.events.next()
				if !ok {
					break
				}
				var c tea.Cmd
				a, c = send(t, a, msg)
				queue = append(queue, c)
			}
			if len(queue) == 0 {
				return a
			}
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case masterAsync, songsMsg, openMasterMsg, switchScreenMsg, openLibraryMsg:
			var next tea.Cmd
			a, next = send(t, a, msg)
			queue = append(queue, next)
		}
	}
	t.Fatal("drive: commands kept coming")
	return a
}

// openMasterOn opens the library on song and presses m.
func openMasterOn(t *testing.T, a app, w, h int) app {
	t.Helper()
	a, _ = send(t, a, tea.WindowSizeMsg{Width: w, Height: h})
	a = drive(t, a, a.loadSongs())
	a, cmd := send(t, a, key("m"))
	return drive(t, a, cmd)
}

func fakesOf(a app) (*fakePlayer, *fakeMaster) {
	return a.d.Player.(*fakePlayer), a.d.Master.(*fakeMaster)
}

// focusMaster puts the cursor on the row named name (and option).
func focusMaster(t *testing.T, a app, name string, option int) app {
	t.Helper()
	rows, lines := a.masterLayout()
	for i, l := range lines {
		r := rows[l.row]
		if (r.name == name || r.anchor == name) && l.option == option && l.focusable() {
			a.placeMasterCursor(rows, lines, i)
			return a
		}
	}
	t.Fatalf("no row %q option %d:\n%s", name, option, plain(a))
	return a
}

func pressMaster(t *testing.T, a app, keys ...string) app {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		msg := key(k)
		if k == " " {
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		a, cmd = send(t, a, msg)
		a = drive(t, a, cmd)
	}
	return a
}

func masterRow(t *testing.T, a app, name string) settingRow {
	t.Helper()
	for _, r := range a.masterRows() {
		if r.name == name || r.anchor == name {
			return r
		}
	}
	t.Fatalf("no row %q", name)
	return settingRow{}
}

func TestMasterOpensOnlyForFinishedSongs(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	draft, _ := store.Create(library.Song{Title: "Unfinished", Lyrics: sampleDraft.Lyrics})
	a = drive(t, a, a.loadSongs())
	if strings.Contains(footerLine(a), "m master") {
		t.Fatalf("footer offers m on a draft: %q", footerLine(a))
	}
	a = pressMaster(t, a, "m")
	if a.screen != screenLibrary || !strings.Contains(a.notice, "render this one first") {
		t.Fatalf("screen %v notice %q", a.screen, a.notice)
	}
	_ = draft

	song := masterSong(t, store)
	a.library.selectID = song.ID
	a = drive(t, a, a.loadSongs())
	if !strings.Contains(plain(a), "m master") {
		t.Fatalf("footer lacks m master:\n%s", plain(a))
	}
	a = pressMaster(t, a, "m")
	if a.screen != screenMaster || a.master.songID != song.ID || a.master.take != "song.flac" {
		t.Fatalf("screen %v master %+v", a.screen, a.master)
	}
	if !a.master.measured || a.master.before.Loudness != -17.2 {
		t.Fatalf("not measured: %+v", a.master.before)
	}
	a = pressMaster(t, a, "esc")
	if a.screen != screenLibrary {
		t.Fatalf("esc went to %v", a.screen)
	}
}

func TestMasterMeasuringState(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = drive(t, a, a.loadSongs())
	a, _ = send(t, a, key("m"))
	a, _ = send(t, a, openMasterMsg{song: &a.library.songs[0]})
	if !strings.Contains(plain(a), "measuring song.flac…") {
		t.Fatalf("no measuring state:\n%s", plain(a))
	}
}

func TestMasterTakesWorkOnAndMakeCurrent(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	song := masterSong(t, store, "song.flac", "song-2.flac")
	a = openMasterOn(t, a, 120, 40)
	row := masterRow(t, a, anchorTakes)
	if len(row.options) != 2 || row.current != 1 || !strings.Contains(row.extras[1], "working on") {
		t.Fatalf("takes row %+v", row)
	}
	a = focusMaster(t, a, anchorTakes, 0)
	if !strings.Contains(plain(a), "c make it current") || !strings.Contains(plain(a), "enter work on it") {
		t.Fatalf("footer:\n%s", plain(a))
	}
	a = pressMaster(t, a, "enter")
	if a.master.take != "song.flac" {
		t.Fatalf("working on %q", a.master.take)
	}
	if saved, _ := store.Load(song.ID); saved.Audio != "song-2.flac" {
		t.Fatalf("enter changed the current take to %q", saved.Audio)
	}
	a = pressMaster(t, a, "c")
	if saved, _ := store.Load(song.ID); saved.Audio != "song.flac" {
		t.Fatalf("c left the current take at %q", saved.Audio)
	}
	if masterRow(t, a, anchorTakes).current != 0 {
		t.Fatal("✓ did not move")
	}
}

func TestLibraryTCyclesTakes(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	one := masterSong(t, store)
	a = drive(t, a, a.loadSongs())
	if strings.Contains(footerLine(a), "t next take") {
		t.Fatal("t offered with one take")
	}
	a = pressMaster(t, a, "t")
	if saved, _ := store.Load(one.ID); saved.Audio != "song.flac" {
		t.Fatal("t changed a single take")
	}

	two := masterSong(t, store, "song.flac", "song-2.flac")
	a.library.selectID = two.ID
	a = drive(t, a, a.loadSongs())
	if !strings.Contains(plain(a), "t next take") || !strings.Contains(plain(a), "take 2 of 2 · render") {
		t.Fatalf("library:\n%s", plain(a))
	}
	a = pressMaster(t, a, "t")
	if saved, _ := store.Load(two.ID); saved.Audio != "song.flac" {
		t.Fatalf("t: current %q", saved.Audio)
	}
	a = pressMaster(t, a, "t")
	saved, _ := store.Load(two.ID)
	if saved.Audio != "song-2.flac" || saved.Duration != 150 {
		t.Fatalf("t wrapped to %q, length %v", saved.Audio, saved.Duration)
	}
}

func TestMasterPresetsAndCustom(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = openMasterOn(t, a, 120, 40)
	a = focusMaster(t, a, "Preset", -1)
	a = pressMaster(t, a, "+")
	streaming, _ := master.Find("Streaming")
	if a.master.settings != streaming.Settings || !strings.Contains(plain(a), "Streaming: −14 LUFS, −1 dBTP, a 30 Hz low cut") {
		t.Fatalf("settings %+v:\n%s", a.master.settings, plain(a))
	}
	a = focusMaster(t, a, "Tilt", -1)
	a = pressMaster(t, a, "-")
	if a.master.settings.Tilt != -0.5 || a.master.settings.Loudness != -14 {
		t.Fatalf("settings %+v", a.master.settings)
	}
	if r := masterRow(t, a, "Preset"); r.current != -1 || r.value != "Custom" {
		t.Fatalf("preset row %+v", r)
	}
	// A preset replaces the chain but leaves Repair, which none includes.
	a = focusMaster(t, a, "Remove DC and rumble", -1)
	a = pressMaster(t, a, "enter")
	if !a.master.settings.DCRumble || masterRow(t, a, "Preset").current != -1 {
		t.Fatal("DC toggle")
	}
	// From Custom, + starts the list over: Off, Streaming, Loud.
	a = focusMaster(t, a, "Preset", -1)
	a = pressMaster(t, a, "+", "+", "+")
	loud, _ := master.Find("Loud")
	if want := loud.Apply(master.Settings{DCRumble: true}); a.master.settings != want {
		t.Fatalf("Loud should replace all but Repair: %+v", a.master.settings)
	}
	a = focusMaster(t, a, "Loudness target", -1)
	a = pressMaster(t, a, "-")
	if r := masterRow(t, a, "Preset"); r.current != -1 || a.master.settings.Loudness != -10.5 {
		t.Fatalf("preset row %+v, loudness %v", r, a.master.settings.Loudness)
	}
	// From off, the first step lands on a useful value.
	a = focusMaster(t, a, "Fade out", -1)
	a = pressMaster(t, a, "+")
	if a.master.settings.FadeOut != 4 {
		t.Fatalf("fade out %v", a.master.settings.FadeOut)
	}
}

func TestMasterRepairIsOffAndModest(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = openMasterOn(t, a, 120, 40)
	s := a.master.settings
	if s.NoiseBed != 0 || s.DCRumble || s.NotchTones {
		t.Fatalf("repair on by default: %+v", s)
	}
	for _, p := range master.Presets {
		if p.Settings.NoiseBed != 0 || p.Settings.DCRumble || p.Settings.NotchTones {
			t.Fatalf("%s includes repair", p.Name)
		}
	}
	a = focusMaster(t, a, "Remove noise bed", -1)
	if !strings.Contains(plain(a), "It also dulls the song, and it cannot fix a render that came out noisy.") {
		t.Fatalf("description:\n%s", plain(a))
	}
	a = pressMaster(t, a, "+")
	if a.master.settings.NoiseBed != 8 {
		t.Fatalf("first step = %v", a.master.settings.NoiseBed)
	}
}

func TestMasterTypedValuesAndTheEditorSwallowsKeys(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = openMasterOn(t, a, 120, 40)
	player, _ := fakesOf(a)
	a = focusMaster(t, a, "Loudness target", -1)
	a = pressMaster(t, a, "enter")
	if !a.master.edit.active || !strings.Contains(plain(a), "enter save") {
		t.Fatalf("editor not open:\n%s", plain(a))
	}
	a = pressMaster(t, a, "o", " ", "c", "[", "]")
	if len(player.plays()) != 0 || a.master.position != 30 {
		t.Fatalf("keys reached the screen: plays %v position %v", player.plays(), a.master.position)
	}
	if got := a.master.edit.input.Value(); got != "o c[]" {
		t.Fatalf("typed %q", got)
	}
	a.master.edit.input.SetValue("loud")
	a = pressMaster(t, a, "enter")
	if !a.master.edit.active || !strings.Contains(a.master.edit.note, "Type a number") {
		t.Fatalf("bad value accepted: %+v", a.master.edit)
	}
	a.master.edit.input.SetValue("−16.5 LUFS")
	a = pressMaster(t, a, "enter")
	if a.master.edit.active || a.master.settings.Loudness != -16.5 {
		t.Fatalf("loudness %v", a.master.settings.Loudness)
	}
	// Past the slider's range is fine; only what ffmpeg cannot do is refused.
	a = focusMaster(t, a, "Low cut", -1)
	a = pressMaster(t, a, "enter")
	a.master.edit.input.SetValue("400")
	a = pressMaster(t, a, "enter")
	if a.master.settings.LowCut != 400 {
		t.Fatalf("low cut %v", a.master.settings.LowCut)
	}
	a = focusMaster(t, a, "Peak ceiling", -1)
	a = pressMaster(t, a, "enter")
	a.master.edit.input.SetValue("2")
	a = pressMaster(t, a, "enter")
	if !a.master.edit.active || a.master.settings.Ceiling != 0 {
		t.Fatalf("ceiling above 0 accepted: %v", a.master.settings.Ceiling)
	}
	a = pressMaster(t, a, "esc")
	if a.master.edit.active || a.screen != screenMaster {
		t.Fatal("esc should close only the editor")
	}
}

func TestMasterPreviewDebounceCancelAndStaleResults(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = drive(t, a, a.loadSongs())
	a, open := send(t, a, openMasterMsg{song: &a.library.songs[0]})
	if !a.master.preview.preparing || a.master.preview.seq != 1 || !strings.Contains(plain(a), "preparing preview…") {
		t.Fatalf("preview %+v", a.master.preview)
	}
	_ = open
	// A change before the first preview was due replaces it.
	a = focusMaster(t, a, "Tilt", -1)
	a, _ = send(t, a, key("+"))
	if a.master.preview.seq != 2 {
		t.Fatalf("seq %d", a.master.preview.seq)
	}
	_, fm := fakesOf(a)
	a, cmd := send(t, a, previewDueMsg{seq: 1})
	if cmd != nil || len(fm.calls()) != 0 {
		t.Fatal("a superseded preview started")
	}
	// A late result of an older preview is dropped and its files removed.
	stale := filepath.Join(t.TempDir(), "preview-1.flac")
	os.WriteFile(stale, []byte("x"), 0o644)
	a, _ = send(t, a, previewDoneMsg{seq: 1, file: stale, orig: stale})
	if a.master.preview.file != "" || !a.master.preview.preparing {
		t.Fatalf("stale result used: %+v", a.master.preview)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale file kept")
	}
	// The current one runs both excerpts at the same place.
	a, cmd = send(t, a, previewDueMsg{seq: 2})
	a = drive(t, a, cmd)
	calls := fm.calls()
	if len(calls) != 2 || calls[0].settings.Tilt != 0.5 || calls[1].settings != (master.Settings{}) || calls[0].start != calls[1].start {
		t.Fatalf("calls %+v", calls)
	}
	if a.master.preview.preparing || a.master.preview.file == "" || a.master.afterScope != scopeExcerpt {
		t.Fatalf("preview %+v", a.master.preview)
	}
	// Cancelling a preview being prepared stops its work.
	cancelled := false
	a.master.preview.cancel = func() { cancelled = true }
	a = pressMaster(t, a, "]")
	if !cancelled || a.master.position != 45 {
		t.Fatalf("cancelled %v position %v", cancelled, a.master.position)
	}
}

func TestMasterSpaceAndOPlayOneAtATimeAndStopOnLeave(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = openMasterOn(t, a, 120, 40)
	player, _ := fakesOf(a)
	file, orig := a.master.preview.file, a.master.preview.orig
	if file == "" || orig == "" {
		t.Fatalf("no excerpts: %+v", a.master.preview)
	}
	a = pressMaster(t, a, " ")
	if player.Current() != file || !strings.Contains(plain(a), "▶ playing the master") {
		t.Fatalf("space: %q", player.Current())
	}
	a = pressMaster(t, a, "o")
	if player.Current() != orig {
		t.Fatalf("o: %q", player.Current())
	}
	a = pressMaster(t, a, "o")
	if player.Current() != "" {
		t.Fatal("o did not stop the original")
	}
	a = pressMaster(t, a, " ")
	// A change while the master plays plays the new excerpt when it is ready.
	a = focusMaster(t, a, "Tilt", -1)
	a = pressMaster(t, a, "+")
	if player.Current() == file || player.Current() != a.master.preview.file || kindOf(player.Current()) != excerptMaster {
		t.Fatalf("after a change: playing %q, preview %q", player.Current(), a.master.preview.file)
	}
	dir := a.master.scratch.dir
	a = pressMaster(t, a, "esc")
	if player.Current() != "" {
		t.Fatal("leaving did not stop playback")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("scratch left: %v", entries)
	}
}

func TestMasterLeavingKeepsOtherFilesInTheScratchFolder(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	song := masterSong(t, store)
	dir := filepath.Join(a.cfg().StateDir, "master-preview", song.ID)
	os.MkdirAll(dir, 0o755)
	keep := filepath.Join(dir, "keep.txt")
	os.WriteFile(keep, []byte("x"), 0o644)
	a = openMasterOn(t, a, 120, 40)
	a = pressMaster(t, a, "ctrl+c")
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a file this screen did not make was removed")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.flac")); len(left) != 0 {
		t.Fatalf("excerpts left after quitting: %v", left)
	}
}

func TestMasterJobProgressCancelAndCompletion(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	song := masterSong(t, store)
	a = openMasterOn(t, a, 120, 40)
	_, fm := fakesOf(a)
	fm.block = make(chan struct{})
	a = focusMaster(t, a, "Master this take", -1)
	a, cmd := send(t, a, key("enter"))
	a = drive(t, a, cmd)
	a = waitFor(t, a, func(a app) bool { j, ok := a.mastering.running[song.ID]; return ok && j.done == 0.4 })
	if !strings.Contains(plain(a), "mastering song.flac · 40%") || !strings.Contains(plain(a), "Cancel") {
		t.Fatalf("no progress:\n%s", plain(a))
	}
	if strings.Contains(plain(a), "ctrl+s master") {
		t.Fatal("ctrl+s offered while mastering")
	}
	// It keeps going in the library, which shows it.
	a = pressMaster(t, a, "esc")
	if !strings.Contains(plain(a), "mastering 40%") {
		t.Fatalf("library:\n%s", plain(a))
	}
	a = pressMaster(t, a, "m")
	a = focusMaster(t, a, "cancel master", -1)
	a = pressMaster(t, a, "enter")
	a = waitFor(t, a, func(a app) bool { return len(a.mastering.running) == 0 })
	if len(store.Takes(mustLoad(t, store, song.ID))) != 1 {
		t.Fatal("a cancelled master was saved")
	}

	fm.block = nil
	a = pressMaster(t, a, "ctrl+s")
	a = waitFor(t, a, func(a app) bool { return len(a.mastering.running) == 0 })
	a = drive(t, a, a.loadSongs())
	saved := mustLoad(t, store, song.ID)
	takes := store.Takes(saved)
	if len(takes) != 2 || takes[1].File != "song.master-1.flac" || saved.Audio != "song.flac" {
		t.Fatalf("takes %+v, current %q", takes, saved.Audio)
	}
	if a.master.made != "song.master-1.flac" || a.master.afterScope != scopeWhole {
		t.Fatalf("made %q scope %v", a.master.made, a.master.afterScope)
	}
	a = focusMaster(t, a, "Make it the current take", -1)
	a = pressMaster(t, a, "enter")
	if mustLoad(t, store, song.ID).Audio != "song.master-1.flac" {
		t.Fatal("not made current")
	}
	a = pressMaster(t, a, "esc")
	if !strings.Contains(plain(a), "take 2 of 2 · mastered") {
		t.Fatalf("library:\n%s", plain(a))
	}
}

func mustLoad(t *testing.T, store *library.Store, id string) library.Song {
	t.Helper()
	s, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// waitFor feeds bridge messages into a until ok holds.
func waitFor(t *testing.T, a app, ok func(app) bool) app {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok(a) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out:\n%s", plain(a))
		}
		msg, got := a.events.next()
		if !got {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		var cmd tea.Cmd
		a, cmd = send(t, a, msg)
		a = drive(t, a, cmd)
	}
	return a
}

func TestMasterWithoutFFmpegExplains(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	_, fm := fakesOf(a)
	fm.unavailable = errors.New("ffmpeg is not installed: ffmpeg not found")
	a = openMasterOn(t, a, 80, 24)
	view := plain(a)
	for _, want := range []string{"Mastering needs ffmpeg", "brew install ffmpeg", "apt install ffmpeg", "Takes"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "space play") || strings.Contains(view, "ctrl+s") {
		t.Fatalf("offers keys that cannot work:\n%s", view)
	}
	player, _ := fakesOf(a)
	a = pressMaster(t, a, " ", "ctrl+s")
	if len(player.plays()) != 0 || len(a.mastering.running) != 0 || len(fm.calls()) != 0 {
		t.Fatal("something ran without ffmpeg")
	}
}

func TestMasterFitsEightyByTwentyFour(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store, "song.flac", "song-2.flac", "song-2.master-1.flac")
	a = openMasterOn(t, a, 80, 24)
	if a.master.take != "song-2.flac" {
		t.Fatalf("a master should be reworked from its source, not %q", a.master.take)
	}
	// The longest names: work on the master itself.
	a = focusMaster(t, a, anchorTakes, 2)
	a = pressMaster(t, a, "enter")
	for _, name := range []string{anchorTakes, "Preview from", "Preset", "Remove DC and rumble", "Notch render tones", "Remove noise bed", "Tilt", "Fade out", "Master this take", "save default", "Master new renders automatically"} {
		a = focusMaster(t, a, name, -1+boolInt(name == anchorTakes))
		lines := strings.Split(a.View(), "\n")
		if len(lines) != 24 {
			t.Fatalf("%s: %d lines:\n%s", name, len(lines), plain(a))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > 80 {
				t.Fatalf("%s: line %d wide: %q", name, w, ansi.Strip(l))
			}
			if strings.Contains(l, "…") && !strings.Contains(l, "preparing preview…") && !strings.Contains(l, "measuring") {
				t.Fatalf("%s: a line is cut short: %q\n%s", name, ansi.Strip(l), plain(a))
			}
		}
		if !strings.Contains(lines[len(lines)-1], "esc back") {
			t.Fatalf("%s: footer not last:\n%s", name, plain(a))
		}
		if !strings.Contains(plain(a), "▸") {
			t.Fatalf("%s: cursor off screen:\n%s", name, plain(a))
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestMasterDefaultChainFromSettings(t *testing.T) {
	a, _ := newTestModel(t, fakeDrafter{})
	a, cmd := send(t, a, switchScreenMsg(screenSettings))
	a = drive(t, a, cmd)
	a = pressMaster(t, a, "5")
	if a.settings.section != sectionMaster || !strings.Contains(plain(a), "Edit the default") {
		t.Fatalf("settings:\n%s", plain(a))
	}
	for range 4 {
		a = pressMaster(t, a, "down")
	}
	a = pressMaster(t, a, "enter")
	if a.screen != screenMaster || !a.master.defaults {
		t.Fatalf("screen %v", a.screen)
	}
	a = focusMaster(t, a, "Preset", -1)
	a = pressMaster(t, a, "enter")
	if a.cfg().Master.Default.Loudness != -14 {
		t.Fatalf("default not saved: %+v", a.cfg().Master.Default)
	}
	if strings.Contains(plain(a), "space") || strings.Contains(plain(a), "Takes") {
		t.Fatalf("default chain shows song keys:\n%s", plain(a))
	}
	a = pressMaster(t, a, "esc")
	if a.screen != screenSettings || !strings.Contains(plain(a), "Streaming") {
		t.Fatalf("back in settings:\n%s", plain(a))
	}
}

func TestMasterSaveAsDefaultAndAuto(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	masterSong(t, store)
	a = openMasterOn(t, a, 120, 40)
	a = focusMaster(t, a, "Preset", -1)
	a = pressMaster(t, a, "enter", "enter", "enter")
	a = focusMaster(t, a, "save default", -1)
	a = pressMaster(t, a, "enter")
	if a.cfg().Master.Default.Tilt != -1.5 || masterRow(t, a, "save default").detail != "this is your default" {
		t.Fatalf("default %+v", a.cfg().Master.Default)
	}
	a = focusMaster(t, a, "Master new renders automatically", -1)
	a = pressMaster(t, a, "enter")
	if !a.cfg().Master.Auto {
		t.Fatal("auto not saved")
	}
}
