package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/machines"
)

const mbp = "MacBook Pro"

// rig is a queue over stand-in ComfyUI servers: this computer and the
// added machines, each a server of its own.
type rig struct {
	t      *testing.T
	store  *library.Store
	speeds *machines.Speeds
	local  *comfytest.Server

	mu      sync.Mutex
	cfg     config.Config
	remotes map[string]*comfytest.Server
	hooks   []string
}

func newRig(t *testing.T, remotes ...string) *rig {
	r := &rig{t: t, store: library.NewStore(t.TempDir()), speeds: machines.NewSpeeds(t.TempDir()),
		local: comfytest.New(t, "local"), remotes: map[string]*comfytest.Server{}, cfg: config.Default()}
	r.cfg.ReclaimAfter = time.Minute
	for _, name := range remotes {
		srv := comfytest.New(t, strings.ReplaceAll(name, " ", ""))
		r.remotes[name] = srv
		r.cfg.Machines = append(r.cfg.Machines, config.Machine{Name: name, URL: srv.URL, Enabled: true})
	}
	return r
}

func (r *rig) config() config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *rig) change(fn func(*config.Config)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.Machines = slices.Clone(r.cfg.Machines)
	fn(&r.cfg)
}

// queue builds a fresh queue over the rig, as a new songsmith process would.
func (r *rig) queue() *Queue {
	localBackend := backend.NewComfyMiniMax(comfy.NewClient(r.local.URL), nil, r.config)
	q := New(r.store, localBackend)
	q.probe = func(string) float64 { return 60 }
	q.poll = 20 * time.Millisecond
	q.offlineMin, q.offlineMax = 100*time.Millisecond, 200*time.Millisecond
	q.lostMin, q.lostMax = 20*time.Millisecond, 50*time.Millisecond
	q.liveEvery = 20 * time.Millisecond
	q.fetchPause = 20 * time.Millisecond
	q.SetSpeeds(r.speeds)
	q.ReclaimAfter(func() time.Duration { return r.config().ReclaimAfter })
	remote := map[string]backend.Backend{}
	q.SetMachines(func() []Machine {
		cfg := r.config()
		ms := []Machine{{Name: config.ThisComputer, Backend: localBackend, Enabled: cfg.Local.Enabled, Local: true}}
		for _, mc := range cfg.Machines {
			if remote[mc.Name] == nil {
				remote[mc.Name] = machines.Backend(mc.Name, mc.URL, r.config)
			}
			url, models := mc.URL, mc.Models(cfg.ComfyUI)
			ms = append(ms, Machine{Name: mc.Name, Backend: remote[mc.Name], Enabled: mc.Enabled,
				Ping: func(ctx context.Context) error {
					ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
					defer cancel()
					return machines.Ping(ctx, url)
				},
				Ready: func(ctx context.Context) error {
					ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
					defer cancel()
					if st := machines.Check(ctx, url, models); st.State != machines.Ready {
						return errors.New(st.Detail)
					}
					return nil
				}})
		}
		return ms
	})
	q.BeforeRender(func(context.Context) {
		r.mu.Lock()
		r.hooks = append(r.hooks, "unload writer")
		r.mu.Unlock()
	})
	return q
}

func (r *rig) hookRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hooks)
}

// run runs q until the test ends or stop is called, and waits for it.
func (r *rig) run(q *Queue) (stop func()) {
	ctx, cancel := context.WithCancel(r.t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	r.t.Cleanup(stop)
	return stop
}

func (r *rig) song(title, pin string) library.Song {
	s := create(r.t, r.store, title, library.StatusQueued)
	if pin != "" {
		s.Machine = pin
		if err := r.store.Save(s); err != nil {
			r.t.Fatal(err)
		}
	}
	time.Sleep(5 * time.Millisecond)
	return s
}

func (r *rig) load(id string) library.Song {
	s, err := r.store.Load(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// queued waits for the next prompt srv is sent.
func queued(t *testing.T, srv *comfytest.Server) string {
	t.Helper()
	select {
	case id := <-srv.Queued():
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt reached the server")
		return ""
	}
}

// job waits until song id records the job holding it, and returns it.
func (r *rig) job(id string) string {
	r.t.Helper()
	eventually(r.t, "the song records its job", func() bool { return r.load(id).JobID != "" })
	return r.load(id).JobID
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func noPrompt(t *testing.T, srv *comfytest.Server, within time.Duration) {
	t.Helper()
	select {
	case id := <-srv.Queued():
		t.Fatalf("prompt %s reached a server it should not have", id)
	case <-time.After(within):
	}
}

func takeMachines(s library.Song) []string {
	var out []string
	for _, tk := range s.Takes {
		out = append(out, tk.File+"@"+tk.Machine)
	}
	return out
}

func TestTwoMachinesRenderTwoSongsAtOnce(t *testing.T) {
	r := newRig(t, mbp)
	r.local.SetHold(true)
	r.remotes[mbp].SetHold(true)
	q := r.queue()
	a, b := r.song("A", ""), r.song("B", "")
	r.run(q)

	here, there := queued(t, r.local), queued(t, r.remotes[mbp])
	if got := q.Rendering(); len(got) != 2 {
		t.Fatalf("rendering %v", got)
	}
	if q.Current() == "" {
		t.Fatal("Current names no song rendering on this computer")
	}
	onMBP := r.load(a.ID)
	if onMBP.RenderingOn != mbp {
		onMBP = r.load(b.ID)
	}
	if onMBP.RenderingOn != mbp || onMBP.Status != library.StatusRendering {
		t.Fatalf("no song is rendering on the MacBook Pro: %+v", onMBP)
	}
	r.local.Finish(here)
	r.remotes[mbp].Finish(there)
	eventually(t, "both songs are done", func() bool {
		return r.load(a.ID).Status == library.StatusDone && r.load(b.ID).Status == library.StatusDone
	})
	got := slices.Concat(takeMachines(r.load(a.ID)), takeMachines(r.load(b.ID)))
	slices.Sort(got)
	if !slices.Equal(got, []string{"song.flac@", "song.flac@" + mbp}) {
		t.Fatalf("takes %v", got)
	}
	done := r.load(onMBP.ID)
	if done.RenderingOn != "" || done.JobID != "" {
		t.Fatalf("a finished song still names its job: %+v", done)
	}
	if data, _ := os.ReadFile(r.store.AudioPath(done)); string(data) != "audio from MacBookPro" {
		t.Fatalf("audio %q", data)
	}
	if r.hookRuns() != 1 {
		t.Fatalf("the writer was unloaded %d times for one render on this computer", r.hookRuns())
	}
}

func TestPinnedSongsWaitForTheirMachineWithoutHoldingUpOthers(t *testing.T) {
	r := newRig(t, mbp)
	r.local.SetHold(true)
	r.remotes[mbp].SetHold(true)
	q := r.queue()
	first := r.song("First", mbp)
	second := r.song("Second", mbp)
	third := r.song("Third", "")
	r.run(q)

	if id := queued(t, r.remotes[mbp]); r.job(first.ID) != id {
		t.Fatalf("the MacBook Pro took %s, not the oldest song pinned to it", id)
	}
	here := queued(t, r.local)
	if r.job(third.ID) != here {
		t.Fatal("the unpinned song did not go to the free machine")
	}
	r.local.Finish(here)
	eventually(t, "the unpinned song is done", func() bool { return r.load(third.ID).Status == library.StatusDone })
	noPrompt(t, r.local, 150*time.Millisecond)
	if s := r.load(second.ID); s.Status != library.StatusQueued {
		t.Fatalf("a song pinned to a busy machine went elsewhere: %+v", s)
	}
	r.remotes[mbp].Finish(r.job(first.ID))
	next := queued(t, r.remotes[mbp])
	r.remotes[mbp].Finish(next)
	eventually(t, "the second pinned song is done", func() bool { return r.load(second.ID).Status == library.StatusDone })
	if got := takeMachines(r.load(second.ID)); !slices.Equal(got, []string{"song.flac@" + mbp}) {
		t.Fatalf("takes %v", got)
	}
}

func TestAMachineOfflineAtSendIsSkippedAndTriedAgainLater(t *testing.T) {
	r := newRig(t, mbp)
	r.speeds.Record(mbp, time.Minute, 120)
	r.speeds.Record(config.ThisComputer, 3*time.Minute, 60)
	r.remotes[mbp].SetDown(true)
	q := r.queue()
	first := r.song("First", "")
	r.run(q)
	queued(t, r.local)
	eventually(t, "the song is done on this computer", func() bool { return r.load(first.ID).Status == library.StatusDone })
	if got := takeMachines(r.load(first.ID)); !slices.Equal(got, []string{"song.flac@"}) {
		t.Fatalf("takes %v", got)
	}

	r.remotes[mbp].SetDown(false)
	// Past the wait before a machine that did not answer is tried again.
	time.Sleep(250 * time.Millisecond)
	second := r.song("Second", "")
	queued(t, r.remotes[mbp])
	eventually(t, "the second song is done", func() bool { return r.load(second.ID).Status == library.StatusDone })
	if got := takeMachines(r.load(second.ID)); !slices.Equal(got, []string{"song.flac@" + mbp}) {
		t.Fatalf("the faster machine was not used once it answered: %v", got)
	}
}

func TestAPinnedSongWaitsForAnOfflineMachine(t *testing.T) {
	r := newRig(t, mbp)
	r.remotes[mbp].SetDown(true)
	q := r.queue()
	song := r.song("Pinned", mbp)
	r.run(q)
	noPrompt(t, r.local, 300*time.Millisecond)
	if s := r.load(song.ID); s.Status != library.StatusQueued {
		t.Fatalf("status %s", s.Status)
	}
	r.remotes[mbp].SetDown(false)
	queued(t, r.remotes[mbp])
	eventually(t, "the pinned song is done", func() bool { return r.load(song.ID).Status == library.StatusDone })
}

func TestAMachineLostMidRenderIsWaitedForAndReattached(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.change(func(c *config.Config) { c.Local.Enabled = false })
	q := r.queue()
	updates, unsub := q.Subscribe()
	defer unsub()
	song := r.song("Song", "")
	r.run(q)
	id := queued(t, srv)

	srv.SetDown(true)
	eventually(t, "the song waits for its machine", func() bool { return !r.load(song.ID).LostSince.IsZero() })
	waiting := r.load(song.ID)
	if waiting.Status != library.StatusRendering || waiting.RenderingOn != mbp || waiting.JobID != id {
		t.Fatalf("while lost: %+v", waiting)
	}
	sawWaiting := false
	for len(updates) > 0 {
		if u := <-updates; u.SongID == song.ID && u.Progress.Stage == backend.StageWaiting {
			sawWaiting = true
		}
	}
	if !sawWaiting {
		t.Fatal("no update said the song is waiting for its machine")
	}

	srv.Finish(id)
	srv.SetDown(false)
	eventually(t, "the song is done", func() bool { return r.load(song.ID).Status == library.StatusDone })
	if got := srv.Prompts(); len(got) != 1 {
		t.Fatalf("the render was sent again instead of reattached: %v", got)
	}
	done := r.load(song.ID)
	if !done.LostSince.IsZero() || done.RenderingOn != "" || done.Error != "" {
		t.Fatalf("done: %+v", done)
	}
	if _, ok := r.speeds.Speed(mbp); ok {
		t.Fatal("a reattached render was measured")
	}
}

func TestASongLostPastReclaimAfterIsQueuedAgain(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.local.SetHold(true)
	r.change(func(c *config.Config) { c.ReclaimAfter = 150 * time.Millisecond })
	q := r.queue()
	r.local.SetDown(false)
	blocker := r.song("Blocker", config.ThisComputer)
	pinned := r.song("Pinned", mbp)
	free := r.song("Free", "")
	r.run(q)
	queued(t, r.local)
	lostID := queued(t, srv)
	if r.job(pinned.ID) != lostID {
		t.Fatal("the pinned song did not go to its machine")
	}
	r.local.Finish(r.job(blocker.ID))
	freeID := queued(t, r.local)
	if r.job(free.ID) != freeID {
		t.Fatal("the free song did not render here")
	}

	srv.SetDown(true)
	eventually(t, "the pinned song is queued again", func() bool {
		s := r.load(pinned.ID)
		return s.Status == library.StatusQueued && s.JobID == ""
	})
	s := r.load(pinned.ID)
	if s.RenderingOn != "" || !strings.Contains(s.Error, mbp+" did not answer for 150ms") {
		t.Fatalf("reclaimed: %+v", s)
	}
	r.local.Finish(freeID)
	noPrompt(t, r.local, 300*time.Millisecond)
	if s := r.load(pinned.ID); s.Status != library.StatusQueued {
		t.Fatalf("a reclaimed pinned song went elsewhere: %+v", s)
	}

	srv.SetDown(false)
	again := queued(t, srv)
	if again == lostID {
		t.Fatal("the abandoned render was reused")
	}
	if !slices.Contains(srv.Stopped(), lostID) {
		t.Fatalf("the abandoned render was never stopped once the machine answered: %v", srv.Stopped())
	}
}

func TestAReclaimedUnpinnedSongRendersOnAnotherMachine(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.change(func(c *config.Config) { c.ReclaimAfter = 100 * time.Millisecond; c.Local.Enabled = false })
	q := r.queue()
	song := r.song("Song", "")
	r.run(q)
	queued(t, srv)
	srv.SetDown(true)
	r.change(func(c *config.Config) { c.Local.Enabled = true })
	queued(t, r.local)
	eventually(t, "the song is done here", func() bool { return r.load(song.ID).Status == library.StatusDone })
	if got := takeMachines(r.load(song.ID)); !slices.Equal(got, []string{"song.flac@"}) {
		t.Fatalf("takes %v", got)
	}
}

func TestCancelGoesToTheMachineRenderingTheSong(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.local.SetHold(true)
	q := r.queue()
	here := r.song("Here", config.ThisComputer)
	there := r.song("There", mbp)
	r.run(q)
	hereID, thereID := queued(t, r.local), queued(t, srv)
	r.job(here.ID)
	r.job(there.ID)
	if err := q.Cancel(there.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the remote song is cancelled", func() bool { return r.load(there.ID).Status == library.StatusFailed })
	if s := r.load(there.ID); s.Error != "cancelled" {
		t.Fatalf("cancelled: %+v", s)
	}
	if !slices.Contains(srv.Stopped(), thereID) || len(r.local.Stopped()) != 0 {
		t.Fatalf("stopped on the MacBook Pro %v, here %v", srv.Stopped(), r.local.Stopped())
	}
	if !r.local.Running(hereID) || r.load(here.ID).Status != library.StatusRendering {
		t.Fatal("cancelling one song stopped the other")
	}
}

func TestCancelWhileWaitingForALostMachine(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	q := r.queue()
	song := r.song("Song", mbp)
	r.run(q)
	queued(t, srv)
	srv.SetDown(true)
	eventually(t, "the song waits for its machine", func() bool { return !r.load(song.ID).LostSince.IsZero() })
	if err := q.Cancel(song.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the song is cancelled", func() bool { return r.load(song.ID).Status == library.StatusFailed })
	if s := r.load(song.ID); s.JobID != "" || s.RenderingOn != "" {
		t.Fatalf("cancelled: %+v", s)
	}
}

func TestQuitDetachesFromEveryRenderAndTheNextStartReattaches(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.local.SetHold(true)
	a, b := r.song("A", config.ThisComputer), r.song("B", mbp)
	stop := r.run(r.queue())
	hereID, thereID := queued(t, r.local), queued(t, srv)
	r.job(a.ID)
	r.job(b.ID)
	stop()

	sa, sb := r.load(a.ID), r.load(b.ID)
	if sa.Status != library.StatusQueued || sa.JobID != hereID || sa.RenderingOn != "" {
		t.Fatalf("this computer's song after quitting: %+v", sa)
	}
	if sb.Status != library.StatusQueued || sb.JobID != thereID || sb.RenderingOn != mbp {
		t.Fatalf("the MacBook Pro's song after quitting: %+v", sb)
	}
	if len(srv.Stopped())+len(r.local.Stopped()) != 0 {
		t.Fatal("quitting stopped a render")
	}

	r.local.Finish(hereID)
	srv.Finish(thereID)
	r.run(r.queue())
	eventually(t, "both songs are done", func() bool {
		return r.load(a.ID).Status == library.StatusDone && r.load(b.ID).Status == library.StatusDone
	})
	if len(r.local.Prompts()) != 1 || len(srv.Prompts()) != 1 {
		t.Fatalf("renders were sent again: here %v, there %v", r.local.Prompts(), srv.Prompts())
	}
	if got := takeMachines(r.load(b.ID)); !slices.Equal(got, []string{"song.flac@" + mbp}) {
		t.Fatalf("takes %v", got)
	}
}

func TestASongOnARemovedMachineIsQueuedAgainAtStart(t *testing.T) {
	r := newRig(t, mbp)
	gone := comfytest.New(t, "gone")
	song := r.song("Song", "")
	song.Status, song.JobID, song.RenderingOn = library.StatusRendering, "gone-1", "Old Laptop"
	if err := r.store.Save(song); err != nil {
		t.Fatal(err)
	}
	q := r.queue()
	if err := q.Recover(); err != nil {
		t.Fatal(err)
	}
	s := r.load(song.ID)
	if s.Status != library.StatusQueued || s.JobID != "" || s.RenderingOn != "" {
		t.Fatalf("recovered: %+v", s)
	}
	if len(gone.Stopped()) != 0 {
		t.Fatal("the old job was touched")
	}
}

func TestADetachedRenderOnAMachineThatNeverReturnsIsReclaimedAtStart(t *testing.T) {
	r := newRig(t, mbp)
	r.change(func(c *config.Config) { c.ReclaimAfter = time.Minute })
	r.remotes[mbp].SetDown(true)
	song := r.song("Song", "")
	song.JobID, song.RenderingOn, song.LostSince = "MacBookPro-9", mbp, time.Now().Add(-time.Hour)
	if err := r.store.Save(song); err != nil {
		t.Fatal(err)
	}
	r.run(r.queue())
	queued(t, r.local)
	eventually(t, "the song is done here", func() bool { return r.load(song.ID).Status == library.StatusDone })
}

func TestTheFastestFreeMachineIsUsedAndUnmeasuredOnesAreTriedFirst(t *testing.T) {
	r := newRig(t, mbp, "Attic")
	r.speeds.Record(config.ThisComputer, 2*time.Minute, 60)
	r.speeds.Record(mbp, time.Minute, 60)
	q := r.queue()
	first := r.song("First", "")
	stop := r.run(q)
	queued(t, r.remotes["Attic"])
	eventually(t, "the first song is done", func() bool { return r.load(first.ID).Status == library.StatusDone })
	stop()
	if _, ok := r.speeds.Speed("Attic"); !ok {
		t.Fatal("a render on an unmeasured machine was not measured")
	}

	r.speeds.Record("Attic", 10*time.Minute, 1)
	second := r.song("Second", "")
	r.run(r.queue())
	queued(t, r.remotes[mbp])
	eventually(t, "the second song is done", func() bool { return r.load(second.ID).Status == library.StatusDone })
}

func TestDisabledMachinesGetNoNewSongs(t *testing.T) {
	r := newRig(t, mbp)
	r.change(func(c *config.Config) { c.Machines[0].Enabled = false; c.Local.Enabled = false })
	q := r.queue()
	song := r.song("Song", "")
	r.run(q)
	noPrompt(t, r.local, 200*time.Millisecond)
	noPrompt(t, r.remotes[mbp], 0)
	r.change(func(c *config.Config) { c.Machines[0].Enabled = true })
	q.Wake()
	queued(t, r.remotes[mbp])
	eventually(t, "the song is done", func() bool { return r.load(song.ID).Status == library.StatusDone })
}

func TestRenderNamesWhyNoMachineCanTakeASong(t *testing.T) {
	r := newRig(t, mbp)
	r.remotes[mbp].SetDown(true)
	q := r.queue()
	got := q.Render(t.Context(), r.song("Song", mbp))
	if got.Status != library.StatusQueued || !strings.Contains(got.Error, mbp+" cannot take it: not answering at") {
		t.Fatalf("render on an offline machine: %+v", got)
	}
	got = q.Render(t.Context(), r.song("Elsewhere", "Nowhere"))
	if !strings.Contains(got.Error, "Nowhere, which is not one of the machines") {
		t.Fatalf("render on an unknown machine: %+v", got)
	}
}

func TestRenderOnThisComputerAloneIsUnchanged(t *testing.T) {
	var calls atomic.Int32
	b := &fakeBackend{}
	q, store := setup(t, b)
	q.BeforeRender(func(context.Context) { calls.Add(1) })
	got := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	if got.RenderingOn != "" || got.Takes[0].Machine != "" || calls.Load() != 1 {
		t.Fatalf("song %+v, hook %d", got, calls.Load())
	}
}

func TestAMachineThatAnswersButCannotRenderGetsNoNewSongs(t *testing.T) {
	r := newRig(t, mbp)
	models := config.Default().ComfyUI
	r.remotes[mbp].SetModels(models.DiffusionModel, models.TextEncoder)
	r.speeds.Record(mbp, time.Second, 60)
	r.speeds.Record(config.ThisComputer, time.Hour, 60)
	q := r.queue()
	var songs []library.Song
	for _, title := range []string{"One", "Two", "Three"} {
		songs = append(songs, r.song(title, ""))
	}
	r.run(q)
	eventually(t, "every song is done", func() bool {
		for _, s := range songs {
			if r.load(s.ID).Status != library.StatusDone {
				return false
			}
		}
		return true
	})
	for _, s := range songs {
		if got := takeMachines(r.load(s.ID)); !slices.Equal(got, []string{"song.flac@"}) {
			t.Fatalf("%s: takes %v", s.Title, got)
		}
	}
	if got := r.remotes[mbp].Prompts(); len(got) != 0 {
		t.Fatalf("a machine without the model files was sent %v", got)
	}
}

func TestAMachineThatGoesSilentMidRenderIsWaitedForAndReattached(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.change(func(c *config.Config) { c.Local.Enabled = false })
	song := r.song("Song", "")
	r.run(r.queue())
	id := queued(t, srv)
	r.job(song.ID)

	srv.SetSilent(true)
	eventually(t, "the song waits for its silent machine", func() bool { return !r.load(song.ID).LostSince.IsZero() })
	if s := r.load(song.ID); s.Status != library.StatusRendering || s.JobID != id {
		t.Fatalf("while silent: %+v", s)
	}
	if slices.Contains(srv.Stopped(), id) {
		t.Fatal("going silent stopped the render")
	}
	srv.Finish(id)
	srv.SetSilent(false)
	eventually(t, "the song is done", func() bool { return r.load(song.ID).Status == library.StatusDone })
	if got := srv.Prompts(); len(got) != 1 {
		t.Fatalf("the render was sent again instead of reattached: %v", got)
	}
}

func TestASongWhoseMachineStaysSilentIsQueuedAgain(t *testing.T) {
	r := newRig(t, mbp)
	srv := r.remotes[mbp]
	srv.SetHold(true)
	r.change(func(c *config.Config) { c.Local.Enabled = false; c.ReclaimAfter = 200 * time.Millisecond })
	song := r.song("Song", "")
	r.run(r.queue())
	queued(t, srv)
	r.job(song.ID)
	srv.SetSilent(true)
	eventually(t, "the song is queued again", func() bool {
		s := r.load(song.ID)
		return s.Status == library.StatusQueued && s.JobID == ""
	})
	if s := r.load(song.ID); !strings.Contains(s.Error, mbp+" did not answer for 200ms") {
		t.Fatalf("reclaimed: %+v", s)
	}
}

func TestAnEditFromAStaleCopyKeepsWhereTheRenderIs(t *testing.T) {
	r := newRig(t, mbp)
	q := r.queue()
	song := r.song("Song", "")
	stale := song
	song.JobID, song.RenderingOn = "MacBookPro-3", mbp
	if err := r.store.Save(song); err != nil {
		t.Fatal(err)
	}
	stale.JobID = "MacBookPro-3"
	stale.Title = "Renamed"
	if err := q.SaveEdit(stale); err != nil {
		t.Fatal(err)
	}
	if s := r.load(song.ID); s.JobID != "MacBookPro-3" || s.RenderingOn != mbp || s.Title != "Renamed" {
		t.Fatalf("after an edit from a copy without rendering_on: %+v", s)
	}
	stale.RenderingOn = "Somewhere else"
	stale.Status = library.StatusDraft
	got, err := q.Enqueue(stale)
	if err != nil || got.RenderingOn != mbp || got.JobID != "MacBookPro-3" {
		t.Fatalf("enqueue from a copy naming another machine: %v %+v", err, got)
	}
}

func TestADownloadCutOffMidwayIsFetchedAgain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		memory  int64
		prompts int
	}{{"one prompt", 128 * comfytest.GiB, 1}, {"low memory", 32 * comfytest.GiB, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, mbp)
			srv := r.remotes[mbp]
			srv.SetMemory(tc.memory)
			srv.CutDownloads(1)
			r.change(func(c *config.Config) { c.Local.Enabled = false })
			song := r.song("Song", "")
			r.run(r.queue())
			eventually(t, "the render ends", func() bool {
				s := r.load(song.ID).Status
				return s == library.StatusDone || s == library.StatusFailed
			})
			done := r.load(song.ID)
			if done.Status != library.StatusDone || done.JobID != "" || done.Error != "" {
				t.Fatalf("after a download cut off midway: %+v", done)
			}
			if got := srv.Prompts(); len(got) != tc.prompts {
				t.Fatalf("the render was sent again instead of fetched again: %v", got)
			}
			if got := takeMachines(done); !slices.Equal(got, []string{"song.flac@" + mbp}) {
				t.Fatalf("takes %v", got)
			}
			audio := r.store.AudioPath(done)
			if data, _ := os.ReadFile(audio); string(data) != "audio from MacBookPro" {
				t.Fatalf("audio %q", data)
			}
			if left, _ := filepath.Glob(filepath.Join(filepath.Dir(audio), ".audio-*")); len(left) != 0 {
				t.Fatalf("partial downloads left behind: %v", left)
			}
		})
	}
}

func TestADownloadCutOffMidwayOnThisComputerIsFetchedAgain(t *testing.T) {
	r := newRig(t)
	r.local.CutDownloads(1)
	song := r.song("Song", "")
	r.run(r.queue())
	eventually(t, "the render ends", func() bool {
		s := r.load(song.ID).Status
		return s == library.StatusDone || s == library.StatusFailed
	})
	done := r.load(song.ID)
	if done.Status != library.StatusDone || done.JobID != "" || done.Error != "" {
		t.Fatalf("after a download cut off midway: %+v", done)
	}
	if got := r.local.Prompts(); len(got) != 1 {
		t.Fatalf("the render was sent again instead of fetched again: %v", got)
	}
	if data, _ := os.ReadFile(r.store.AudioPath(done)); string(data) != "audio from local" {
		t.Fatalf("audio %q", data)
	}
}

func TestAudioThatCannotBeFetchedFailsTheSongAndKeepsItsJob(t *testing.T) {
	for _, tc := range []struct{ machine, where string }{{config.ThisComputer, "this computer"}, {mbp, mbp}} {
		t.Run(tc.machine, func(t *testing.T) {
			r := newRig(t, mbp)
			srv := r.local
			if tc.machine == mbp {
				srv = r.remotes[mbp]
			}
			srv.CutDownloads(1000)
			song := r.song("Song", tc.machine)
			stop := r.run(r.queue())
			eventually(t, "the render ends", func() bool {
				s := r.load(song.ID).Status
				return s == library.StatusDone || s == library.StatusFailed
			})
			failed := r.load(song.ID)
			if failed.Status != library.StatusFailed || failed.JobID == "" || !strings.Contains(failed.Error, "could not be fetched from "+tc.where) {
				t.Fatalf("after every download was cut off: %+v", failed)
			}
			stop()

			srv.CutDownloads(0)
			q := r.queue()
			r.run(q)
			if _, err := q.Enqueue(failed); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the song is done", func() bool { return r.load(song.ID).Status == library.StatusDone })
			if got := srv.Prompts(); len(got) != 1 {
				t.Fatalf("rendering it again sent the render again instead of fetching it: %v", got)
			}
			if done := r.load(song.ID); done.JobID != "" || done.Error != "" {
				t.Fatalf("done: %+v", done)
			}
		})
	}
}

func TestAReattachWhoseHistoryIsCutOffFetchesTheRenderInsteadOfSendingItAgain(t *testing.T) {
	for _, machine := range []string{config.ThisComputer, mbp} {
		for _, tc := range []struct {
			name    string
			memory  int64
			prompts int
		}{{"one prompt", 128 * comfytest.GiB, 1}, {"low memory", 32 * comfytest.GiB, 2}} {
			t.Run(machine+"/"+tc.name, func(t *testing.T) {
				r := newRig(t, mbp)
				srv := r.local
				if machine == mbp {
					srv = r.remotes[mbp]
				}
				if tc.prompts == 1 {
					r.change(func(c *config.Config) { c.Render.LowMemory = config.LowMemoryOff })
				}
				srv.SetMemory(tc.memory)
				srv.SetHold(true)
				song := r.song("Song", machine)
				stop := r.run(r.queue())
				id := queued(t, srv)
				r.job(song.ID)
				stop()

				srv.Finish(id)
				srv.SetHold(false)
				srv.CutHistory(1)
				r.run(r.queue())
				eventually(t, "the render ends", func() bool {
					s := r.load(song.ID).Status
					return s == library.StatusDone || s == library.StatusFailed
				})
				if done := r.load(song.ID); done.Status != library.StatusDone || done.Error != "" {
					t.Fatalf("after its history was cut off: %+v", done)
				}
				if got := srv.Prompts(); len(got) != tc.prompts {
					t.Fatalf("the render was sent again: %v", got)
				}
			})
		}
	}
}
