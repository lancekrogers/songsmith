package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

type fakeBackend struct {
	mu        sync.Mutex
	calls     []backend.Request
	err       error
	started   chan string
	block     bool
	jobID     string
	abandoned []string
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) Abandon(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abandoned = append(f.abandoned, jobID)
	return nil
}

func (f *fakeBackend) Generate(ctx context.Context, req backend.Request, progress func(backend.Progress)) (backend.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.jobID != "" && req.OnStarted != nil {
		req.OnStarted(f.jobID)
	}
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
	q.poll = 50 * time.Millisecond
	return q, store
}

func create(t *testing.T, store *library.Store, title string, status library.Status) library.Song {
	s, err := store.Create(library.Song{Title: title, Lyrics: "[Verse]\nla", Seconds: 30, Status: status,
		Caption: writer.Caption{GlobalMetadata: "Folk", VocalDetails: "Soft lead", Arrangement: "Verse: guitar."}})
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

func TestShutdownKeepsJobForResume(t *testing.T) {
	b := &fakeBackend{block: true, started: make(chan string, 2), jobID: "prompt-1"}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan library.Song)
	go func() { result <- q.Render(ctx, song) }()
	<-b.started
	if s, _ := store.Load(song.ID); s.JobID != "prompt-1" || s.Status != library.StatusRendering {
		t.Fatalf("job id not saved while rendering: %+v", s)
	}
	cancel()
	got := <-result
	if got.Status != library.StatusQueued || got.JobID != "prompt-1" {
		t.Fatalf("after shutdown: %+v", got)
	}
	b.block = false
	q.Render(t.Context(), got)
	if b.calls[1].ResumeID != "prompt-1" {
		t.Fatalf("resume id = %q", b.calls[1].ResumeID)
	}
	if s, _ := store.Load(song.ID); s.Status != library.StatusDone || s.JobID != "" {
		t.Fatalf("after resume: %+v", s)
	}
}

func TestRerenderKeepsPreviousTake(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	if song.Audio != "song.flac" {
		t.Fatalf("first take = %q", song.Audio)
	}

	b.err = errors.New("MPS out of memory")
	song, _ = q.Enqueue(song)
	got := q.Render(t.Context(), song)
	if got.Status != library.StatusDone || got.Audio != "song.flac" || !strings.Contains(got.Error, "kept the previous take") {
		t.Fatalf("failed re-render: %+v", got)
	}

	b.err = nil
	got, _ = q.Enqueue(got)
	got = q.Render(t.Context(), got)
	if got.Audio != "song-2.flac" || !store.HasAudio(got) {
		t.Fatalf("second take = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), got.ID, "song.flac")); err != nil {
		t.Fatal("first take was removed")
	}
}

func TestCancelQueuedRerenderStaysDone(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	song := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	song, _ = q.Enqueue(song)
	if err := q.Cancel(song.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Load(song.ID); s.Status != library.StatusDone {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestRenderSkipsSongNoLongerQueued(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	listed := song
	if err := q.Cancel(song.ID); err != nil {
		t.Fatal(err)
	}
	got := q.Render(t.Context(), listed)
	if got.Status != library.StatusDraft || len(b.calls) != 0 {
		t.Fatalf("rendered a cancelled song: %+v calls %d", got, len(b.calls))
	}
}

func TestSaveEditRefusesRenderingSong(t *testing.T) {
	b := &fakeBackend{block: true, started: make(chan string, 1)}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	done := make(chan library.Song)
	go func() { done <- q.Render(t.Context(), song) }()
	<-b.started
	song.Lyrics = "[Verse]\nedited"
	if err := q.SaveEdit(song); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	if _, err := q.Enqueue(song); !errors.Is(err, ErrBusy) {
		t.Fatalf("enqueue err = %v", err)
	}
	q.Cancel(song.ID)
	<-done
}

func TestRunPicksUpSongsQueuedElsewhere(t *testing.T) {
	b := &fakeBackend{started: make(chan string, 1)}
	q, store := setup(t, b)
	runQueue(t, q)
	time.Sleep(20 * time.Millisecond)
	song := create(t, store, "From another process", library.StatusQueued)
	select {
	case id := <-b.started:
		if id != song.ID {
			t.Fatalf("rendered %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not poll for new songs")
	}
}

func TestRunSurvivesCorruptSong(t *testing.T) {
	b := &fakeBackend{started: make(chan string, 1)}
	q, store := setup(t, b)
	os.MkdirAll(filepath.Join(store.Dir(), "bad"), 0o755)
	os.WriteFile(filepath.Join(store.Dir(), "bad", "song.json"), []byte("{"), 0o644)
	song := create(t, store, "Good", library.StatusQueued)
	runQueue(t, q)
	select {
	case id := <-b.started:
		if id != song.ID {
			t.Fatalf("rendered %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a corrupt song stopped the worker")
	}
}

// runQueue runs q until the test ends and waits for the worker to stop, so
// it never writes into a temp dir that is being removed.
func runQueue(t *testing.T, q *Queue) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestEditingDetachedSongDropsStaleJob(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	song.JobID = "old-prompt"
	store.Save(song)

	song.Lyrics = "[Verse]\nnew words"
	if err := q.SaveEdit(song); err != nil {
		t.Fatal(err)
	}
	q.Render(t.Context(), song)
	if got := b.calls[0].ResumeID; got != "" {
		t.Fatalf("edited song resumed stale job %q", got)
	}
	if len(b.abandoned) != 1 || b.abandoned[0] != "old-prompt" {
		t.Fatalf("abandoned = %v", b.abandoned)
	}
}

func TestTitleOnlyEditKeepsDetachedJob(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	song.JobID = "prompt-1"
	store.Save(song)
	song.Title = "Renamed"
	if err := q.SaveEdit(song); err != nil {
		t.Fatal(err)
	}
	q.Render(t.Context(), song)
	if b.calls[0].ResumeID != "prompt-1" || len(b.abandoned) != 0 {
		t.Fatalf("resume %q abandoned %v", b.calls[0].ResumeID, b.abandoned)
	}
}

func TestCancelDetachedSongAbandonsJob(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	song.JobID = "prompt-1"
	store.Save(song)
	if err := q.Cancel(song.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Load(song.ID)
	if got.JobID != "" || len(b.abandoned) != 1 {
		t.Fatalf("job %q abandoned %v", got.JobID, b.abandoned)
	}
}

func TestEnqueueRefusesASongThatCannotRender(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	song, err := store.Create(library.Song{Title: "Half", Lyrics: "[Verse]\nla", Seconds: 30, Status: library.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	var fe *writer.FieldError
	if _, err := q.Enqueue(song); !errors.As(err, &fe) {
		t.Fatalf("enqueue an incomplete song: %v", err)
	}
	if got, _ := store.Load(song.ID); got.Status != library.StatusDraft {
		t.Fatalf("status %s", got.Status)
	}
}

func TestEnqueueDrawsAFreshSeedForEachNewRenderOfAnUnpinnedSong(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusDraft)
	song.Seed = 1234
	queued, _ := q.Enqueue(song)
	first := queued.Seed
	if first == 0 || first == 1234 {
		t.Fatalf("first render seed %d", first)
	}
	done := q.Render(t.Context(), queued)
	again, _ := q.Enqueue(done)
	if again.Seed == first {
		t.Fatal("a new take of an unpinned song reused its seed")
	}
	b.err = errors.New("boom")
	failed := q.Render(t.Context(), create(t, store, "Other", library.StatusQueued))
	if failed.Status != library.StatusFailed {
		t.Fatalf("status %s", failed.Status)
	}
	retry, _ := q.Enqueue(failed)
	if retry.Seed == failed.Seed {
		t.Fatal("a retry after a failure reused the seed")
	}
}

func TestEnqueueKeepsAPinnedSeed(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	song := create(t, store, "Song", library.StatusDraft)
	song.Seed, song.SeedPinned = 0, true
	got, _ := q.Enqueue(song)
	if got.Seed != 0 {
		t.Fatalf("a pinned seed of 0 became %d", got.Seed)
	}
	if got = q.Render(t.Context(), got); got.Seed != 0 {
		t.Fatalf("rendered with seed %d", got.Seed)
	}
	song = got
	song.Seed = 42
	got, _ = q.Enqueue(song)
	if got = q.Render(t.Context(), got); got.Seed != 42 {
		t.Fatalf("a new take of a pinned song used seed %d", got.Seed)
	}
}

func TestEnqueueKeepsTheSeedOfADetachedRenderItResumes(t *testing.T) {
	b := &fakeBackend{}
	q, store := setup(t, b)
	song := create(t, store, "Song", library.StatusQueued)
	song.Seed, song.JobID = 77, "prompt-1"
	store.Save(song)
	// Saved as a draft by a title-only edit, then queued again.
	song.Title, song.Status = "Renamed", library.StatusDraft
	if err := q.SaveEdit(song); err != nil {
		t.Fatal(err)
	}
	got, err := q.Enqueue(song)
	if err != nil || got.Seed != 77 || got.JobID != "prompt-1" {
		t.Fatalf("enqueue: %v %+v", err, got)
	}
	q.Render(t.Context(), got)
	if b.calls[0].ResumeID != "prompt-1" || b.calls[0].Seed != 77 || len(b.abandoned) != 0 {
		t.Fatalf("resume %q seed %d abandoned %v", b.calls[0].ResumeID, b.calls[0].Seed, b.abandoned)
	}
}
