package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/ollama"
)

// runAll runs cmd and every command it batches, sending back the messages
// the form and Settings act on.
func runAll(t *testing.T, a app, cmd tea.Cmd) app {
	t.Helper()
	if cmd == nil {
		return a
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			a = runAll(t, a, c)
		}
	case draftDoneMsg, settingsAsync, healthDoneMsg, songsMsg, openComposeMsg:
		var next tea.Cmd
		a, next = send(t, a, msg)
		a = runAll(t, a, next)
	}
	for {
		msg, ok := a.events.next()
		if !ok {
			return a
		}
		var next tea.Cmd
		a, next = send(t, a, msg)
		a = runAll(t, a, next)
	}
}

// formText is the form as drawn, with box borders and runs of spaces
// folded, so text wrapped over lines reads as one sentence.
func formText(a app) string {
	v := strings.NewReplacer("│", " ", "╭", " ", "╰", " ", "╮", " ", "╯", " ", "─", " ").Replace(plain(a))
	return strings.Join(strings.Fields(v), " ")
}

func TestTheDraftRowDrafts(t *testing.T) {
	d := newRecordingDrafter(sampleDraft, nil)
	m, _ := openNewSong(t, d)
	m = press(t, m, "a cat who steals socks", "down")
	if m.compose.focus != rowDraft || !strings.Contains(plain(m), "▸ ✦ Draft lyrics and style with "+m.cfg().Ollama.Model) {
		t.Fatalf("down from Idea is on %v:\n%s", m.compose.focus, plain(m))
	}
	m, cmd := send(t, m, key("enter"))
	m = runAll(t, m, cmd)
	if *d.calls != 1 || m.compose.texts[0] != sampleDraft.Lyrics {
		t.Fatalf("calls %d lyrics %q", *d.calls, m.compose.texts[0])
	}
}

func TestCtrlGDraftsFromEveryRow(t *testing.T) {
	for r := range rowCount {
		d := newRecordingDrafter(sampleDraft, nil)
		m, _ := openNewSong(t, d)
		m = press(t, m, "an idea")
		m.compose.focusOn(r)
		m, cmd := send(t, m, key("ctrl+g"))
		m = runAll(t, m, cmd)
		if *d.calls != 1 || m.compose.editor != nil {
			t.Errorf("ctrl+g on %s: %d calls", r.name(), *d.calls)
		}
	}
}

func TestFormFooterOffersTheDraftOnlyWithAnIdeaAndAWriter(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	if strings.Contains(footerLines(m), "ctrl+g") {
		t.Fatalf("offered without an idea: %q", footerLines(m))
	}
	m = press(t, m, "an idea")
	for range rowCount {
		if !strings.Contains(footerLines(m), "ctrl+g draft from idea") {
			t.Fatalf("not offered on %s: %q", m.compose.focus.name(), footerLines(m))
		}
		m = press(t, m, "down")
	}
	checks := readyChecks()
	checks[5].Result, checks[5].Detail = health.Warn, "Ollama is not reachable"
	m.d.Checks = func(context.Context, config.Config) []health.Check { return checks }
	m = finishHealth(t, m)
	if strings.Contains(footerLines(m), "ctrl+g") {
		t.Fatalf("offered without a writer: %q", footerLines(m))
	}
}

func footerLines(a app) string {
	lines := strings.Split(plain(a), "\n")
	return strings.Join(lines[len(lines)-2:], "\n")
}

func TestAnEmptyLyricsBoxPointsAtTheDraft(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "an idea")
	if !strings.Contains(plain(m), "empty · ctrl+g drafts from your idea · enter to write them yourself") {
		t.Fatalf("lyrics box:\n%s", plain(m))
	}
	m, _ = send(t, m, key("ctrl+s"))
	if m.screen != screenCompose || m.compose.msg != "Nothing to render yet: ctrl+g drafts lyrics and style from your idea" || m.compose.focus != rowDraft {
		t.Fatalf("ctrl+s: screen %v focus %v msg %q", m.screen, m.compose.focus, m.compose.msg)
	}
}

func TestALongIdeaIsShownWhole(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	sentence := "a slow song about a lighthouse keeper who writes letters to ships "
	for _, w := range []int{60, 80, 101, 140} {
		for n := 1; n <= 6; n++ {
			m, _ = send(t, m, tea.WindowSizeMsg{Width: w, Height: 50})
			m.compose.idea.SetValue("")
			idea := strings.TrimSpace(strings.Repeat(sentence, n))
			m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(idea), Paste: true})
			if m.compose.idea.Height() < maxIdeaLines && !strings.Contains(formText(m), idea) {
				t.Fatalf("width %d, %d sentences: the idea is not all on screen:\n%s", w, n, plain(m))
			}
		}
	}
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	m.compose.idea.SetValue("")
	idea := strings.TrimSpace(strings.Repeat(sentence, 6))
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(idea), Paste: true})
	if len(idea) < 380 || !strings.Contains(formText(m), idea) {
		t.Fatalf("the idea is not all on screen:\n%s", plain(m))
	}
	if m.compose.idea.Height() < 4 || m.compose.idea.Height() > maxIdeaLines {
		t.Fatalf("idea height %d", m.compose.idea.Height())
	}
	m = press(t, m, "enter")
	if !strings.Contains(m.compose.idea.Value(), "letters to ships") || strings.Contains(m.compose.idea.Value(), "\n") {
		t.Fatalf("enter changed the idea: %q", m.compose.idea.Value())
	}
}

func TestAFailedDraftSaysWhichModelAndWhy(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{err: errors.New("context deadline exceeded")})
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m = press(t, m, "an idea")
	m, cmd := send(t, m, key("ctrl+g"))
	m = runAll(t, m, cmd)
	want := "Draft with " + m.cfg().Ollama.Model + " failed: context deadline exceeded. ctrl+g tries again; or write the lyrics and style yourself."
	if m.compose.drafting || m.compose.tone != toneBad || !strings.Contains(formText(m), want) {
		t.Fatalf("want %q on screen:\n%s", want, plain(m))
	}
}

func TestLibraryDraftsASongThatHasOnlyAnIdea(t *testing.T) {
	d := newRecordingDrafter(sampleDraft, nil)
	m, store := newTestModel(t, d)
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	song, err := store.Create(library.Song{Title: "Lighthouse", Idea: "a lighthouse keeper's letters", Seconds: 60, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	view := formText(m)
	for _, s := range []string{"g draft lyrics and style", "not written yet · g drafts them from the idea · e to write them yourself"} {
		if !strings.Contains(view, s) {
			t.Fatalf("library missing %q:\n%s", s, plain(m))
		}
	}
	m, _ = send(t, m, key("r"))
	text := formText(m)
	if want := "Can't render yet: no lyrics and no style. g drafts them from the idea, e edits."; !strings.Contains(text, want) {
		t.Fatalf("r on an idea-only song, want %q:\n%s", want, plain(m))
	}
	m, cmd := send(t, m, key("g"))
	m = runAll(t, m, cmd)
	if m.screen != screenCompose || m.compose.base.ID != song.ID || *d.calls != 1 || m.compose.texts[0] != sampleDraft.Lyrics || m.compose.title.Value() != "Lighthouse" {
		t.Fatalf("g: screen %v calls %d lyrics %q", m.screen, *d.calls, m.compose.texts[0])
	}
}

func TestLauncherWrapsALongMessage(t *testing.T) {
	a, _ := newLauncherApp(t, readyChecks())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 40})
	a.notice = "Saved Lighthouse as a draft instead of queueing it: rendering needs ComfyUI and the music model files. Finish setup on the home screen installs it."
	if !strings.Contains(strings.Join(strings.Fields(plain(a)), " "), a.notice) {
		t.Fatalf("message cut:\n%s", plain(a))
	}
}

func cycleApp(t *testing.T) app {
	t.Helper()
	m, _ := openNewSong(t, fakeDrafter{})
	m.settings.models.list = fakeModelInfos(false,
		ollama.ModelInfo{Name: "alpha:1b"}, ollama.ModelInfo{Name: testModel}, ollama.ModelInfo{Name: "zeta:3b"},
		ollama.ModelInfo{Name: "vectors:1b", Family: "bert"})
	if err := m.d.Config.Set("ollama.model", testModel); err != nil {
		t.Fatal(err)
	}
	return runAll(t, m, m.loadModels())
}

func TestTheWriterRowCyclesAndSavesTheModel(t *testing.T) {
	m := cycleApp(t)
	chat := &recordingChat{}
	m.d.Drafter = liveWriter{m.d.Config, chat}
	m = press(t, m, "down")
	if m.compose.focus != rowWriter || !strings.Contains(plain(m), "‹ "+testModel+" ›") {
		t.Fatalf("Writer row on %v:\n%s", m.compose.focus, plain(m))
	}
	saves := recordSaves(&m)
	m = press(t, m, "right")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.model", "zeta:3b"}) || !strings.Contains(plain(m), "‹ zeta:3b ›") {
		t.Fatalf("saves %v:\n%s", *saves, plain(m))
	}
	m = press(t, m, "right")
	if got := m.cfg().Ollama.Model; got != "alpha:1b" {
		t.Fatalf("right from the last model went to %q", got)
	}
	if m.compose.dirty() {
		t.Fatal("choosing the writer made the song unsaved")
	}
	m = press(t, m, "up", "an idea")
	m, cmd := send(t, m, key("ctrl+g"))
	runAll(t, m, cmd)
	if len(chat.reqs) != 1 || chat.reqs[0].Model != "alpha:1b" {
		t.Fatalf("draft requests %+v", chat.reqs)
	}
}

func TestTheWriterRowSaysWhyWhenThereIsNoChoice(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m.settings.models.list = fakeModels()
	m = runAll(t, m, m.loadModels())
	if !strings.Contains(plain(m), "Writer   Ollama is not reachable at http://127.0.0.1:1") {
		t.Fatalf("unreachable:\n%s", plain(m))
	}
	m = press(t, m, "down")
	if m.compose.focus == rowWriter {
		t.Fatal("focus landed on a writer row with nothing to choose")
	}
}

func TestTheIdeaEditsInTheMiddleAndKeepsOneLine(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m = press(t, m, "a song about rain")
	for range 4 {
		m = press(t, m, "left")
	}
	m = press(t, m, "cold ")
	if got := m.compose.idea.Value(); got != "a song about cold rain" {
		t.Fatalf("idea %q", got)
	}
	m.compose.startEdit(library.Song{Idea: "two\nlines"}, 60)
	if got := m.compose.idea.Value(); got != "two lines" {
		t.Fatalf("a saved idea with a line break became %q", got)
	}
}
