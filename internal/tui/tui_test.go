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

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/player"
	"github.com/lancekrogers/songsmith/internal/queue"
	"github.com/lancekrogers/songsmith/internal/writer"
)

type fakeDrafter struct {
	draft writer.Draft
	err   error
}

func (f fakeDrafter) Write(context.Context, string, writer.Options) (writer.Draft, error) {
	return f.draft, f.err
}

type nopBackend struct{}

func (nopBackend) Name() string                          { return "nop" }
func (nopBackend) Abandon(context.Context, string) error { return nil }
func (nopBackend) Generate(context.Context, backend.Request, func(backend.Progress)) (backend.Result, error) {
	return backend.Result{}, nil
}

var sampleDraft = writer.Draft{
	Title:   "Sock Thief",
	Lyrics:  "[Verse]\nGone again\n[Chorus]\nWhere'd my socks go",
	Caption: writer.Caption{GlobalMetadata: "Garage rock, 150 BPM", VocalDetails: "Shouted male lead", Arrangement: "Verse: fuzz guitar."},
	Seconds: 45,
}

// testConfig keeps every path in a temp dir, so nothing reaches a real install.
func testConfig(t *testing.T) *config.Live {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.LibraryDir = filepath.Join(dir, "library")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.ComfyUI.Dir = filepath.Join(dir, "comfyui")
	cfg.ComfyUI.Port = 18188
	return config.NewLive(filepath.Join(dir, "config.yaml"), cfg)
}

func testDeps(t *testing.T, store *library.Store, q *queue.Queue, d Drafter) Deps {
	return Deps{
		Store: store, Queue: q, Drafter: d, Player: player.New(), Config: testConfig(t),
		Checks: func(context.Context, config.Config) []health.Check { return readyChecks() },
	}
}

func newTestApp(t *testing.T, deps Deps, updates <-chan queue.Update) app {
	a := newApp(t.Context(), deps, updates)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 120, Height: 40})
	return a
}

// newTestModel opens the library screen, where the song tests start.
func newTestModel(t *testing.T, d Drafter) (app, *library.Store) {
	store := library.NewStore(t.TempDir())
	q := queue.New(store, nopBackend{})
	updates, stop := q.Subscribe()
	t.Cleanup(stop)
	a := newTestApp(t, testDeps(t, store, q, d), updates)
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	return a, store
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+e":
		return tea.KeyMsg{Type: tea.KeyCtrlE}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(t *testing.T, a app, msgs ...tea.Msg) (app, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = a.Update(msg)
		a = next.(app)
	}
	return a, cmd
}

// nav presses k and delivers the screen-switch message it returns.
func nav(t *testing.T, a app, k string) app {
	t.Helper()
	a, cmd := send(t, a, key(k))
	if cmd == nil {
		t.Fatalf("%s: no command", k)
	}
	msg := cmd()
	switch msg.(type) {
	case switchScreenMsg, openComposeMsg, openLibraryMsg:
	default:
		t.Fatalf("%s: got %T, want a screen switch", k, msg)
	}
	a, _ = send(t, a, msg)
	return a
}

// runCmd executes cmd and feeds the messages it produces back into a,
// skipping ticks and blocking waits.
func runCmd(t *testing.T, a app, cmd tea.Cmd) app {
	t.Helper()
	if cmd == nil {
		return a
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if r := c(); r != nil {
				if _, isDraft := r.(draftDoneMsg); isDraft {
					a, _ = send(t, a, r)
				}
				if _, isSongs := r.(songsMsg); isSongs {
					a, _ = send(t, a, r)
				}
			}
		}
		return a
	}
	if msg != nil {
		a, _ = send(t, a, msg)
	}
	return a
}

func plain(a app) string { return ansi.Strip(a.View()) }

func TestComposeDraftAndQueue(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{draft: sampleDraft})
	m = nav(t, m, "n")
	if m.screen != screenCompose || m.compose.focus != rowIdea || m.compose.base.ID != "" {
		t.Fatalf("screen %v focus %v", m.screen, m.compose.focus)
	}
	m, _ = send(t, m, key("a cat who steals socks"))
	m, cmd := send(t, m, key("enter"))
	if !m.compose.drafting {
		t.Fatal("not drafting")
	}
	m = runCmd(t, m, cmd)
	if m.compose.drafting || m.compose.title.Value() != "Sock Thief" || m.compose.texts[2] != "Shouted male lead" {
		t.Fatalf("title %q vocal details %q", m.compose.title.Value(), m.compose.texts[2])
	}
	if v := m.View(); !strings.Contains(v, "ctrl+s save & queue") {
		t.Fatalf("edit view missing help:\n%s", v)
	}

	m, cmd = send(t, m, key("ctrl+s"))
	if m.screen != screenLibrary {
		t.Fatalf("screen after save = %v (msg %q)", m.screen, m.compose.msg)
	}
	m = runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 1 || songs[0].Status != library.StatusQueued || songs[0].Idea != "a cat who steals socks" || songs[0].Seconds != 45 {
		t.Fatalf("songs = %+v", songs)
	}
	if len(m.library.songs) != 1 || !strings.Contains(m.View(), "Sock Thief") {
		t.Fatalf("library view:\n%s", m.View())
	}
}

func TestDraftFailureFallsBackToManualEdit(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{err: errors.New("connection refused")})
	m = nav(t, m, "n")
	m, _ = send(t, m, key("idea"))
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.compose.drafting || !strings.Contains(m.compose.msg, "connection refused") {
		t.Fatalf("drafting %v msg %q", m.compose.drafting, m.compose.msg)
	}
	m, _ = send(t, m, key("ctrl+s"))
	if m.screen != screenCompose || !strings.Contains(m.compose.msg, "lyrics are empty") {
		t.Fatalf("empty form should not save: screen %v msg %q", m.screen, m.compose.msg)
	}
	if songs, _ := store.List(); len(songs) != 0 {
		t.Fatalf("saved %d songs", len(songs))
	}
}

func TestEditingDoneSongMakesACopy(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	done, err := store.Create(library.Song{Title: "Original", Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption, Seconds: 60, Status: library.StatusDone, Seed: 9, Duration: 58})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.AudioPath(done), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	if m.screen != screenCompose || m.compose.base.ID != "" {
		t.Fatalf("screen %v base %q", m.screen, m.compose.base.ID)
	}
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 2 {
		t.Fatalf("songs = %d", len(songs))
	}
	orig, _ := store.Load(done.ID)
	if orig.Status != library.StatusDone || orig.Duration != 58 {
		t.Fatalf("original changed: %+v", orig)
	}
}

func TestLibraryKeysOnEmptyLibrary(t *testing.T) {
	m, _ := newTestModel(t, fakeDrafter{})
	for _, k := range []string{"enter", "e", "r", "x", "j", "k", "q"} {
		m, _ = send(t, m, key(k))
	}
	if m.screen != screenLibrary {
		t.Fatalf("q left the library: screen %v", m.screen)
	}
	if !strings.Contains(plain(m), "Press n to write one") {
		t.Fatalf("view:\n%s", m.View())
	}
	if _, cmd := send(t, m, key("ctrl+c")); cmd == nil || !isQuit(cmd) {
		t.Fatal("ctrl+c should quit from the library")
	}
	m = nav(t, m, "esc")
	if m.screen != screenLauncher {
		t.Fatalf("esc went to %v", m.screen)
	}
	_, cmd := send(t, m, key("q"))
	if cmd == nil {
		t.Fatal("q should quit from the launcher")
	}
	if _, cmd := send(t, m, cmd()); !isQuit(cmd) {
		t.Fatal("quit message did not quit")
	}
}

func isQuit(cmd tea.Cmd) bool {
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestWideTitlesFitTheList(t *testing.T) {
	for _, width := range []int{80, 120, 200} {
		m, store := newTestModel(t, fakeDrafter{})
		m, _ = send(t, m, tea.WindowSizeMsg{Width: width, Height: 30})
		if _, err := store.Create(library.Song{Title: strings.Repeat("雨の日曜日リスボン", 4), Status: library.StatusDone, Duration: 61}); err != nil {
			t.Fatal(err)
		}
		m = runCmd(t, m, m.loadSongs())
		view := m.View()
		for _, line := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("width %d: line is %d wide: %q", width, w, line)
			}
		}
		if strings.ContainsRune(view, 0) {
			t.Fatalf("width %d: NUL in view", width)
		}
	}
}

func TestSelectionSurvivesStaleReload(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{draft: sampleDraft})
	old, _ := store.Create(library.Song{Title: "Old", Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption, Seconds: 30})
	m = runCmd(t, m, m.loadSongs())
	stale := m.loadSongs()()

	m = nav(t, m, "n")
	m, _ = send(t, m, key("idea"))
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	m, cmd = send(t, m, key("ctrl+d"))
	m, _ = send(t, m, stale)
	if s, _ := m.selected(); s.ID != old.ID && m.library.selectID == "" {
		t.Fatal("stale reload consumed the pending selection")
	}
	m = runCmd(t, m, cmd)
	if s, _ := m.selected(); s.Title != "Sock Thief" {
		t.Fatalf("selected %q after save", s.Title)
	}
}

type blockingBackend struct{ started chan struct{} }

func (blockingBackend) Name() string                          { return "block" }
func (blockingBackend) Abandon(context.Context, string) error { return nil }
func (b blockingBackend) Generate(ctx context.Context, _ backend.Request, _ func(backend.Progress)) (backend.Result, error) {
	close(b.started)
	<-ctx.Done()
	return backend.Result{}, ctx.Err()
}

func TestSavingEditsToASongThatStartedRenderingMakesACopy(t *testing.T) {
	store := library.NewStore(t.TempDir())
	bb := blockingBackend{started: make(chan struct{})}
	q := queue.New(store, bb)
	updates, stop := q.Subscribe()
	defer stop()
	m := newTestApp(t, testDeps(t, store, q, fakeDrafter{}), updates)
	m, _ = send(t, m, switchScreenMsg(screenLibrary))

	song, _ := store.Create(library.Song{Title: "Queued", Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption, Seconds: 30, Status: library.StatusQueued})
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	if m.compose.base.ID != song.ID {
		t.Fatal("queued song should be edited in place")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { q.Render(ctx, song); close(done) }()
	<-bb.started

	m.compose.texts[0] = "[Verse]\nnew words"
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	if !strings.Contains(m.notice, "saved as a new song") {
		t.Fatalf("notice = %q msg = %q", m.notice, m.compose.msg)
	}
	songs, _ := store.List()
	if len(songs) != 2 {
		t.Fatalf("songs = %d", len(songs))
	}
	cancel()
	<-done
}

func TestEditorCommand(t *testing.T) {
	cases := []struct{ visual, editor, want string }{
		{"   ", "", "vi"},
		{"", "nvim -u NONE", "nvim -u NONE /tmp/x"},
		{"code --wait", "nvim", "code --wait /tmp/x"},
	}
	for _, c := range cases {
		t.Setenv("VISUAL", c.visual)
		t.Setenv("EDITOR", c.editor)
		got := strings.Join(editorCommand("/tmp/x").Args, " ")
		if c.want == "vi" {
			c.want = "vi /tmp/x"
		}
		if got != c.want {
			t.Errorf("VISUAL=%q EDITOR=%q: %q, want %q", c.visual, c.editor, got, c.want)
		}
	}
}

func TestNewSongsUseConfiguredDefaultLength(t *testing.T) {
	m, _ := newTestModel(t, fakeDrafter{})
	cfg := m.d.Config.Get()
	cfg.Render.DefaultSeconds = 75
	m.d.Config.Replace(cfg)
	m = nav(t, m, "n")
	m, _ = send(t, m, key("tab"))
	if m.compose.focus != rowTitle || m.compose.seconds != 75 {
		t.Fatalf("focus %v seconds %v", m.compose.focus, m.compose.seconds)
	}
	m, _ = send(t, m, key("esc"))
	if m.screen != screenLibrary {
		t.Fatalf("esc from the form went to %v, want the library that opened it", m.screen)
	}
}
