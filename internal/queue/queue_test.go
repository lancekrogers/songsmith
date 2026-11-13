package queue

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/library"
)

type fakeBackend struct {
	mu      sync.Mutex
	calls   []backend.Request
	err     error
	started chan string
	block   bool
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) Generate(ctx context.Context, req backend.Request, progress func(backend.Progress)) (backend.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- req.ID
	}
	progress(backend.Progress{Stage: backend.StageComposing, Value: 1, Max: 2})
	if f.block {
		<-ctx.Done()
		return backend.Result{}, ctx.Err()
	}
	if f.err != nil {
		return backend.Result{}, f.err
	}
	if err := os.WriteFile(req.OutPath, []byte("audio"), 0o644); err != nil {
		return backend.Result{}, err
	}
	return backend.Result{AudioPath: req.OutPath, Timings: map[backend.Stage]time.Duration{backend.StageComposing: 1500 * time.Millisecond}}, nil
}

func setup(t *testing.T, b *fakeBackend) (*Queue, *library.Store) {
	store := library.NewStore(t.TempDir())
	q := New(store, b)
	q.probe = func(string) float64 { return 61.2 }
	return q, store
}

func create(t *testing.T, store *library.Store, title string, status library.Status) library.Song {
	s, err := store.Create(library.Song{Title: title, Lyrics: "[Verse]\nla", Seconds: 30, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenderSuccess(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	updates, stop := q.Subscribe()
	defer stop()

	got := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	if got.Status != library.StatusDone || got.Duration != 61.2 || got.Timings["composing"] != 1.5 || got.Seed == 0 {
		t.Fatalf("song = %+v", got)
	}
	saved, _ := store.Load(got.ID)
	if saved.Status != library.StatusDone || saved.Backend != "fake" {
		t.Fatalf("saved = %+v", saved)
	}
	var statuses []library.Status
	for len(updates) > 0 {
		statuses = append(statuses, (<-updates).Status)
	}
	if len(statuses) != 3 || statuses[0] != library.StatusRendering || statuses[2] != library.StatusDone {
		t.Fatalf("updates = %v", statuses)
	}
}

func TestRenderFailureRecordsError(t *testing.T) {
	q, store := setup(t, &fakeBackend{err: errors.New("MPS out of memory")})
	got := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	if got.Status != library.StatusFailed || got.Error != "MPS out of memory" {
		t.Fatalf("song = %+v", got)
	}
}

func TestRunProcessesQueueInOrderAndSkipsDrafts(t *testing.T) {
	b := &fakeBackend{started: make(chan string, 10)}
	q, store := setup(t, b)
	first := create(t, store, "First", library.StatusQueued)
	time.Sleep(5 * time.Millisecond)
	second := create(t, store, "Second", library.StatusQueued)
	create(t, store, "Draft", library.StatusDraft)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- q.Run(ctx) }()
	if id := <-b.started; id != first.ID {
		t.Fatalf("first render = %s", id)
	}
	if id := <-b.started; id != second.ID {
		t.Fatalf("second render = %s", id)
	}

	third := create(t, store, "Third", library.StatusDraft)
	if _, err := q.Enqueue(third); err != nil {
		t.Fatal(err)
	}
	if id := <-b.started; id != third.ID {
		t.Fatalf("enqueued render = %s", id)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 3 {
		t.Fatalf("calls = %d", len(b.calls))
	}
}

func TestCancelRendering(t *testing.T) {
	b := &fakeBackend{block: true, started: make(chan string, 1)}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	result := make(chan library.Song)
	go func() { result <- q.Render(t.Context(), song) }()
	<-b.started
	if q.Current() != song.ID {
		t.Fatalf("current = %q", q.Current())
	}
	if err := q.Cancel(song.ID); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.Status != library.StatusFailed || got.Error != "cancelled" {
		t.Fatalf("song = %+v", got)
	}
}

func TestShutdownLeavesSongQueued(t *testing.T) {
	b := &fakeBackend{block: true, started: make(chan string, 1)}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan library.Song)
	go func() { result <- q.Render(ctx, song) }()
	<-b.started
	cancel()
	if got := <-result; got.Status != library.StatusQueued {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestCancelQueuedReturnsToDraft(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	song := create(t, store, "Song", library.StatusQueued)
	if err := q.Cancel(song.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Load(song.ID)
	if got.Status != library.StatusDraft {
		t.Fatalf("status = %s", got.Status)
	}
	if err := q.Cancel(song.ID); err == nil {
		t.Fatal("cancelling a draft should fail")
	}
}

func TestRecoverRequeuesRendering(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	stuck := create(t, store, "Stuck", library.StatusRendering)
	done := create(t, store, "Done", library.StatusDone)
	if err := q.Recover(); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Load(stuck.ID); s.Status != library.StatusQueued {
		t.Fatalf("stuck = %s", s.Status)
	}
	if s, _ := store.Load(done.ID); s.Status != library.StatusDone {
		t.Fatalf("done = %s", s.Status)
	}
}
