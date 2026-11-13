package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

const shortLyrics = "[Verse]\nGone again\nUnder the bed\n\n[Chorus]\nWhere'd my socks go"

var longerDraft = writer.Draft{
	Title:   "Sock Thief",
	Lyrics:  "[Verse]\nGone again\nUnder the bed\n\n[Chorus]\nWhere'd my socks go\n\n[Verse]\nThe dryer hums\nIt knows\n\n[Bridge]\nOne lone sock\n\n[Chorus]\nWhere'd my socks go",
	Caption: writer.Caption{GlobalMetadata: "Garage rock, 150 BPM", VocalDetails: "Shouted male lead", Arrangement: "Verse: fuzz. Bridge: drums drop. Chorus: everything."},
	Seconds: 200,
}

// openLyricSong opens the form on a saved draft that has lyrics and style.
func openLyricSong(t *testing.T, d Drafter) (app, *library.Store, library.Song) {
	t.Helper()
	m, store := newTestModel(t, d)
	song, err := store.Create(library.Song{Title: "Sock Thief", Idea: "socks", Lyrics: shortLyrics, Seconds: 120, Status: library.StatusDraft,
		Caption: writer.Caption{GlobalMetadata: "Garage rock, 150 BPM", VocalDetails: "Shouted male lead", Arrangement: "Verse: fuzz."}})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	return m, store, song
}

// revise opens the instruction, types it and runs it to the end.
func revise(t *testing.T, m app, instruction string) app {
	t.Helper()
	m = press(t, m, "ctrl+r", instruction)
	m, cmd := send(t, m, key("enter"))
	return runAll(t, m, cmd)
}

func writerDown(t *testing.T, m app) app {
	t.Helper()
	checks := readyChecks()
	checks[5].Result, checks[5].Detail = health.Warn, "Ollama is not reachable"
	m.d.Checks = func(context.Context, config.Config) []health.Check { return checks }
	return finishHealth(t, m)
}

func TestTheReviseRowAndKeyNeedLyricsAndAWriter(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "an idea")
	if strings.Contains(plain(m), "Revise with") || strings.Contains(footerLines(m), "ctrl+r") {
		t.Fatalf("offered without lyrics:\n%s", plain(m))
	}
	m = press(t, m, "ctrl+r")
	if m.compose.rv.stage != reviseOff || m.compose.msg != "Nothing to revise yet: draft or write the lyrics first." {
		t.Fatalf("ctrl+r without lyrics: stage %v msg %q", m.compose.rv.stage, m.compose.msg)
	}

	m, _, _ = openLyricSong(t, fakeDrafter{})
	model := m.cfg().Ollama.Model
	if !strings.Contains(plain(m), "✎ Revise with "+model+"…") {
		t.Fatalf("no revise row:\n%s", plain(m))
	}
	m = press(t, m, "up", "up", "up", "up", "up")
	if m.compose.focus != rowRevise || !strings.Contains(footerLines(m), "enter revise with "+model) {
		t.Fatalf("up from Lyrics reached %v: %q", m.compose.focus, footerLines(m))
	}
	for r := range rowCount {
		m.compose.focusOn(r)
		t.Setenv("EDITOR", "vi")
		m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		if !strings.Contains(footerLines(m), "ctrl+r revise") {
			t.Fatalf("footer on %s lacks ctrl+r: %q", r.name(), footerLines(m))
		}
		m = press(t, m, "ctrl+r")
		if m.compose.rv.stage != reviseInput || m.compose.focus != rowRevise || !m.compose.rv.input.Focused() {
			t.Fatalf("ctrl+r on %s: stage %v focus %v", r.name(), m.compose.rv.stage, m.compose.focus)
		}
		m = press(t, m, "esc")
	}

	m = writerDown(t, m)
	m = press(t, m, "ctrl+r")
	if m.compose.rv.stage != reviseOff || !strings.HasPrefix(m.compose.msg, "No revision: Ollama is not reachable") ||
		strings.Contains(plain(m), "Revise with") || strings.Contains(footerLines(m), "ctrl+r") {
		t.Fatalf("writer down: msg %q\n%s", m.compose.msg, plain(m))
	}
}

func TestTheInstructionInputRunsOrCancels(t *testing.T) {
	d := newRecordingDrafter(longerDraft, nil)
	m, _, _ := openLyricSong(t, d)
	m = press(t, m, "ctrl+r")
	if !strings.Contains(plain(m), "What should change?") || !strings.Contains(plain(m), "e.g. make it longer with two more verses that continue the story") ||
		footerLines(m) != "\n  enter revise with "+m.cfg().Ollama.Model+"   esc cancel" {
		t.Fatalf("input:\n%s", plain(m))
	}
	m = press(t, m, "enter")
	if m.compose.rv.stage != reviseInput || !strings.HasPrefix(m.compose.msg, "Say what should change first") || len(*d.revises) != 0 {
		t.Fatalf("empty instruction: stage %v msg %q", m.compose.rv.stage, m.compose.msg)
	}
	m = press(t, m, "add a verse", "esc")
	if m.compose.rv.stage != reviseOff || len(*d.revises) != 0 || m.compose.dirty() {
		t.Fatalf("esc: stage %v, %d revisions", m.compose.rv.stage, len(*d.revises))
	}
	m = press(t, m, "ctrl+r")
	m.compose.rv.input.SetValue("")
	m = press(t, m, "add a verse")
	m, cmd := send(t, m, key("enter"))
	if m.compose.rv.stage != reviseRunning || !m.compose.drafting || !strings.Contains(plain(m), "Revising with") ||
		!strings.Contains(footerLines(m), "esc cancel the revision") {
		t.Fatalf("running:\n%s", plain(m))
	}
	m = runAll(t, m, cmd)
	got := *d.revises
	if len(got) != 1 || got[0].instruction != "add a verse" || got[0].current.Lyrics != shortLyrics || got[0].current.Seconds != 120 {
		t.Fatalf("revisions %+v", got)
	}
	if m.compose.rv.stage != reviseReview {
		t.Fatalf("stage %v", m.compose.rv.stage)
	}
}

func TestCancellingARevisionChangesNothing(t *testing.T) {
	m, _, _ := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
	before := m.compose.fields()
	m = press(t, m, "ctrl+r", "longer")
	m, cmd := send(t, m, key("enter"))
	m = press(t, m, "esc")
	if m.compose.drafting || m.compose.rv.stage != reviseOff || !strings.HasPrefix(m.compose.msg, "Revision cancelled") {
		t.Fatalf("cancel: stage %v msg %q", m.compose.rv.stage, m.compose.msg)
	}
	m = runAll(t, m, cmd)
	m, _ = send(t, m, reviseDoneMsg{seq: m.compose.draftSeq - 1, draft: longerDraft})
	if m.compose.fields() != before || m.compose.rv.stage != reviseOff {
		t.Fatal("a cancelled revision reached the form")
	}
}

func TestTheReviewSummarisesAndMarksTheChanges(t *testing.T) {
	m, _, _ := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
	m = revise(t, m, "two more sections")
	want := "Lyrics: 6 → 16 lines · sections added: Verse 2, Bridge, Chorus 2 · length 2:00 → 3:20 · style: arrangement updated"
	if m.compose.rv.summary != want {
		t.Fatalf("summary\n%q\nwant\n%q", m.compose.rv.summary, want)
	}
	view := plain(m)
	for _, s := range []string{"+ The dryer hums", "+ [Bridge]", "  Gone again", "You asked: two more sections"} {
		if !strings.Contains(view, s) {
			t.Fatalf("review lacks %q:\n%s", s, view)
		}
	}
	if !strings.HasSuffix(view, "enter apply   r try again   e edit the instruction   esc discard") {
		t.Fatalf("footer:\n%s", view)
	}
	var plus, minus bool
	for _, l := range m.compose.reviewLines() {
		plus = plus || l == statusStyle.Render("+ The dryer hums")
		minus = minus || strings.HasPrefix(ansi.Strip(l), "- ")
	}
	if !plus || minus {
		t.Fatalf("marks: plus %v minus %v", plus, minus)
	}

	m.compose.rv.diff = lineDiff(splitLines("[Verse]\nold line"), splitLines("[Verse]\nnew line"))
	lines := m.compose.reviewLines()
	if lines[1] != errorStyle.Render("- old line") || lines[2] != statusStyle.Render("+ new line") {
		t.Fatalf("replaced line: %q", lines)
	}
}

func TestTheReviewFitsAnd80x24Scrolls(t *testing.T) {
	m, _, _ := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = revise(t, m, "make it longer with two more verses that continue the story and end with a quiet final chorus")
	lines := strings.Split(plain(m), "\n")
	if len(lines) != 24 {
		t.Fatalf("%d lines:\n%s", len(lines), plain(m))
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 80 {
			t.Fatalf("line wider than 80: %q", l)
		}
	}
	text := formText(m)
	for _, s := range []string{m.compose.rv.summary, "You asked: make it longer with two more verses that continue the story and end with a quiet final chorus"} {
		if !strings.Contains(text, s) {
			t.Fatalf("not wrapped whole: %q\n%s", s, plain(m))
		}
	}
	if !strings.Contains(lines[23], "enter apply   r try again   e edit the instruction   esc discard   ↑/↓ scroll") {
		t.Fatalf("footer %q", lines[23])
	}
	m = press(t, m, "down", "down")
	if m.compose.rv.scroll != 2 || !strings.Contains(plain(m), "lines 3–") {
		t.Fatalf("scroll %d:\n%s", m.compose.rv.scroll, plain(m))
	}
	for range 40 {
		m = press(t, m, "down")
	}
	if !strings.Contains(plain(m), "+ Where'd my socks go") {
		t.Fatalf("end not reachable:\n%s", plain(m))
	}
}

func TestApplyingARevisionFillsTheFormAndSavesNothing(t *testing.T) {
	m, store, song := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
	m = revise(t, m, "longer")
	m = press(t, m, "enter")
	c := m.compose
	if c.rv.stage != reviseOff || c.texts[0] != longerDraft.Lyrics || c.texts[3] != longerDraft.Caption.Arrangement || c.seconds != 200 || !c.dirty() {
		t.Fatalf("applied: %+v dirty %v", c.fields(), c.dirty())
	}
	if !strings.HasPrefix(c.msg, "Revision applied; nothing is saved yet.") || c.focus != rowLyrics {
		t.Fatalf("msg %q focus %v", c.msg, c.focus)
	}
	stored, err := store.Load(song.ID)
	if err != nil || stored.Lyrics != shortLyrics {
		t.Fatalf("stored lyrics changed: %q %v", stored.Lyrics, err)
	}
	if songs, _ := store.List(); len(songs) != 1 {
		t.Fatalf("%d songs", len(songs))
	}
}

func TestDiscardingARevisionKeepsEveryField(t *testing.T) {
	m, _, _ := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
	m.compose.seed.SetValue("77")
	before, title, seed := m.compose.fields(), m.compose.title.Value(), m.compose.seed.Value()
	m = revise(t, m, "longer")
	m = press(t, m, "esc")
	c := m.compose
	if c.fields() != before || c.title.Value() != title || c.seed.Value() != seed || c.rv.stage != reviseOff {
		t.Fatalf("discard changed the form: %+v", c.fields())
	}
	if c.msg != "Revision discarded; the song is as it was." {
		t.Fatalf("msg %q", c.msg)
	}
}

func TestRetryAndEditTheInstruction(t *testing.T) {
	d := newRecordingDrafter(longerDraft, nil)
	m, _, _ := openLyricSong(t, d)
	m = revise(t, m, "longer")
	m, cmd := send(t, m, key("r"))
	m = runAll(t, m, cmd)
	if got := *d.revises; len(got) != 2 || got[1].instruction != "longer" || got[1].current.Lyrics != shortLyrics || m.compose.rv.stage != reviseReview {
		t.Fatalf("retry: %+v", got)
	}
	m = press(t, m, "e")
	if m.compose.rv.stage != reviseInput || m.compose.rv.input.Value() != "longer" {
		t.Fatalf("e: stage %v value %q", m.compose.rv.stage, m.compose.rv.input.Value())
	}
	m = press(t, m, " still")
	m, cmd = send(t, m, key("enter"))
	runAll(t, m, cmd)
	if got := *d.revises; len(got) != 3 || got[2].instruction != "longer still" {
		t.Fatalf("edited: %+v", got)
	}
}

func TestTheRevisionLengthRule(t *testing.T) {
	t.Run("longer replaces a length the user did not set", func(t *testing.T) {
		m, _, _ := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
		m = press(t, revise(t, m, "longer"), "enter")
		if m.compose.seconds != 200 {
			t.Fatalf("seconds %v", m.compose.seconds)
		}
	})
	t.Run("a length the user set is kept", func(t *testing.T) {
		m, _, _ := openLyricSong(t, newRecordingDrafter(longerDraft, nil))
		m.compose.focusOn(rowLength)
		m = press(t, m, "right")
		m = revise(t, m, "longer")
		if want := "length kept at 2:15 as you set it (the writer suggested 3:20)"; !strings.Contains(m.compose.rv.summary, want) {
			t.Fatalf("summary %q", m.compose.rv.summary)
		}
		m = press(t, m, "enter")
		if m.compose.seconds != 135 {
			t.Fatalf("seconds %v", m.compose.seconds)
		}
	})
	t.Run("shorter keeps the form's length", func(t *testing.T) {
		shorter := longerDraft
		shorter.Seconds = 90
		m, _, _ := openLyricSong(t, newRecordingDrafter(shorter, nil))
		m = press(t, revise(t, m, "tighter"), "enter")
		if m.compose.seconds != 120 {
			t.Fatalf("seconds %v", m.compose.seconds)
		}
	})
}

func TestAFailedRevisionSaysWhyAndChangesNothing(t *testing.T) {
	m, _, _ := openLyricSong(t, newRecordingDrafter(writer.Draft{Lyrics: "half a song"}, errors.New("context deadline exceeded")))
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	before := m.compose.fields()
	m = revise(t, m, "longer")
	want := "Revision with " + m.cfg().Ollama.Model + " failed: context deadline exceeded. The song is unchanged; enter tries again, esc leaves it as it is."
	if m.compose.fields() != before || m.compose.tone != toneBad || !strings.Contains(formText(m), want) {
		t.Fatalf("want %q on screen:\n%s", want, plain(m))
	}
	if m.compose.rv.stage != reviseInput || m.compose.rv.input.Value() != "longer" || len(m.compose.messageLines()) < 2 {
		t.Fatalf("stage %v input %q", m.compose.rv.stage, m.compose.rv.input.Value())
	}
}

func TestLibraryReviseOnADraftEditsInPlace(t *testing.T) {
	m, store := newTestModel(t, newRecordingDrafter(longerDraft, nil))
	song, err := store.Create(library.Song{Title: "Sock Thief", Lyrics: shortLyrics, Seconds: 120, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "v")
	c := m.compose
	if m.screen != screenCompose || c.copied || c.base.ID != song.ID || c.rv.stage != reviseInput || !c.rv.input.Focused() || c.note != "" {
		t.Fatalf("v on a draft: copied %v base %q stage %v", c.copied, c.base.ID, c.rv.stage)
	}
}

func TestLibraryReviseOnADoneSongMakesASeededCopy(t *testing.T) {
	m, store := newTestModel(t, newRecordingDrafter(longerDraft, nil))
	done, err := store.Create(library.Song{Title: "Sock Thief", Lyrics: shortLyrics, Seconds: 120, Status: library.StatusDone, Seed: 281474976710, Audio: "song.flac", Duration: 96,
		Caption: writer.Caption{GlobalMetadata: "Garage rock", VocalDetails: "Shouted", Arrangement: "Verse: fuzz."}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.AudioPath(done), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "v")
	c := m.compose
	if !c.copied || c.base.ID != "" || c.base.DerivedFrom != done.ID || c.seed.Value() != "281474976710" || c.title.Value() != "Sock Thief (extended)" ||
		c.rv.stage != reviseInput || c.dirty() {
		t.Fatalf("copy: %+v derived %q stage %v dirty %v", c.fields(), c.base.DerivedFrom, c.rv.stage, c.dirty())
	}
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	if !strings.Contains(plain(m), "pinned · every render uses it") {
		t.Fatalf("seed hint cut at 80 columns:\n%s", plain(m))
	}
	if !strings.Contains(formText(m), extendedNote) || !strings.Contains(formText(m), "Edit a copy of Sock Thief") {
		t.Fatalf("notice missing:\n%s", plain(m))
	}
	m = press(t, m, "longer")
	if !strings.Contains(formText(m), extendedNote) {
		t.Fatal("the notice went away on typing")
	}
	m, cmd := send(t, m, key("enter"))
	m = runAll(t, m, cmd)
	m = press(t, m, "enter")
	m, cmd = send(t, m, key("ctrl+d"))
	m = runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 2 {
		t.Fatalf("%d songs", len(songs))
	}
	var saved library.Song
	for _, s := range songs {
		if s.ID != done.ID {
			saved = s
		}
	}
	if saved.DerivedFrom != done.ID || !saved.SeedPinned || saved.Seed != done.Seed || saved.Title != "Sock Thief (extended)" || saved.Lyrics != longerDraft.Lyrics {
		t.Fatalf("saved copy %+v", saved)
	}
	orig, _ := store.Load(done.ID)
	if orig.Lyrics != shortLyrics || orig.Status != library.StatusDone {
		t.Fatalf("original changed: %+v", orig)
	}
	m.library.selectID = saved.ID
	m.selectPending()
	m.layoutDetail()
	detail := strings.Join(strings.Fields(m.detailText(200)), " ")
	if !strings.Contains(ansi.Strip(detail), "from Sock Thief") || !strings.Contains(ansi.Strip(detail), "Seed 281474976710 · pinned · from Sock Thief") {
		t.Fatalf("detail:\n%s", ansi.Strip(m.detailText(200)))
	}
}

func TestLibraryReviseNeedsLyrics(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	if _, err := store.Create(library.Song{Title: "Idea only", Idea: "socks", Status: library.StatusDraft}); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	if strings.Contains(footerLine(m), "v revise") {
		t.Fatalf("footer %q", footerLine(m))
	}
	m, cmd := send(t, m, key("v"))
	if cmd != nil || m.screen != screenLibrary || m.notice != "v revises a song that has lyrics; g drafts them from the idea, e edits." {
		t.Fatalf("v: screen %v notice %q", m.screen, m.notice)
	}
}

func TestSectionChanges(t *testing.T) {
	seen := sectionLabeler{}
	var got []string
	for _, l := range splitLines("[Verse]\na\n[Chorus]\nb\n[Verse]\nc\n[Verse 2]\n[Verse 2]\n[Chorus]\n[Bridge]\nnot a tag") {
		if label := seen.label(l); label != "" {
			got = append(got, label)
		}
	}
	if want := "Verse|Chorus|Verse 2|Verse 2|Verse 2 (2)|Chorus 2|Bridge"; strings.Join(got, "|") != want {
		t.Fatalf("labels %q", got)
	}
	added, removed := sectionChanges("[Verse]\n[Chorus]\n[Outro]", "[Verse]\n[Bridge]\n[Chorus]")
	if strings.Join(added, ",") != "Bridge" || strings.Join(removed, ",") != "Outro" {
		t.Fatalf("added %q removed %q", added, removed)
	}
	added, _ = sectionChanges("[Verse]\na\n[Chorus]\nb\n[Verse]\nc", "[Verse]\na\n[Verse]\nnew\n[Chorus]\nb\n[Verse]\nc")
	if strings.Join(added, ",") != "Verse 2" {
		t.Fatalf("a verse inserted before the last one: %q", added)
	}
}
