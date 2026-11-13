package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// recordingDrafter counts drafts and keeps the options of the last one, and
// the songs and instructions sent to Revise.
type recordingDrafter struct {
	calls   *int
	opts    *writer.Options
	revises *[]revision
	draft   writer.Draft
	err     error
}

type revision struct {
	current     writer.Draft
	instruction string
}

func newRecordingDrafter(d writer.Draft, err error) recordingDrafter {
	return recordingDrafter{calls: new(int), opts: new(writer.Options), revises: new([]revision), draft: d, err: err}
}

func (r recordingDrafter) Revise(_ context.Context, current writer.Draft, _, instruction string, _ writer.Options) (writer.Draft, error) {
	*r.revises = append(*r.revises, revision{current, instruction})
	return r.draft, r.err
}

func (r recordingDrafter) Write(_ context.Context, _ string, opts writer.Options) (writer.Draft, error) {
	*r.calls++
	*r.opts = opts
	return r.draft, r.err
}

// openNewSong opens an empty form from the library.
func openNewSong(t *testing.T, d Drafter) (app, *library.Store) {
	t.Helper()
	m, store := newTestModel(t, d)
	m = nav(t, m, "n")
	return m, store
}

// fillValid gives the form everything queueing needs.
func fillValid(m app) app {
	m.compose.title.SetValue("Sock Thief")
	m.compose.texts = [4]string{sampleDraft.Lyrics, "Garage rock", "Shouted lead", "Verse: fuzz."}
	return m
}

func press(t *testing.T, m app, keys ...string) app {
	t.Helper()
	for _, k := range keys {
		m, _ = send(t, m, key(k))
	}
	return m
}

func TestFormFocusMovesAndWraps(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	if m.compose.focus != rowIdea {
		t.Fatalf("new song starts on %v", m.compose.focus)
	}
	m = press(t, m, "up")
	if m.compose.focus != rowArrangement {
		t.Fatalf("up from Idea went to %v", m.compose.focus)
	}
	m = press(t, m, "tab")
	if m.compose.focus != rowIdea {
		t.Fatalf("tab from Arrangement went to %v", m.compose.focus)
	}
	m = press(t, m, "down", "down", "down", "shift+tab")
	if m.compose.focus != rowTitle {
		t.Fatalf("focus %v", m.compose.focus)
	}
	m = press(t, m, "enter")
	if m.compose.focus != rowLength {
		t.Fatalf("enter on Title should move on, got %v", m.compose.focus)
	}
}

func TestLengthStepsAndClamps(t *testing.T) {
	cases := []struct {
		from float64
		dir  int
		want float64
	}{
		{120, 1, 135}, {120, -1, 105}, {20, 1, 30}, {20, -1, 15},
		{300, 1, 300}, {15, -1, 10}, {10, -1, 10}, {10, 1, 15}, {295, 1, 300}, {100, 1, 105}, {100, -1, 90},
	}
	for _, c := range cases {
		if got := stepLength(c.from, c.dir); got != c.want {
			t.Errorf("stepLength(%v, %d) = %v, want %v", c.from, c.dir, got, c.want)
		}
	}

	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "down", "down", "down")
	if !strings.Contains(plain(m), "‹ auto ›") {
		t.Fatalf("a new song's length starts on auto:\n%s", plain(m))
	}
	m = press(t, m, "right")
	if m.compose.seconds != 120 || m.compose.autoLength || !strings.Contains(plain(m), "‹ 2:00 ›") {
		t.Fatalf("the first step from auto lands on the default length, got %v", m.compose.seconds)
	}
	m = press(t, m, "right")
	if m.compose.seconds != 135 || !strings.Contains(plain(m), "‹ 2:15 ›") {
		t.Fatalf("seconds %v:\n%s", m.compose.seconds, plain(m))
	}
	for range 30 {
		m = press(t, m, "left")
	}
	if m.compose.seconds != minLength || !strings.Contains(plain(m), "‹ 0:10 ›") {
		t.Fatalf("clamped low to %v", m.compose.seconds)
	}
	for range 30 {
		m = press(t, m, "right")
	}
	if m.compose.seconds != maxLength || !strings.Contains(plain(m), "‹ 5:00 ›") {
		t.Fatalf("clamped high to %v", m.compose.seconds)
	}
}

func TestVocalsAndLengthReachTheDraftAndTheSong(t *testing.T) {
	d := newRecordingDrafter(writer.Draft{Title: "Drift", Lyrics: "[Intro]\n[Instrumental]\n[Outro]",
		Caption: writer.Caption{GlobalMetadata: "Ambient", VocalDetails: "Instrumental; lead is a synth", Arrangement: "Intro: pads."}}, nil)
	m, store := openNewSong(t, d)
	m = press(t, m, "down", "down", "down", "left", "left", "down", "right")
	if !m.compose.instrumental || !strings.Contains(plain(m), "‹ instrumental ›") {
		t.Fatalf("vocals did not cycle:\n%s", plain(m))
	}
	m = press(t, m, "up", "up", "up", "up", "a slow drift")
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if *d.calls != 1 || !d.opts.Instrumental || d.opts.Seconds != 105 {
		t.Fatalf("draft options %+v after %d calls", *d.opts, *d.calls)
	}
	m, cmd = send(t, m, key("ctrl+s"))
	runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 1 || !songs[0].Instrumental || songs[0].Seconds != 105 || songs[0].Status != library.StatusQueued {
		t.Fatalf("songs %+v (msg %q)", songs, m.compose.msg)
	}
}

func TestInstrumentalNeedsTagsNotSungLines(t *testing.T) {
	m, store := openNewSong(t, fakeDrafter{})
	m = fillValid(m)
	m.compose.instrumental = true
	m.compose.texts[0] = ""
	m = press(t, m, "ctrl+s")
	if m.screen != screenCompose || m.compose.focus != rowLyrics || !strings.Contains(m.compose.msg, "instrumental still needs section tags") {
		t.Fatalf("screen %v focus %v msg %q", m.screen, m.compose.focus, m.compose.msg)
	}
	m.compose.texts[0] = "[Intro]\n[Instrumental]\n[Outro]"
	m, cmd := send(t, m, key("ctrl+s"))
	runCmd(t, m, cmd)
	if songs, _ := store.List(); len(songs) != 1 || songs[0].Status != library.StatusQueued {
		t.Fatalf("tags-only instrumental should queue: %+v", songs)
	}
}

func TestSeedField(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{{"", 0, true}, {"  ", 0, true}, {"42", 42, true}, {"0", 0, true}, {"-1", 0, false}, {"4.2", 0, false}, {"abc", 0, false}} {
		got, pinned, err := parseSeed(c.in)
		if (err == nil) != c.ok || got != c.want || pinned != (c.ok && strings.TrimSpace(c.in) != "") {
			t.Errorf("parseSeed(%q) = %d, %v, %v", c.in, got, pinned, err)
		}
	}

	m, store := openNewSong(t, fakeDrafter{})
	m = fillValid(m)
	m.compose.seed.SetValue("lucky")
	m = press(t, m, "ctrl+d")
	if m.screen != screenCompose || m.compose.focus != rowSeed || !strings.Contains(m.compose.msg, "seed must be a whole number") {
		t.Fatalf("focus %v msg %q", m.compose.focus, m.compose.msg)
	}
	m.compose.seed.SetValue("42")
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 1 || songs[0].Seed != 42 || !songs[0].SeedPinned || songs[0].Status != library.StatusDraft {
		t.Fatalf("songs %+v", songs)
	}

	m, _ = send(t, m, switchScreenMsg(screenLibrary))
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	if m.compose.seed.Value() != "42" {
		t.Fatalf("seed field %q", m.compose.seed.Value())
	}
	m.compose.seed.SetValue("")
	m, cmd = send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	if s, _ := store.Load(songs[0].ID); s.SeedPinned {
		t.Fatalf("blank seed saved pinned: %+v", s)
	}
}

func TestEveryFieldMakesTheFormDirty(t *testing.T) {
	changes := map[string]func(m app) app{
		"idea":   func(m app) app { return press(t, m, "x") },
		"title":  func(m app) app { return press(t, m, "down", "down", "x") },
		"length": func(m app) app { return press(t, m, "down", "down", "down", "right") },
		"vocals": func(m app) app { return press(t, m, "down", "down", "down", "down", "right") },
		"seed":   func(m app) app { return press(t, m, "up", "up", "up", "up", "up", "7") },
		"lyrics": func(m app) app {
			return press(t, m, "up", "up", "up", "up", "enter", "x", "esc", ":", "w", "enter")
		},
		"arrangement": func(m app) app { return press(t, m, "up", "enter", "x", "esc", ":", "w", "enter") },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			m, _ := openNewSong(t, fakeDrafter{})
			m = change(m)
			if !m.compose.dirty() {
				t.Fatal("form not dirty")
			}
			m = press(t, m, "esc")
			if m.screen != screenCompose || m.compose.msg != unsavedChanges {
				t.Fatalf("first esc: screen %v msg %q", m.screen, m.compose.msg)
			}
			if !strings.Contains(footerLine(m), "esc discard changes") {
				t.Fatalf("footer %q", footerLine(m))
			}
			m = press(t, m, "esc")
			if m.screen != screenLibrary {
				t.Fatalf("second esc left to %v", m.screen)
			}
		})
	}
}

func TestDirtyEscIsDisarmedByOtherKeys(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "x", "esc", "down", "esc")
	if m.screen != screenCompose {
		t.Fatal("a key between the two esc presses should ask again")
	}
	m, _ = openNewSong(t, fakeDrafter{})
	if m = press(t, m, "esc"); m.screen != screenLibrary {
		t.Fatal("esc on an untouched form should leave at once")
	}
}

func TestQueueValidationFocusesTheField(t *testing.T) {
	cases := []struct {
		edit func(*composeModel)
		row  formRow
		want string
	}{
		{func(c *composeModel) { c.texts[0] = "words without tags" }, rowLyrics, "section tag"},
		{func(c *composeModel) { c.texts[1] = " " }, rowGlobal, "the Global metadata box is empty"},
		{func(c *composeModel) { c.texts[2] = "" }, rowVocal, "the Vocal details box is empty"},
		{func(c *composeModel) { c.texts[3] = "" }, rowArrangement, "the Arrangement box is empty"},
		{func(c *composeModel) { c.texts[2], c.texts[3] = "", "" }, rowVocal, "the Vocal details box is empty"},
	}
	for _, c := range cases {
		m, store := openNewSong(t, fakeDrafter{})
		m = fillValid(m)
		c.edit(&m.compose)
		m = press(t, m, "ctrl+s")
		if m.compose.focus != c.row || !strings.Contains(m.compose.msg, c.want) || strings.Contains(m.compose.msg, "\n") {
			t.Errorf("focus %v msg %q, want %v %q", m.compose.focus, m.compose.msg, c.row, c.want)
		}
		if songs, _ := store.List(); len(songs) != 0 {
			t.Errorf("saved %d songs", len(songs))
		}
	}
}

func TestSaveDraftSkipsValidationButNotAnEmptyForm(t *testing.T) {
	m, store := openNewSong(t, fakeDrafter{})
	m = press(t, m, "ctrl+d")
	if m.screen != screenCompose || !strings.Contains(m.compose.msg, "Nothing to save") {
		t.Fatalf("empty form: screen %v msg %q", m.screen, m.compose.msg)
	}
	m = press(t, m, "half an idea")
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 1 || songs[0].Idea != "half an idea" || songs[0].Title != "Untitled" || songs[0].Status != library.StatusDraft {
		t.Fatalf("songs %+v", songs)
	}
}

func TestDraftOverLyricsAsksFirst(t *testing.T) {
	d := newRecordingDrafter(sampleDraft, nil)
	m, _ := openNewSong(t, d)
	m.compose.texts[0] = "[Verse]\nmine"
	m = press(t, m, "idea", "enter")
	if m.compose.drafting || *d.calls != 0 || !strings.Contains(m.compose.msg, "enter again drafts") {
		t.Fatalf("first enter: drafting %v msg %q", m.compose.drafting, m.compose.msg)
	}
	m = press(t, m, "x", "enter")
	if m.compose.drafting {
		t.Fatal("another key should cancel the warning")
	}
	m, cmd := send(t, m, key("enter"))
	if !m.compose.drafting {
		t.Fatal("second enter should draft")
	}
	m = runCmd(t, m, cmd)
	if m.compose.texts[0] != sampleDraft.Lyrics {
		t.Fatalf("lyrics %q", m.compose.texts[0])
	}
}

func TestEscCancelsADraft(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{draft: sampleDraft})
	m = press(t, m, "idea")
	m, cmd := send(t, m, key("enter"))
	if !strings.Contains(footerLine(m), "esc cancel the draft") {
		t.Fatalf("footer while drafting %q", footerLine(m))
	}
	m = press(t, m, "down", "ctrl+d")
	if m.screen != screenCompose || m.compose.focus != rowIdea {
		t.Fatal("only esc acts while drafting")
	}
	m = press(t, m, "esc")
	if m.compose.drafting || m.screen != screenCompose || !strings.Contains(m.compose.msg, "Draft cancelled") {
		t.Fatalf("drafting %v screen %v msg %q", m.compose.drafting, m.screen, m.compose.msg)
	}
	// The cancelled draft's command finds its context done and reports nothing.
	m = runCmd(t, m, cmd)
	m, _ = send(t, m, draftDoneMsg{seq: m.compose.draftSeq - 1, draft: sampleDraft})
	if m.compose.title.Value() != "" || m.compose.texts[0] != "" {
		t.Fatal("a cancelled draft filled the form")
	}
}

func TestFailedDraftKeepsWhatItCannotReplace(t *testing.T) {
	partial := writer.Draft{Title: "Half", Caption: writer.Caption{GlobalMetadata: "Lo-fi"}}
	m, _ := openNewSong(t, fakeDrafter{draft: partial, err: errors.New("draft is incomplete")})
	m.compose.texts[0] = "[Verse]\nmine"
	m = press(t, m, "idea", "enter")
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	c := m.compose
	if c.texts[0] != "[Verse]\nmine" || c.texts[1] != "Lo-fi" || c.title.Value() != "Half" || !strings.Contains(c.msg, "failed: draft is incomplete") {
		t.Fatalf("texts %q title %q msg %q", c.texts, c.title.Value(), c.msg)
	}
}

func TestWriterUnavailableMeansWriteByHand(t *testing.T) {
	checks := readyChecks()
	checks[5].Result, checks[5].Missing = health.Warn, "Ollama"
	d := newRecordingDrafter(sampleDraft, nil)
	a, _ := newLauncherApp(t, checks)
	a.d.Drafter = d
	a = nav(t, a, "enter")
	view := plain(a)
	if !strings.Contains(view, "Writer unavailable: Ollama is not reachable") || !strings.Contains(view, "written by hand") {
		t.Fatalf("idea box:\n%s", view)
	}
	if strings.Contains(plain(a), "enter draft") {
		t.Fatalf("footer offers drafting:\n%s", plain(a))
	}
	a = press(t, a, "an idea", "enter")
	if *d.calls != 0 || a.compose.drafting || a.compose.focus != rowTitle {
		t.Fatalf("calls %d drafting %v focus %v", *d.calls, a.compose.drafting, a.compose.focus)
	}

	checks[5].Result, checks[5].Fix = health.Warn, health.FixWriterModel
	a, _ = newLauncherApp(t, checks)
	a = nav(t, a, "enter")
	if !strings.Contains(plain(a), "is not installed in Ollama") {
		t.Fatalf("missing model:\n%s", plain(a))
	}
}

func TestQueueWhileSetupIsMissingSavesADraft(t *testing.T) {
	checks := missingModelChecks()
	checks[1].Missing = "music model files"
	a, store := newLauncherApp(t, checks)
	a = press(t, a, "down")
	a = nav(t, a, "enter")
	if a.screen != screenCompose {
		t.Fatalf("screen %v", a.screen)
	}
	a = fillValid(a)
	a, cmd := send(t, a, key("ctrl+s"))
	a = runCmd(t, a, cmd)
	songs, _ := store.List()
	if len(songs) != 1 || songs[0].Status != library.StatusDraft {
		t.Fatalf("songs %+v", songs)
	}
	if !strings.Contains(a.notice, "music model files") || !strings.Contains(a.notice, "Finish setup") {
		t.Fatalf("notice %q", a.notice)
	}
}

func TestTextEditorInTheForm(t *testing.T) {
	m, store := openNewSong(t, fakeDrafter{})
	m = fillValid(m)
	m.compose.initial = m.compose.fields()
	m = press(t, m, "up", "up", "up", "up", "enter")
	if m.compose.editor == nil || m.compose.editorRow != rowLyrics {
		t.Fatal("enter on Lyrics should open the editor")
	}
	if v := plain(m); !strings.Contains(v, "INSERT") || !strings.Contains(v, "[ then tab: section tags") {
		t.Fatalf("editor view:\n%s", v)
	}

	m = press(t, m, "enter", "[", "B", "tab", "enter", "esc")
	m = press(t, m, ":", "w", "enter")
	if m.compose.editor != nil || m.compose.focus != rowLyrics || !strings.HasSuffix(m.compose.texts[0], "\n[Bridge]") {
		t.Fatalf(":w: editor %v lyrics %q", m.compose.editor != nil, m.compose.texts[0])
	}

	before := m.compose.texts[0]
	m = press(t, m, "enter", "gone", "esc")
	m = press(t, m, "esc")
	if m.compose.editor == nil || m.compose.msg != idleEscHint {
		t.Fatalf("idle esc: open %v msg %q", m.compose.editor != nil, m.compose.msg)
	}
	m = press(t, m, ":", "q", "enter")
	if m.compose.editor != nil || m.compose.texts[0] != before || !strings.Contains(m.compose.msg, "Discarded") {
		t.Fatalf(":q: lyrics %q msg %q", m.compose.texts[0], m.compose.msg)
	}

	m = press(t, m, "down", "enter", " Bass.")
	m, cmd := send(t, m, key("esc"), key(":"), key("w"), key("q"), key("enter"))
	m = runCmd(t, m, cmd)
	songs, _ := store.List()
	if m.screen != screenLibrary || len(songs) != 1 || songs[0].Status != library.StatusDraft || songs[0].Caption.GlobalMetadata != "Garage rock Bass." {
		t.Fatalf(":wq: screen %v songs %+v", m.screen, songs)
	}
}

func TestSaveKeysFromInsideTheEditor(t *testing.T) {
	for _, k := range []string{"ctrl+s", "ctrl+d"} {
		m, store := openNewSong(t, fakeDrafter{})
		m = fillValid(m)
		m = press(t, m, "up", "enter", " Outro: fade.")
		m, cmd := send(t, m, key(k))
		m = runCmd(t, m, cmd)
		songs, _ := store.List()
		want := map[string]library.Status{"ctrl+s": library.StatusQueued, "ctrl+d": library.StatusDraft}[k]
		if m.screen != screenLibrary || len(songs) != 1 || songs[0].Status != want || songs[0].Caption.Arrangement != "Verse: fuzz. Outro: fade." {
			t.Fatalf("%s: screen %v songs %+v", k, m.screen, songs)
		}
	}
}

func TestQueueFromTheEditorStillValidates(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = fillValid(m)
	m.compose.texts[2] = ""
	m = press(t, m, "up", "enter", " more", "ctrl+s")
	if m.compose.editor != nil || m.compose.focus != rowVocal || m.compose.texts[3] != "Verse: fuzz. more" {
		t.Fatalf("editor %v focus %v arrangement %q", m.compose.editor != nil, m.compose.focus, m.compose.texts[3])
	}
}

func TestExternalEditorHandoff(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "up", "ctrl+e")
	if !strings.Contains(m.compose.msg, "neither is set") || strings.Contains(footerLine(m)+plain(m), "ctrl+e $EDITOR   ↑") {
		t.Fatalf("msg %q", m.compose.msg)
	}

	t.Setenv("EDITOR", "true")
	m, cmd := send(t, m, key("ctrl+e"))
	if cmd == nil {
		t.Fatal("ctrl+e with $EDITOR set should run it")
	}
	path := filepath.Join(t.TempDir(), "back.txt")
	if err := os.WriteFile(path, []byte("Outro: tape stop.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ = send(t, m, editorDoneMsg{field: rowArrangement, path: path})
	if m.compose.texts[3] != "Outro: tape stop." {
		t.Fatalf("arrangement %q", m.compose.texts[3])
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}

	m = press(t, m, "enter")
	path = filepath.Join(t.TempDir(), "again.txt")
	if err := os.WriteFile(path, []byte("Intro: rain."), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ = send(t, m, editorDoneMsg{field: rowArrangement, path: path})
	if m.compose.editor == nil || m.compose.editor.text() != "Intro: rain." {
		t.Fatal("text from $EDITOR should land in the open editor")
	}
}

func TestFormFitsAt80x24(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{draft: sampleDraft})
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = fillValid(m)
	check := func(state string) {
		t.Helper()
		lines := strings.Split(m.View(), "\n")
		if len(lines) > 24 {
			t.Fatalf("%s: %d lines", state, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > 80 {
				t.Fatalf("%s: line %d wide: %q", state, ansi.StringWidth(l), ansi.Strip(l))
			}
		}
		for _, l := range lines[len(lines)-2:] {
			if strings.HasSuffix(ansi.Strip(l), "…") {
				t.Fatalf("%s: key line cut: %q", state, ansi.Strip(l))
			}
		}
		if !m.compose.drafting && !strings.Contains(plain(m), "save & queue") {
			t.Fatalf("%s: no save key in view", state)
		}
		if v := plain(m); strings.Count(v, "╭") != strings.Count(v, "╰") {
			t.Fatalf("%s: a box is cut off:\n%s", state, v)
		}
	}
	for r := range rowCount {
		if m.compose.skip[r] {
			continue
		}
		check("focus " + r.name())
		focused := false
		for _, l := range strings.Split(plain(m), "\n") {
			if strings.Contains(l, r.name()) && (strings.Contains(l, "▸") || strings.Contains(l, "╭─ "+r.name())) {
				focused = true
			}
		}
		if !focused {
			t.Fatalf("focused row %s is off screen:\n%s", r.name(), plain(m))
		}
		m = press(t, m, "down")
	}
	m = press(t, m, "x", "esc")
	check("unsaved warning")
	m = press(t, m, "up", "up", "up", "up", "enter")
	check("insert")
	m = press(t, m, "enter", "[", "tab")
	if m.compose.editor == nil || m.compose.editor.menu == nil {
		t.Fatal("tag menu did not open")
	}
	check("tag menu")
	m = press(t, m, "esc", "esc")
	check("normal")
	m = press(t, m, "v")
	check("visual")
	m = press(t, m, "esc", ":")
	check("command")
	m = press(t, m, "esc", "esc")
	check("idle esc hint")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	m = press(t, m, "ctrl+e")
	if len(m.compose.messageLines()) < 2 {
		t.Fatalf("expected a wrapped message, got %q", m.compose.msg)
	}
	check("wrapped message in the editor")
	m = press(t, m, ":", "q", "enter", "up", "up", "up", "up", "up", "up")
	m, _ = send(t, m, key("enter"), key("enter"))
	if !m.compose.drafting {
		t.Fatalf("not drafting: focus %v msg %q", m.compose.focus, m.compose.msg)
	}
	check("drafting")
}

func TestEditExistingSongFillsEveryField(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	song, err := store.Create(library.Song{Title: "Tide", Idea: "the sea", Lyrics: "[Intro]\n[Outro]", Seconds: 37, Seed: 7, SeedPinned: true, Instrumental: true,
		Caption: writer.Caption{GlobalMetadata: "Ambient", VocalDetails: "None", Arrangement: "Pads."}, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	c := m.compose
	if c.base.ID != song.ID || c.idea.Value() != "the sea" || c.title.Value() != "Tide" || c.seconds != 37 || c.seed.Value() != "7" ||
		!c.instrumental || c.texts != [4]string{"[Intro]\n[Outro]", "Ambient", "None", "Pads."} || c.dirty() || c.focus != rowLyrics {
		t.Fatalf("form %+v", c.fields())
	}
	if !strings.Contains(plain(m), "Edit song") || !strings.Contains(plain(m), "‹ 0:37 ›") {
		t.Fatalf("view:\n%s", plain(m))
	}
}
