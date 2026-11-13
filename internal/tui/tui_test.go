package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/backend"
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

func (nopBackend) Name() string { return "nop" }
func (nopBackend) Generate(context.Context, backend.Request, func(backend.Progress)) (backend.Result, error) {
	return backend.Result{}, nil
}

var sampleDraft = writer.Draft{
	Title:   "Sock Thief",
	Lyrics:  "[Verse]\nGone again\n[Chorus]\nWhere'd my socks go",
	Caption: writer.Caption{GlobalMetadata: "Garage rock, 150 BPM", VocalDetails: "Shouted male lead", Arrangement: "Verse: fuzz guitar."},
	Seconds: 45,
}

func newTestModel(t *testing.T, d Drafter) (model, *library.Store) {
	store := library.NewStore(t.TempDir())
	q := queue.New(store, nopBackend{})
	updates, stop := q.Subscribe()
	t.Cleanup(stop)
	m := newModel(t.Context(), Deps{Store: store, Queue: q, Drafter: d, Player: player.New()}, updates)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return next.(model), store
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(t *testing.T, m model, msgs ...tea.Msg) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(model)
	}
	return m, cmd
}

// runCmd executes cmd and feeds the messages it produces back into m,
// skipping ticks and blocking waits.
func runCmd(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if r := c(); r != nil {
				if _, isDraft := r.(draftDoneMsg); isDraft {
					m, _ = send(t, m, r)
				}
				if _, isSongs := r.(songsMsg); isSongs {
					m, _ = send(t, m, r)
				}
			}
		}
		return m
	}
	if msg != nil {
		m, _ = send(t, m, msg)
	}
	return m
}

func TestComposeDraftAndQueue(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{draft: sampleDraft})
	m, _ = send(t, m, key("n"))
	if m.mode != modeIdea {
		t.Fatalf("mode = %v", m.mode)
	}
	m, _ = send(t, m, key("a cat who steals socks"))
	m, cmd := send(t, m, key("enter"))
	if !m.compose.drafting {
		t.Fatal("not drafting")
	}
	m = runCmd(t, m, cmd)
	if m.mode != modeEdit || m.compose.title.Value() != "Sock Thief" || !strings.Contains(m.compose.caption.Value(), "Vocal Details: Shouted") {
		t.Fatalf("mode %v title %q", m.mode, m.compose.title.Value())
	}
	if v := m.View(); !strings.Contains(v, "ctrl+s save & queue") {
		t.Fatalf("edit view missing help:\n%s", v)
	}

	m, cmd = send(t, m, key("ctrl+s"))
	if m.mode != modeLibrary {
		t.Fatalf("mode after save = %v (err %q)", m.mode, m.compose.err)
	}
	m = runCmd(t, m, cmd)
	songs, _ := store.List()
	if len(songs) != 1 || songs[0].Status != library.StatusQueued || songs[0].Idea != "a cat who steals socks" || songs[0].Seconds != 45 {
		t.Fatalf("songs = %+v", songs)
	}
	if len(m.songs) != 1 || !strings.Contains(m.View(), "Sock Thief") {
		t.Fatalf("library view:\n%s", m.View())
	}
}

func TestDraftFailureFallsBackToManualEdit(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{err: errors.New("connection refused")})
	m, _ = send(t, m, key("n"), key("idea"))
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.mode != modeEdit || !strings.Contains(m.compose.err, "connection refused") {
		t.Fatalf("mode %v err %q", m.mode, m.compose.err)
	}
	m, _ = send(t, m, key("ctrl+s"))
	if m.mode != modeEdit || !strings.Contains(m.compose.err, "lyrics are empty") {
		t.Fatalf("empty form should not save: mode %v err %q", m.mode, m.compose.err)
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
	m, _ = send(t, m, key("e"))
	if m.mode != modeEdit || m.compose.base.ID != "" {
		t.Fatalf("mode %v base %q", m.mode, m.compose.base.ID)
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
	for _, k := range []string{"enter", "e", "r", "x", "j", "k"} {
		m, _ = send(t, m, key(k))
	}
	if !strings.Contains(m.View(), "Press n to write one") {
		t.Fatalf("view:\n%s", m.View())
	}
	_, cmd := send(t, m, key("q"))
	if cmd == nil {
		t.Fatal("q should quit")
	}
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

	m, _ = send(t, m, key("n"), key("idea"))
	m, cmd := send(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	m, cmd = send(t, m, key("ctrl+d"))
	m, _ = send(t, m, stale)
	if s, _ := m.selected(); s.ID != old.ID && m.selectID == "" {
		t.Fatal("stale reload consumed the pending selection")
	}
	m = runCmd(t, m, cmd)
	if s, _ := m.selected(); s.Title != "Sock Thief" {
		t.Fatalf("selected %q after save", s.Title)
	}
}

type blockingBackend struct{ started chan struct{} }

func (blockingBackend) Name() string { return "block" }
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
	m := newModel(t.Context(), Deps{Store: store, Queue: q, Drafter: fakeDrafter{}, Player: player.New()}, updates)
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	song, _ := store.Create(library.Song{Title: "Queued", Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption, Seconds: 30, Status: library.StatusQueued})
	m = runCmd(t, m, m.loadSongs())
	m, _ = send(t, m, key("e"))
	if m.compose.base.ID != song.ID {
		t.Fatal("queued song should be edited in place")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { q.Render(ctx, song); close(done) }()
	<-bb.started

	m.compose.lyrics.SetValue("[Verse]\nnew words")
	m, cmd := send(t, m, key("ctrl+d"))
	runCmd(t, m, cmd)
	if !strings.Contains(m.notice, "saved as a new song") {
		t.Fatalf("notice = %q err = %q", m.notice, m.compose.err)
	}
	songs, _ := store.List()
	if len(songs) != 2 {
		t.Fatalf("songs = %d", len(songs))
	}
	cancel()
	<-done
}
