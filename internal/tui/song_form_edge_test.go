package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/tui/vim"
)

func pasteKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return slices.ContainsFunc(batch, quits)
	}
	_, ok := msg.(tea.QuitMsg)
	return ok
}

func TestPasteKeepsLineBreaksFromEveryPlatform(t *testing.T) {
	ed := newTextEditor("Lyrics", "", true)
	ed.update(pasteKey("[Verse]\r\nline one\rline two\nline three"))
	want := "[Verse]\nline one\nline two\nline three"
	if got := ed.text(); got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
	if strings.ContainsRune(ed.text(), '\r') {
		t.Fatal("a carriage return reached the buffer")
	}
}

func TestPasteInNormalModeInsertsTheText(t *testing.T) {
	ed := newTextEditor("Lyrics", "keep me", true)
	ed.update(key("esc"))
	if ed.ed.Mode() != vim.ModeNormal {
		t.Fatalf("mode %v", ed.ed.Mode())
	}
	// Typed, "dd" would delete the line; pasted, it is text.
	ed.update(pasteKey("dd\r\nx"))
	if got := ed.text(); got != "keep mdd\nxe" {
		t.Fatalf("text %q", got)
	}
}

func TestPasteIntoAOneLineFieldDropsLineBreaks(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "down", "down")
	m, _ = send(t, m, pasteKey("Sock\r\nThief\r"))
	if got := m.compose.title.Value(); got != "Sock Thief " {
		t.Fatalf("title %q", got)
	}
}

func TestDraftAsksBeforeReplacingATitleOrStyle(t *testing.T) {
	for _, c := range []struct {
		name string
		fill func(m app) app
		want string
	}{
		{"title", func(m app) app { m.compose.title.SetValue("My Title"); return m }, "replaces the title below"},
		{"style", func(m app) app { m.compose.texts[3] = "Verse: piano."; return m }, "replaces the arrangement below"},
		{"all", func(m app) app { return fillValid(m) }, "replaces the title, lyrics, global metadata, vocal details and arrangement below"},
	} {
		d := newRecordingDrafter(sampleDraft, nil)
		m, _ := openNewSong(t, d)
		m = c.fill(m)
		m = press(t, m, "an idea")
		m, cmd := send(t, m, key("enter"))
		m = runCmd(t, m, cmd)
		if *d.calls != 0 || m.compose.armed != "draft" || !strings.Contains(m.compose.msg, c.want) {
			t.Fatalf("%s: calls %d armed %q msg %q", c.name, *d.calls, m.compose.armed, m.compose.msg)
		}
		m, cmd = send(t, m, key("enter"))
		runCmd(t, m, cmd)
		if *d.calls != 1 {
			t.Fatalf("%s: a second enter should draft", c.name)
		}
	}
}

func TestLongIdeaAndTitleSurviveOpeningAndSaving(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	idea := strings.TrimSpace(strings.Repeat("an idea that keeps going ", 40))
	title := strings.TrimSpace(strings.Repeat("Title ", 50))
	song, err := store.Create(library.Song{Idea: idea, Title: title, Seconds: 60, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	m = press(t, m, "up", "up", "up", "up", "x")
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	got, _ := store.Load(song.ID)
	if got.Idea != idea || got.Title != title+"x" {
		t.Fatalf("idea %d chars, title %q", len(got.Idea), got.Title)
	}
}

func TestLibraryRefusesToQueueAnIncompleteSong(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	song, err := store.Create(library.Song{Title: "Half", Lyrics: "[Verse]\nla", Seconds: 60, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m, cmd := send(t, m, key("r"))
	m = runCmd(t, m, cmd)
	got, _ := store.Load(song.ID)
	if got.Status != library.StatusDraft {
		t.Fatalf("an incomplete song was queued: %s", got.Status)
	}
	for _, want := range []string{"Can't render yet: no style. e edits it."} {
		if !strings.Contains(m.notice, want) {
			t.Fatalf("notice %q lacks %q", m.notice, want)
		}
	}
}

func TestCtrlCInTheEditorActsLikeEsc(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "down", "down", "down", "down", "down", "down", "enter")
	if m.compose.editor == nil || m.compose.editor.ed.Mode() != vim.ModeInsert {
		t.Fatal("editor not open in INSERT")
	}
	m = press(t, m, "words")
	m, cmd := send(t, m, key("ctrl+c"))
	if quits(cmd) || m.compose.editor == nil || m.compose.editor.ed.Mode() != vim.ModeNormal {
		t.Fatalf("ctrl+c in INSERT should go to NORMAL: quit %v", quits(cmd))
	}
	m, cmd = send(t, m, key("ctrl+c"))
	if quits(cmd) || m.compose.editor == nil || m.compose.msg != idleEscHint {
		t.Fatalf("ctrl+c in idle NORMAL should hint: msg %q", m.compose.msg)
	}
	if m.compose.editor.text() != "words" {
		t.Fatalf("text %q", m.compose.editor.text())
	}
}

func TestCtrlCOnAChangedFormAsksBeforeQuitting(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m, cmd := send(t, m, key("ctrl+c"))
	if !quits(cmd) {
		t.Fatal("ctrl+c on an unchanged form should quit")
	}

	m, _ = openNewSong(t, fakeDrafter{})
	m = press(t, m, "idea")
	m, cmd = send(t, m, key("ctrl+c"))
	if quits(cmd) || m.compose.msg != unsavedQuit {
		t.Fatalf("first ctrl+c: quit %v msg %q", quits(cmd), m.compose.msg)
	}
	m, cmd = send(t, m, key("down"), key("ctrl+c"))
	if quits(cmd) {
		t.Fatal("a key between the two ctrl+c presses should disarm it")
	}
	_, cmd = send(t, m, key("ctrl+c"))
	if !quits(cmd) {
		t.Fatal("a second ctrl+c in a row should quit")
	}
}

func TestEditsSavedAsACopyKeepTheTypedSeed(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	song, err := store.Create(library.Song{Title: "Busy", Lyrics: "[Verse]\nla", Seconds: 60, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	m.compose.seed.SetValue("99")
	// It starts rendering elsewhere while the form is open.
	song.Status = library.StatusRendering
	if err := store.Save(song); err != nil {
		t.Fatal(err)
	}
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	songs, _ := store.List()
	var copied []library.Song
	for _, s := range songs {
		if s.ID != song.ID {
			copied = append(copied, s)
		}
	}
	if len(copied) != 1 || copied[0].Seed != 99 || !copied[0].SeedPinned {
		t.Fatalf("copies %+v", copied)
	}
}

func TestTagCompletionReplacesTheWholeTag(t *testing.T) {
	for _, c := range []struct {
		text string
		col  int
		want string
	}{
		{"[Verse 2]", 4, "[Verse]"},
		{"[Ver", 4, "[Verse]"},
		{"[Verse", 4, "[Verse]"},
		{"[Vers tail", 3, "[Verse] tail"},
		{"[Ve] after", 2, "[Verse] after"},
	} {
		ed := newTextEditor("Lyrics", c.text, true)
		ed.ed.SetCursorInsert(vim.Position{Line: 0, Col: c.col})
		ed.update(key("tab"))
		if len(ed.menu) == 0 || ed.menu[0] != "Verse" {
			t.Fatalf("%q: menu %v", c.text, ed.menu)
		}
		ed.update(key("enter"))
		if got := ed.text(); got != c.want {
			t.Errorf("%q at %d: %q, want %q", c.text, c.col, got, c.want)
		}
	}

	ed := newTextEditor("Lyrics", "[Verse] words", true)
	ed.update(key("tab"))
	if ed.menu != nil {
		t.Fatalf("no menu outside a tag, got %v", ed.menu)
	}
}

// forceColour turns on colour output for one test.
func forceColour(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestTagMenuSitsUnderTheCursorInColour(t *testing.T) {
	forceColour(t)
	for _, c := range []struct {
		text string
		line int
	}{
		{"[Verse]\nfirst line\n[", 2},
		{"[\nsecond line\nthird line", 0},
	} {
		ed := newTextEditor("Lyrics", c.text, true)
		ed.setSize(40, 8)
		ed.ed.SetCursorInsert(vim.Position{Line: c.line, Col: 1})
		ed.update(key("tab"))
		if ed.menu == nil {
			t.Fatalf("%q: no menu", c.text)
		}
		lines := ed.bodyLines(3)
		if !strings.Contains(ansi.Strip(lines[0]), "1") || !strings.Contains(lines[0], "\x1b[") {
			t.Fatalf("not drawn in colour: %q", lines[0])
		}
		at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(ansi.Strip(l), "▸ [Intro]") })
		if at != c.line+1 {
			t.Fatalf("%q: menu at row %d, want %d:\n%s", c.text, at, c.line+1, ansi.Strip(strings.Join(lines, "\n")))
		}
	}
}

func TestComposeFitsTinyTerminals(t *testing.T) {
	for _, size := range [][2]int{{30, 8}, {20, 5}, {1, 1}, {0, 0}, {80, 3}} {
		w, h := size[0], size[1]
		for _, editor := range []bool{false, true} {
			m, _ := openNewSong(t, fakeDrafter{})
			m = fillValid(m)
			if editor {
				m = press(t, m, "down", "down", "down", "down", "down", "down", "enter")
			}
			m.compose.setMsg(toneWarn, "%s", strings.Repeat("a long message ", 12))
			m, _ = send(t, m, tea.WindowSizeMsg{Width: w, Height: h})
			view := m.View()
			lines := strings.Split(view, "\n")
			if view != "" && len(lines) > h {
				t.Errorf("%dx%d editor=%v: %d lines", w, h, editor, len(lines))
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > w {
					t.Errorf("%dx%d editor=%v: line %q is wider than the terminal", w, h, editor, ansi.Strip(l))
					break
				}
			}
		}
	}
}

func TestAutoLengthLeavesTheLengthToTheWriter(t *testing.T) {
	d := newRecordingDrafter(sampleDraft, nil)
	m, store := openNewSong(t, d)
	m = press(t, m, "an idea")
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if *d.calls != 1 || d.opts.Seconds != 0 {
		t.Fatalf("auto length sent %v", d.opts.Seconds)
	}
	if m.compose.seconds != sampleDraft.Seconds || !strings.Contains(plain(m), "‹ 0:45 ›") {
		t.Fatalf("the form should take the drafted length: %v", m.compose.seconds)
	}
	m = press(t, m, "up", "up", "up", "up", "up")
	m, _ = send(t, m, key("enter"), key("enter"))
	if d.opts.Seconds != 0 {
		t.Fatalf("a second draft sent the drafted length %v as a target", d.opts.Seconds)
	}

	// Without a draft, auto saves the default length.
	m, store = openNewSong(t, fakeDrafter{})
	m = fillValid(m)
	m, cmd = send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	if songs, _ := store.List(); len(songs) != 1 || songs[0].Seconds != m.defaultSeconds() {
		t.Fatalf("songs %+v", songs)
	}
}

func TestAChosenLengthIsSentAndKept(t *testing.T) {
	d := newRecordingDrafter(sampleDraft, nil)
	m, _ := openNewSong(t, d)
	m = press(t, m, "down", "down", "down", "right", "right", "up", "up", "up", "an idea")
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if d.opts.Seconds != 135 || m.compose.seconds != 135 {
		t.Fatalf("sent %v, kept %v", d.opts.Seconds, m.compose.seconds)
	}
}

func TestAnOddStoredLengthIsShownAsIs(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	if _, err := store.Create(library.Song{Title: "Odd", Seconds: 100, Status: library.StatusDraft}); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	m = press(t, m, "up", "up", "up")
	if m.compose.focus != rowLength || m.compose.seconds != 100 || !strings.Contains(plain(m), "‹ 1:40 ›") {
		t.Fatalf("focus %v seconds %v", m.compose.focus, m.compose.seconds)
	}
	m = press(t, m, "left")
	if m.compose.seconds != 90 {
		t.Fatalf("stepped to %v", m.compose.seconds)
	}
}

func TestTheSeedShowsOnlyWhenPinned(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	song, err := store.Create(library.Song{Title: "Drawn", Seconds: 60, Seed: 77, Status: library.StatusDraft, JobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	if m.compose.seed.Value() != "" {
		t.Fatalf("an unpinned seed shows %q", m.compose.seed.Value())
	}
	m.compose.title.SetValue("Drawn again")
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	got, _ := store.Load(song.ID)
	if got.SeedPinned || got.Seed != 77 || got.JobID != "job-1" {
		t.Fatalf("a blank seed should keep the drawn one unpinned: %+v", got)
	}
}

func TestANewTakeWithAPinnedSeedSaysItRepeats(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	s := library.Song{Title: "Same", Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption, Seconds: 60, Seed: 5, SeedPinned: true,
		Status: library.StatusDone, Audio: "song.flac"}
	s, err := store.Create(s)
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m, cmd := send(t, m, key("r"))
	runCmd(t, m, cmd)
	got, _ := store.Load(s.ID)
	if got.Status != library.StatusQueued || got.Seed != 5 || !strings.Contains(m.notice, "identical unless the lyrics, style or seed change") {
		t.Fatalf("song %+v notice %q", got, m.notice)
	}
}
