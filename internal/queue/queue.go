// Package queue renders queued songs one at a time in the background.
package queue

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/library"
)

// Update is published whenever a song's status or render progress changes.
type Update struct {
	SongID   string
	Status   library.Status
	Progress backend.Progress
	Err      string
}

// PollInterval is how often an idle worker looks for songs queued by other
// processes.
const PollInterval = 5 * time.Second

type Queue struct {
	store   *library.Store
	backend backend.Backend
	wake    chan struct{}
	now     func() time.Time
	probe   func(path string) float64
	poll    time.Duration

	// mu guards current and cancel, and serializes the status changes that
	// race with a render being claimed: claim, cancel, enqueue and edit saves.
	mu      sync.Mutex
	current string
	cancel  context.CancelCauseFunc

	subMu   sync.Mutex
	subs    map[int]chan Update
	nextSub int
}

func New(store *library.Store, b backend.Backend) *Queue {
	return &Queue{
		store:   store,
		backend: b,
		wake:    make(chan struct{}, 1),
		now:     time.Now,
		probe:   probeDuration,
		poll:    PollInterval,
		subs:    map[int]chan Update{},
	}
}

var (
	// ErrCancelled is the cause recorded when the user cancels a render.
	ErrCancelled = errors.New("cancelled")
	// ErrBusy means the song is rendering and cannot be changed in place.
	ErrBusy = errors.New("song is rendering")
)

// Subscribe returns a channel of updates and a function that stops them.
// Slow subscribers miss progress updates rather than stalling renders.
func (q *Queue) Subscribe() (<-chan Update, func()) {
	q.subMu.Lock()
	defer q.subMu.Unlock()
	id := q.nextSub
	q.nextSub++
	ch := make(chan Update, 64)
	q.subs[id] = ch
	return ch, func() {
		q.subMu.Lock()
		defer q.subMu.Unlock()
		if c, ok := q.subs[id]; ok {
			delete(q.subs, id)
			close(c)
		}
	}
}

func (q *Queue) publish(u Update) {
	q.subMu.Lock()
	defer q.subMu.Unlock()
	for _, ch := range q.subs {
		select {
		case ch <- u:
		default:
		}
	}
}

// Current returns the ID of the song this process is rendering, if any.
func (q *Queue) Current() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.current
}

// busy reports whether id is rendering here or in another process. Call with q.mu held.
func (q *Queue) busy(id string) bool {
	if id == "" {
		return false
	}
	if q.current == id {
		return true
	}
	stored, err := q.store.Load(id)
	return err == nil && stored.Status == library.StatusRendering
}

// renderInputsChanged reports whether a render of a and b would differ.
func renderInputsChanged(a, b library.Song) bool {
	return a.Lyrics != b.Lyrics || a.Caption != b.Caption || a.Seed != b.Seed || a.Seconds != b.Seconds
}

// dropStaleJob clears song's detached job when its render inputs changed, and
// stops that job in the backend. Call with q.mu held.
func (q *Queue) dropStaleJob(song *library.Song, force bool) {
	stored, err := q.store.Load(song.ID)
	if err != nil || stored.JobID == "" {
		return
	}
	if !force && !renderInputsChanged(stored, *song) && song.JobID == stored.JobID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = q.backend.Abandon(ctx, stored.JobID)
	song.JobID = ""
}

// SaveEdit saves changes to an existing song unless it is rendering.
func (q *Queue) SaveEdit(song library.Song) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy(song.ID) {
		return ErrBusy
	}
	q.dropStaleJob(&song, false)
	if err := q.store.Save(song); err != nil {
		return err
	}
	q.publish(Update{SongID: song.ID, Status: song.Status})
	return nil
}

// Enqueue marks a song queued and wakes the worker. A song that cannot
// render is refused with its library.Song.CheckRender error. An unpinned
// seed is drawn afresh unless the song resumes a detached render.
func (q *Queue) Enqueue(song library.Song) (library.Song, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy(song.ID) {
		return song, ErrBusy
	}
	if err := song.CheckRender(); err != nil {
		return song, err
	}
	if !song.SeedPinned && !q.resumes(song) {
		song.Seed = newSeed()
	}
	q.dropStaleJob(&song, false)
	song.Status = library.StatusQueued
	song.Error = ""
	if err := q.store.Save(song); err != nil {
		return song, err
	}
	q.publish(Update{SongID: song.ID, Status: library.StatusQueued})
	q.Wake()
	return song, nil
}

// resumes reports whether song still matches the detached render its stored
// copy is waiting on, so queueing it picks that render up again. Call with
// q.mu held.
func (q *Queue) resumes(song library.Song) bool {
	if song.ID == "" || song.JobID == "" {
		return false
	}
	stored, err := q.store.Load(song.ID)
	return err == nil && stored.JobID == song.JobID && !renderInputsChanged(stored, song)
}

func newSeed() int64 { return rand.Int64N(1 << 48) }

// Wake makes an idle worker look for queued songs now.
func (q *Queue) Wake() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a rendering song or takes a queued one off the queue.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == id && q.cancel != nil {
		q.cancel(ErrCancelled)
		return nil
	}
	song, err := q.store.Load(id)
	if err != nil {
		return err
	}
	switch song.Status {
	case library.StatusQueued:
	case library.StatusRendering:
		return errors.New("this song is rendering in another songsmith process; cancel it there")
	default:
		return fmt.Errorf("%s is %s, not queued or rendering", song.Title, song.Status)
	}
	q.dropStaleJob(&song, true)
	song.Status = library.StatusDraft
	if q.store.HasAudio(song) {
		song.Status = library.StatusDone
	}
	if err := q.store.Save(song); err != nil {
		return err
	}
	q.publish(Update{SongID: id, Status: song.Status})
	return nil
}

// Recover returns songs left "rendering" by a crashed or killed process to
// the queue, keeping their job ID so the render can be resumed. Call it only
// while holding the library render lock. Unreadable songs are skipped.
func (q *Queue) Recover() error {
	songs, _ := q.store.List()
	var errs []error
	for _, s := range songs {
		if s.Status != library.StatusRendering {
			continue
		}
		s.Status = library.StatusQueued
		if err := q.store.Save(s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run renders queued songs until ctx ends. Hold the library render lock while
// it runs. Songs queued by other processes are picked up within PollInterval.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.Recover(); err != nil {
		q.publish(Update{Err: "recover queue: " + err.Error()})
	}
	for {
		song, ok := q.next()
		if !ok {
			select {
			case <-ctx.Done():
				return nil
			case <-q.wake:
			case <-time.After(q.poll):
			}
			continue
		}
		q.Render(ctx, song)
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (q *Queue) next() (library.Song, bool) {
	songs, _ := q.store.List()
	queued := slices.DeleteFunc(songs, func(s library.Song) bool { return s.Status != library.StatusQueued })
	if len(queued) == 0 {
		return library.Song{}, false
	}
	return slices.MinFunc(queued, func(a, b library.Song) int { return a.Updated.Compare(b.Updated) }), true
}

// claim marks song as rendering if it is still queued, reading the stored
// copy so edits and cancels made since it was listed win.
func (q *Queue) claim(song library.Song, cancel context.CancelCauseFunc) (library.Song, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if stored, err := q.store.Load(song.ID); err == nil {
		song = stored
	}
	if song.Status != library.StatusQueued {
		return song, false
	}
	q.current, q.cancel = song.ID, cancel
	song.Status = library.StatusRendering
	song.Error = ""
	song.Started = q.now()
	song.Finished = time.Time{}
	song.Backend = q.backend.Name()
	if song.Seed == 0 && !song.SeedPinned {
		song.Seed = newSeed()
	}
	q.save(song)
	return song, true
}

// Render renders one queued song now and records the outcome in the library.
// A song that is no longer queued is returned unchanged.
func (q *Queue) Render(ctx context.Context, song library.Song) library.Song {
	// Shutting down the caller detaches from the render instead of stopping
	// it, so the backend keeps working and the next run picks up the result.
	parent := ctx
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	defer cancel(nil)
	stopDetach := context.AfterFunc(parent, func() { cancel(backend.ErrDetach) })
	defer stopDetach()
	song, ok := q.claim(song, cancel)
	if !ok {
		return song
	}

	hadTake := q.store.HasAudio(song)
	take := q.store.NextTake(song)
	out := song
	out.Audio = take
	res, err := q.backend.Generate(ctx, backend.Request{
		ID:         song.ID,
		Caption:    song.Caption.String(),
		Lyrics:     song.Lyrics,
		Seed:       song.Seed,
		MaxSeconds: song.Seconds,
		OutPath:    q.store.AudioPath(out),
		ResumeID:   song.JobID,
		OnStarted: func(id string) {
			if id != song.JobID {
				song.JobID = id
				q.save(song)
			}
		},
	}, func(p backend.Progress) {
		q.publish(Update{SongID: song.ID, Status: library.StatusRendering, Progress: p})
	})

	song.Finished = q.now()
	song.Timings = map[string]float64{}
	for stage, d := range res.Timings {
		song.Timings[string(stage)] = d.Round(100 * time.Millisecond).Seconds()
	}
	cancelled := errors.Is(context.Cause(ctx), ErrCancelled)
	switch {
	case err == nil:
		song.Status = library.StatusDone
		song.Audio = take
		song.Duration = q.probe(res.AudioPath)
		song.JobID = ""
	case errors.Is(context.Cause(ctx), backend.ErrDetach):
		// The app is shutting down; leave the song queued so the next run
		// reattaches to the render, which keeps going in the backend.
		song.Status = library.StatusQueued
	default:
		song.JobID = ""
		msg := err.Error()
		if cancelled {
			msg = "cancelled"
		}
		song.Status = library.StatusFailed
		song.Error = msg
		if hadTake {
			song.Status = library.StatusDone
			song.Error = "re-render " + msg + "; kept the previous take"
		}
	}
	if errors.Is(err, backend.ErrJobLost) {
		song.JobID = ""
	}

	q.mu.Lock()
	q.current, q.cancel = "", nil
	q.save(song)
	q.mu.Unlock()
	return song
}

func (q *Queue) save(song library.Song) {
	u := Update{SongID: song.ID, Status: song.Status, Err: song.Error}
	if err := q.store.Save(song); err != nil {
		u.Err = strings.TrimSpace(u.Err + "; " + err.Error())
	}
	q.publish(u)
}

func probeDuration(path string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}
