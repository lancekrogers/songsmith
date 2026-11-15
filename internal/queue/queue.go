// Package queue renders queued songs in the background, one at a time on
// each machine it can render on.
package queue

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
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
	store    *library.Store
	machines func() []Machine
	speeds   Speeds
	reclaim  func() time.Duration
	wake     chan struct{}
	now      func() time.Time
	probe    func(path string) float64
	poll     time.Duration
	// offlineMin and offlineMax bound how long a machine that did not
	// answer is left before it is tried again; lostMin and lostMax how
	// often a machine lost mid-render is checked for.
	offlineMin, offlineMax time.Duration
	lostMin, lostMax       time.Duration
	// liveEvery is how often a machine on another computer is pinged while
	// it renders.
	liveEvery time.Duration
	// fetchPause is how long this computer waits before fetching audio
	// again after a download was cut off.
	fetchPause time.Duration

	// mu guards renders and offline, and serializes the status changes that
	// race with a render being claimed: claim, cancel, enqueue and edit saves.
	mu      sync.Mutex
	renders map[string]*render
	offline map[string]backoff
	// orphans are jobs let go on a machine that did not answer, by machine
	// name, stopped once it answers so they do not hold up its next song.
	orphans map[string][]string
	// running counts the render goroutines Run started.
	running sync.WaitGroup

	// beforeRender runs once a song is claimed and before the backend
	// starts, such as freeing memory the render needs.
	beforeRender func(ctx context.Context)
	// afterRender runs after a render succeeded and was saved.
	afterRender AfterRender

	subMu   sync.Mutex
	subs    map[int]chan Update
	nextSub int
}

// New returns a queue that renders on this computer with b. SetMachines
// adds machines on other computers.
func New(store *library.Store, b backend.Backend) *Queue {
	local := []Machine{{Name: config.ThisComputer, Backend: b, Enabled: true, Local: true}}
	return &Queue{
		store:      store,
		machines:   func() []Machine { return local },
		reclaim:    func() time.Duration { return config.DefaultReclaimAfter },
		wake:       make(chan struct{}, 1),
		now:        time.Now,
		probe:      library.ProbeDuration,
		poll:       PollInterval,
		offlineMin: 15 * time.Second,
		offlineMax: 5 * time.Minute,
		lostMin:    5 * time.Second,
		lostMax:    30 * time.Second,
		liveEvery:  15 * time.Second,
		fetchPause: 2 * time.Second,
		renders:    map[string]*render{},
		offline:    map[string]backoff{},
		orphans:    map[string][]string{},
		subs:       map[int]chan Update{},
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

// Current returns the ID of the song this process is rendering on this
// computer, if any.
func (q *Queue) Current() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, r := range q.renders {
		if r.local && r.cancel != nil {
			return id
		}
	}
	return ""
}

// Rendering returns the IDs of the songs this process is rendering, on any
// machine.
func (q *Queue) Rendering() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []string
	for id, r := range q.renders {
		if r.cancel != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// busy reports whether id is rendering here or in another process. Call with q.mu held.
func (q *Queue) busy(id string) bool {
	if id == "" {
		return false
	}
	if r := q.renders[id]; r != nil && r.cancel != nil {
		return true
	}
	stored, err := q.store.Load(id)
	return err == nil && stored.Status == library.StatusRendering
}

// renderInputsChanged reports whether a render of a and b would differ.
func renderInputsChanged(a, b library.Song) bool {
	return a.Lyrics != b.Lyrics || a.Caption != b.Caption || a.Seed != b.Seed || a.Seconds != b.Seconds
}

// dropStaleJob clears song's detached job when its render inputs changed, or
// it is now pinned to another machine, and stops that job on the machine
// that holds it in the background. Call with q.mu held.
func (q *Queue) dropStaleJob(song *library.Song, force bool) {
	stored, err := q.store.Load(song.ID)
	if err != nil || stored.JobID == "" {
		return
	}
	repinned := song.Machine != "" && !sameMachine(song.Machine, stored.RenderingOn)
	if !force && !repinned && !renderInputsChanged(stored, *song) && song.JobID == stored.JobID {
		return
	}
	if m, ok := q.machine(stored.RenderingOn); ok {
		if q.resting(m.Name, q.now()) {
			q.orphans[m.Name] = append(q.orphans[m.Name], stored.JobID)
		} else {
			go q.stop(m, stored.JobID)
		}
	}
	clearJob(song)
}

// stop abandons jobID on m, and if m does not take it, keeps it to stop
// once m answers again.
func (q *Queue) stop(m Machine, jobID string) {
	if abandon(m, jobID) != nil {
		q.orphan(m.Name, jobID)
	}
}

// clearJob forgets song's detached render.
func clearJob(song *library.Song) {
	song.JobID, song.RenderingOn, song.LostSince = "", "", time.Time{}
}

// abandon stops jobID on m, best effort and for at most a few seconds.
func abandon(m Machine, jobID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.Backend.Abandon(ctx, jobID)
}

// SaveEdit saves changes to an existing song unless it is rendering.
func (q *Queue) SaveEdit(song library.Song) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy(song.ID) {
		return ErrBusy
	}
	q.dropStaleJob(&song, false)
	q.keepTakes(&song)
	q.keepJob(&song)
	if err := q.store.Save(song); err != nil {
		return err
	}
	q.publish(Update{SongID: song.ID, Status: song.Status})
	return nil
}

// keepTakes gives song the current take on disk: a copy read before a take
// was chosen or mastered must not switch it back.
func (q *Queue) keepTakes(song *library.Song) {
	if stored, err := q.store.Load(song.ID); err == nil {
		song.Audio, song.Duration = stored.Audio, stored.Duration
	}
}

// keepJob gives song the stored record of where its detached render is, so
// a copy read before the render detached or stopped answering cannot send
// it to the wrong machine.
func (q *Queue) keepJob(song *library.Song) {
	stored, err := q.store.Load(song.ID)
	if song.JobID == "" || err != nil || stored.JobID != song.JobID {
		song.RenderingOn, song.LostSince = "", time.Time{}
		return
	}
	song.RenderingOn, song.LostSince = stored.RenderingOn, stored.LostSince
}

// Enqueue marks a song queued and wakes the worker, returning the song it
// queued. A finished song, or one with audio, is left as it is: Enqueue
// queues a new version of it from library.Store.NewVersion instead. A song
// that cannot render is refused with its library.Song.CheckRender error. An
// unpinned seed is drawn afresh unless the song resumes a detached render.
func (q *Queue) Enqueue(song library.Song) (library.Song, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy(song.ID) {
		return song, ErrBusy
	}
	if err := song.CheckRender(); err != nil {
		return song, err
	}
	resumes := q.resumes(song)
	if !resumes && (song.Status == library.StatusDone || q.store.HasAudio(song)) {
		v, err := q.store.NewVersion(song)
		if err != nil {
			return song, fmt.Errorf("new version of %s: %w", song.Title, err)
		}
		song, resumes = v, false
	}
	if !song.SeedPinned && !resumes {
		song.Seed = newSeed()
	}
	q.dropStaleJob(&song, false)
	q.keepTakes(&song)
	q.keepJob(&song)
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
	return err == nil && stored.JobID == song.JobID && !renderInputsChanged(stored, song) &&
		(song.Machine == "" || sameMachine(song.Machine, stored.RenderingOn))
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
	if r := q.renders[id]; r != nil && r.cancel != nil {
		r.cancel(ErrCancelled)
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
// the queue, keeping their job ID so the render can be resumed on the
// machine that holds it. A song whose job is on a machine that is no longer
// set up forgets the job, which is left alone. Call it only while holding
// the library render lock. Unreadable songs are skipped.
func (q *Queue) Recover() error {
	songs, _ := q.store.List()
	var errs []error
	for _, s := range songs {
		if s.Status != library.StatusRendering && s.Status != library.StatusQueued {
			continue
		}
		changed := s.Status == library.StatusRendering
		s.Status = library.StatusQueued
		if s.JobID != "" || s.RenderingOn != "" {
			if _, ok := q.machine(s.RenderingOn); !ok {
				clearJob(&s)
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := q.store.Save(s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run renders queued songs until ctx ends, each on a free machine. Hold the
// library render lock while it runs. Songs queued by other processes are
// picked up within PollInterval. When ctx ends, Run detaches from every
// render and returns once each one's song is saved.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.Recover(); err != nil {
		q.publish(Update{Err: "recover queue: " + err.Error()})
	}
	defer q.running.Wait()
	for {
		q.dispatch(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-q.wake:
		case <-time.After(q.poll):
		}
	}
}

// queued lists the queued songs, oldest first.
func (q *Queue) queued() []library.Song {
	songs, _ := q.store.List()
	queued := slices.DeleteFunc(songs, func(s library.Song) bool { return s.Status != library.StatusQueued })
	slices.SortStableFunc(queued, func(a, b library.Song) int { return a.Updated.Compare(b.Updated) })
	return queued
}

// claim marks song as rendering on m if it is still queued, reading the
// stored copy so edits and cancels made since it was listed win.
func (q *Queue) claim(song library.Song, m Machine, cancel context.CancelCauseFunc) (library.Song, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if stored, err := q.store.Load(song.ID); err == nil {
		song = stored
	} else if errors.Is(err, library.ErrNoSong) {
		return song, false
	}
	if song.Status != library.StatusQueued {
		return song, false
	}
	r := q.renders[song.ID]
	if r == nil {
		r = &render{machine: m.Name, local: m.Local}
		q.renders[song.ID] = r
	}
	r.cancel = cancel
	song.Status = library.StatusRendering
	song.Error = ""
	song.Started = q.now()
	song.Finished = time.Time{}
	song.Backend = m.Backend.Name()
	song.RenderingOn = remoteName(m)
	song.LostSince = time.Time{}
	if song.Seed == 0 && !song.SeedPinned {
		song.Seed = newSeed()
	}
	q.save(song)
	return song, true
}

// beforeRenderTimeout bounds the hook that runs before each render.
var beforeRenderTimeout = 5 * time.Second

// BeforeRender sets fn to run, best effort and for at most a few seconds,
// each time a song is claimed to render on this computer and before the
// backend starts it. fn returns nothing: whatever it does, the render goes
// ahead.
func (q *Queue) BeforeRender(fn func(ctx context.Context)) { q.beforeRender = fn }

// Render renders one queued song now, on the machine holding its detached
// render, the machine it is pinned to, or the fastest machine that answers,
// and records the outcome in the library. A song that is no longer queued is
// returned unchanged. When no machine can take it, it stays queued and the
// returned copy's Error says why.
func (q *Queue) Render(ctx context.Context, song library.Song) library.Song {
	m, err := q.pick(ctx, song)
	if err != nil {
		if stored, lerr := q.store.Load(song.ID); lerr == nil {
			song = stored
		}
		song.Error = err.Error()
		return song
	}
	return q.renderOn(ctx, song, m)
}

// renderOn renders song on m, which the caller has reserved for it.
func (q *Queue) renderOn(ctx context.Context, song library.Song, m Machine) library.Song {
	// Shutting down the caller detaches from the render instead of stopping
	// it, so the backend keeps working and the next run picks up the result.
	parent := ctx
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	defer cancel(nil)
	stopDetach := context.AfterFunc(parent, func() { cancel(backend.ErrDetach) })
	defer stopDetach()
	song, ok := q.claim(song, m, cancel)
	if !ok {
		q.release(song.ID)
		return song
	}

	if m.Local && q.beforeRender != nil {
		hookCtx, done := context.WithTimeout(ctx, beforeRenderTimeout)
		q.beforeRender(hookCtx)
		done()
	}

	hadTake := q.store.HasAudio(song)
	take := q.store.NextTake(song)
	out := song
	out.Audio = take
	resumed := song.JobID != ""
	req := backend.Request{
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
	}
	progress := func(p backend.Progress) {
		q.publish(Update{SongID: song.ID, Status: library.StatusRendering, Progress: p})
	}

	var res backend.Result
	var err error
	// requeued sends the song back to the queue, saying why in note.
	requeued, note := false, ""
	// fetchFails counts the attempts in a row to fetch a render the machine
	// holds that were cut off: its audio, or on this computer, any reattach.
	fetchFails, unfetched := 0, false
	for {
		res, err = q.generate(ctx, m, req, progress)
		if err == nil || context.Cause(ctx) != nil || !errors.Is(err, backend.ErrUnreachable) {
			break
		}
		reattached := m.Local && req.ResumeID != ""
		if song.JobID != "" && (errors.Is(err, backend.ErrDownload) || reattached) {
			if fetchFails++; fetchFails >= fetchTries {
				unfetched = true
				break
			}
			if m.Local {
				if !pause(ctx, q.fetchPause) {
					break
				}
				req.ResumeID, resumed = song.JobID, true
				continue
			}
		} else {
			fetchFails = 0
		}
		if m.Local {
			break
		}
		if song.JobID == "" {
			// The machine stopped answering before it took the song.
			q.markOffline(m.Name)
			requeued = true
			note = fmt.Sprintf("%s stopped answering before it took the song (%v); it stays queued", m.Name, err)
			break
		}
		switch q.waitForMachine(ctx, &song, m) {
		case machineBack:
			req.ResumeID, resumed = song.JobID, true
			continue
		case machineGone:
			q.orphan(m.Name, song.JobID)
			q.markOffline(m.Name)
			requeued = true
			note = fmt.Sprintf("%s did not answer for %s, so the song was queued again", m.Name, q.reclaim())
		}
		break
	}

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
		clearJob(&song)
		if !resumed && q.speeds != nil {
			if err := q.speeds.Record(m.Name, song.Finished.Sub(song.Started), song.Duration); err != nil {
				song.Error = fmt.Sprintf("the render finished, but its speed on %s was not recorded: %v", spoken(m), err)
			}
		}
	case requeued:
		song.Status = library.StatusQueued
		song.Error = note
		clearJob(&song)
	case unfetched:
		// The render is still on the machine: keeping the job lets rendering
		// the song again pick it up instead of rendering it anew.
		song.Status = library.StatusFailed
		song.Error = fmt.Sprintf("the render finished, but its audio could not be fetched from %s (%v); render it again to fetch it once more", spoken(m), err)
		if !errors.Is(err, backend.ErrDownload) {
			song.Error = fmt.Sprintf("could not pick up its render on %s (%v); render it again to try once more", spoken(m), err)
		}
		if hadTake {
			song.Status = library.StatusDone
			song.Error += "; kept the previous take"
		}
	case errors.Is(context.Cause(ctx), backend.ErrDetach):
		// The app is shutting down; leave the song queued so the next run
		// reattaches to the render, which keeps going on its machine.
		song.Status = library.StatusQueued
		if song.JobID == "" {
			clearJob(&song)
		}
	default:
		clearJob(&song)
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
		clearJob(&song)
	}

	q.mu.Lock()
	delete(q.renders, song.ID)
	song = q.finish(song, err == nil, library.Take{File: take, Kind: library.TakeRender, Machine: remoteName(m)})
	q.mu.Unlock()
	q.Wake()
	if err == nil && q.afterRender != nil {
		song = q.runAfterRender(parent, song)
	}
	return song
}

// fetchTries is how many downloads in a row may be cut off before a render
// whose audio cannot be fetched fails.
const fetchTries = 3

// pause waits for d, and reports false if ctx ended first.
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// spoken names m as a sentence does: "this computer", or its name.
func spoken(m Machine) string {
	if m.Local {
		return "this computer"
	}
	return m.Name
}

// finish saves the outcome of a render. A new take is recorded and made
// current; otherwise the current take stays whichever is current on disk,
// since it may have been changed while the render ran.
func (q *Queue) finish(song library.Song, rendered bool, take library.Take) library.Song {
	saved, err := q.store.Update(song.ID, func(s *library.Song) error {
		audio, duration := s.Audio, s.Duration
		*s = song
		if rendered {
			take.Created, take.Duration = q.now(), song.Duration
			s.Takes = append(s.Takes, take)
		} else {
			s.Audio, s.Duration = audio, duration
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		saved, err = song, q.store.Save(song)
	}
	u := Update{SongID: song.ID, Status: song.Status, Err: song.Error}
	if err != nil {
		u.Err = strings.TrimSpace(u.Err + "; " + err.Error())
		saved = song
	}
	q.publish(u)
	return saved
}

// AfterRender post-processes a finished render, such as mastering it. It
// returns the song as it should be saved; an error leaves the raw render
// current and is recorded on the song.
type AfterRender func(ctx context.Context, song library.Song) (library.Song, error)

// AfterRender sets fn to run after each successful render. A failure never
// fails the render.
func (q *Queue) AfterRender(fn AfterRender) { q.afterRender = fn }

func (q *Queue) runAfterRender(ctx context.Context, song library.Song) library.Song {
	out, err := q.afterRender(ctx, song)
	if err != nil && ctx.Err() != nil {
		// Shutting down is not a failure; the raw render stays current.
		return song
	}
	if err == nil {
		q.publish(Update{SongID: out.ID, Status: out.Status, Err: out.Error})
		return out
	}
	saved, uerr := q.store.Update(song.ID, func(s *library.Song) error {
		s.Error = "mastering after the render failed, so the raw take is current: " + err.Error()
		return nil
	})
	if uerr != nil {
		return song
	}
	q.publish(Update{SongID: song.ID, Status: saved.Status, Err: saved.Error})
	return saved
}

func (q *Queue) save(song library.Song) {
	u := Update{SongID: song.ID, Status: song.Status, Err: song.Error}
	if err := q.store.Save(song); err != nil {
		u.Err = strings.TrimSpace(u.Err + "; " + err.Error())
	}
	q.publish(u)
}

// Delete removes a song for good unless it is queued or rendering.
func (q *Queue) Delete(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy(id) {
		return ErrBusy
	}
	return q.store.Delete(id)
}

// Move puts a song in a library folder unless it is queued or rendering.
func (q *Queue) Move(id, folder string) (library.Song, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy(id) {
		return library.Song{}, ErrBusy
	}
	return q.store.Move(id, folder)
}

// MoveFolder moves or renames a library folder unless a song in it is
// queued or rendering.
func (q *Queue) MoveFolder(from, to string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.store.MoveFolder(from, to)
}

// DeleteFolder removes a library folder and its songs for good unless one
// of them is queued or rendering.
func (q *Queue) DeleteFolder(folder string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.store.DeleteFolder(folder)
}
