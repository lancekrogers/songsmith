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

type Queue struct {
	store   *library.Store
	backend backend.Backend
	wake    chan struct{}
	now     func() time.Time
	probe   func(path string) float64

	mu      sync.Mutex
	current string
	cancel  context.CancelCauseFunc
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
		subs:    map[int]chan Update{},
	}
}

// ErrCancelled is the cause recorded when the user cancels a render.
var ErrCancelled = errors.New("cancelled")

// Subscribe returns a channel of updates and a function that stops them.
// Slow subscribers miss progress updates rather than stalling renders.
func (q *Queue) Subscribe() (<-chan Update, func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	id := q.nextSub
	q.nextSub++
	ch := make(chan Update, 64)
	q.subs[id] = ch
	return ch, func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if c, ok := q.subs[id]; ok {
			delete(q.subs, id)
			close(c)
		}
	}
}

func (q *Queue) publish(u Update) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ch := range q.subs {
		select {
		case ch <- u:
		default:
		}
	}
}

// Current returns the ID of the song being rendered, if any.
func (q *Queue) Current() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.current
}

// Enqueue marks a song queued and wakes the worker.
func (q *Queue) Enqueue(song library.Song) (library.Song, error) {
	if song.Seed == 0 {
		song.Seed = rand.Int64N(1 << 48)
	}
	song.Status = library.StatusQueued
	song.Error = ""
	if err := q.store.Save(song); err != nil {
		return song, err
	}
	q.publish(Update{SongID: song.ID, Status: library.StatusQueued})
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return song, nil
}

// Cancel stops a rendering song or takes a queued one off the queue.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	if q.current == id && q.cancel != nil {
		q.cancel(ErrCancelled)
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()
	song, err := q.store.Load(id)
	if err != nil {
		return err
	}
	if song.Status != library.StatusQueued {
		return fmt.Errorf("song %s is %s, not queued or rendering", id, song.Status)
	}
	song.Status = library.StatusDraft
	if err := q.store.Save(song); err != nil {
		return err
	}
	q.publish(Update{SongID: id, Status: library.StatusDraft})
	return nil
}

// Recover returns songs left "rendering" by a crashed or killed process to
// the queue. Call it only while holding the library render lock.
func (q *Queue) Recover() error {
	songs, err := q.store.List()
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
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
// it runs.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.Recover(); err != nil {
		return fmt.Errorf("recover queue: %w", err)
	}
	for {
		song, ok, err := q.next()
		if err != nil {
			return err
		}
		if !ok {
			select {
			case <-ctx.Done():
				return nil
			case <-q.wake:
				continue
			}
		}
		q.Render(ctx, song)
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (q *Queue) next() (library.Song, bool, error) {
	songs, _ := q.store.List()
	queued := slices.DeleteFunc(songs, func(s library.Song) bool { return s.Status != library.StatusQueued })
	if len(queued) == 0 {
		return library.Song{}, false, nil
	}
	return slices.MinFunc(queued, func(a, b library.Song) int { return a.Updated.Compare(b.Updated) }), true, nil
}

// Render renders one song now and records the outcome in the library.
func (q *Queue) Render(ctx context.Context, song library.Song) library.Song {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	q.mu.Lock()
	q.current, q.cancel = song.ID, cancel
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.current, q.cancel = "", nil
		q.mu.Unlock()
	}()

	if song.Seed == 0 {
		song.Seed = rand.Int64N(1 << 48)
	}
	song.Status = library.StatusRendering
	song.Error = ""
	song.Started = q.now()
	song.Finished = time.Time{}
	song.Backend = q.backend.Name()
	if song.Audio == "" {
		song.Audio = "song.flac"
	}
	q.save(song)

	res, err := q.backend.Generate(ctx, backend.Request{
		ID:         song.ID,
		Caption:    song.Caption.String(),
		Lyrics:     song.Lyrics,
		Seed:       song.Seed,
		MaxSeconds: song.Seconds,
		OutPath:    q.store.AudioPath(song),
	}, func(p backend.Progress) {
		q.publish(Update{SongID: song.ID, Status: library.StatusRendering, Progress: p})
	})

	song.Finished = q.now()
	song.Timings = map[string]float64{}
	for stage, d := range res.Timings {
		song.Timings[string(stage)] = d.Round(100 * time.Millisecond).Seconds()
	}
	switch {
	case err == nil:
		song.Status = library.StatusDone
		song.Duration = q.probe(res.AudioPath)
	case errors.Is(context.Cause(ctx), ErrCancelled):
		song.Status = library.StatusFailed
		song.Error = "cancelled"
	case ctx.Err() != nil:
		// The app is shutting down; leave the song queued for next time.
		song.Status = library.StatusQueued
	default:
		song.Status = library.StatusFailed
		song.Error = err.Error()
	}
	q.save(song)
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
