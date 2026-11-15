package queue

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

// Machine is a ComfyUI server songs render on. Each renders one song at a time.
type Machine struct {
	Name    string
	Backend backend.Backend
	// Enabled machines are sent new songs. A render a disabled machine
	// already holds is still picked up from it.
	Enabled bool
	// Local is this computer.
	Local bool
	// Ping reports whether a machine on another computer answers. It is
	// called before a song reattaches to a render there, while a render
	// runs there, and while a song waits for it to come back.
	Ping func(ctx context.Context) error
	// Ready reports whether a machine on another computer can take a new
	// song: it answers and has what the render needs. It is called before
	// a new song is sent there; nil means Ping.
	Ready func(ctx context.Context) error
}

// probe is the check a song makes before it goes to m: reattaching needs
// only an answer, a new song needs the machine ready. nil means none.
func (m Machine) probe(song library.Song) func(context.Context) error {
	if song.JobID == "" && m.Ready != nil {
		return m.Ready
	}
	return m.Ping
}

// Speeds measures how fast each machine renders.
type Speeds interface {
	// Speed is the seconds machine takes per second of audio, and false
	// before it has finished a render.
	Speed(machine string) (float64, bool)
	Record(machine string, took time.Duration, audioSeconds float64) error
}

// SetMachines makes the queue render on the machines fn returns. fn is
// called each time songs are handed out, so a change applies to the next
// song sent.
func (q *Queue) SetMachines(fn func() []Machine) { q.machines = fn }

// SetSpeeds has the queue prefer faster machines and measure every render
// that starts and finishes in one go.
func (q *Queue) SetSpeeds(s Speeds) { q.speeds = s }

// ReclaimAfter sets how long a song waits for a machine that stopped
// answering mid-render before it is queued again; fn is read each time.
func (q *Queue) ReclaimAfter(fn func() time.Duration) { q.reclaim = fn }

// render is a song reserved for, or rendering on, a machine.
type render struct {
	machine string
	local   bool
	// cancel is set once the song is claimed and its render starts.
	cancel context.CancelCauseFunc
}

// backoff is a machine that did not answer, left alone until until.
type backoff struct {
	until time.Time
	wait  time.Duration
}

// remoteName is what a song and a take record for m: its name, or "" for
// this computer.
func remoteName(m Machine) string {
	if m.Local {
		return ""
	}
	return m.Name
}

// sameMachine reports whether a pin and a recorded machine name the same
// machine, where "" is this computer.
func sameMachine(pin, recorded string) bool {
	if recorded == "" {
		recorded = config.ThisComputer
	}
	return strings.EqualFold(pin, recorded)
}

// machine finds the machine called name; "" is this computer.
func (q *Queue) machine(name string) (Machine, bool) {
	return find(q.machines(), name)
}

func find(ms []Machine, name string) (Machine, bool) {
	for _, m := range ms {
		if name == "" && m.Local || name != "" && strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Machine{}, false
}

// reserve holds m for song id, unless m or the song is already taken.
func (q *Queue) reserve(id string, m Machine) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.renders[id] != nil || q.busyMachine(m.Name) {
		return false
	}
	q.renders[id] = &render{machine: m.Name, local: m.Local}
	return true
}

func (q *Queue) release(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if r := q.renders[id]; r != nil && r.cancel == nil {
		delete(q.renders, id)
	}
}

// busyMachine reports whether a song holds machine. Call with q.mu held.
func (q *Queue) busyMachine(machine string) bool {
	for _, r := range q.renders {
		if strings.EqualFold(r.machine, machine) {
			return true
		}
	}
	return false
}

// markOffline leaves machine alone for a while, longer each time in a row
// it does not answer.
func (q *Queue) markOffline(machine string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.offline[machine]
	b.wait = min(max(b.wait*2, q.offlineMin), q.offlineMax)
	b.until = q.now().Add(b.wait)
	q.offline[machine] = b
}

func (q *Queue) markOnline(machine string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.offline, machine)
}

// resting reports whether machine did not answer recently. Call with q.mu held.
func (q *Queue) resting(machine string, now time.Time) bool {
	b, ok := q.offline[machine]
	return ok && now.Before(b.until)
}

// candidates lists the free machines song may go to now, best first: the
// machine holding its detached render, else the machine it is pinned to,
// else every enabled machine, those not yet measured before the slower
// ones. withResting includes machines that did not answer recently.
func (q *Queue) candidates(song library.Song, ms []Machine, withResting bool) []Machine {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	free := func(m Machine) bool {
		return !q.busyMachine(m.Name) && (withResting || !q.resting(m.Name, now))
	}
	one := func(m Machine, ok bool) []Machine {
		if ok && free(m) {
			return []Machine{m}
		}
		return nil
	}
	if song.JobID != "" {
		return one(find(ms, song.RenderingOn))
	}
	if song.Machine != "" {
		m, ok := find(ms, song.Machine)
		return one(m, ok && m.Enabled)
	}
	var out []Machine
	for _, m := range ms {
		if m.Enabled && free(m) {
			out = append(out, m)
		}
	}
	if q.speeds != nil {
		slices.SortStableFunc(out, func(a, b Machine) int {
			sa, oka := q.speeds.Speed(a.Name)
			sb, okb := q.speeds.Speed(b.Name)
			if !oka || !okb {
				return compareBool(!oka, !okb)
			}
			return cmp.Compare(sa, sb)
		})
	}
	return out
}

// compareBool sorts true before false.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// pick reserves the first machine that can take song now, checking that a
// machine on another computer answers first.
func (q *Queue) pick(ctx context.Context, song library.Song) (Machine, error) {
	if stored, err := q.store.Load(song.ID); err == nil {
		song = stored
	}
	ms := q.machines()
	var tried []string
	for _, m := range q.candidates(song, ms, true) {
		if probe := m.probe(song); probe != nil {
			if err := probe(ctx); err != nil {
				q.markOffline(m.Name)
				tried = append(tried, fmt.Sprintf("%s cannot take it: %v", m.Name, err))
				continue
			}
			q.markOnline(m.Name)
			q.stopOrphans(m)
		}
		if q.reserve(song.ID, m) {
			return m, nil
		}
	}
	if len(tried) > 0 {
		return Machine{}, fmt.Errorf("no machine could take %s: %s; it stays queued", song.Title, strings.Join(tried, "; "))
	}
	return Machine{}, fmt.Errorf("no machine can take %s now: %s; it stays queued", song.Title, q.whyNoMachine(song, ms))
}

// whyNoMachine says why song has no machine to go to.
func (q *Queue) whyNoMachine(song library.Song, ms []Machine) string {
	switch {
	case song.JobID != "":
		name := song.RenderingOn
		if _, ok := find(ms, name); !ok {
			return fmt.Sprintf("its render is on %s, which is no longer one of the machines", name)
		}
		return fmt.Sprintf("its render is on %s, which is busy", cmp.Or(name, config.ThisComputer))
	case song.Machine != "":
		m, ok := find(ms, song.Machine)
		switch {
		case !ok:
			return fmt.Sprintf("it is set to render on %s, which is not one of the machines", song.Machine)
		case !m.Enabled:
			return fmt.Sprintf("it is set to render on %s, which is turned off", m.Name)
		}
		return fmt.Sprintf("it is set to render on %s, which is busy", m.Name)
	case !slices.ContainsFunc(ms, func(m Machine) bool { return m.Enabled }):
		return "every machine is turned off"
	}
	return "every machine is busy"
}

// dispatch hands queued songs, oldest first, to free machines. Each send
// runs on its own, so a machine that is slow to answer holds up no other.
func (q *Queue) dispatch(ctx context.Context) {
	ms := q.machines()
	for _, song := range q.queued() {
		if q.unstick(&song, ms) {
			continue
		}
		cands := q.candidates(song, ms, false)
		if len(cands) == 0 {
			continue
		}
		m := cands[0]
		if !q.reserve(song.ID, m) {
			continue
		}
		q.running.Go(func() { q.send(ctx, song, m) })
	}
}

// send renders song on m, once m answers and, for a new song, is ready; if
// not, the song stays queued for another machine.
func (q *Queue) send(ctx context.Context, song library.Song, m Machine) {
	defer q.Wake()
	if probe := m.probe(song); probe != nil {
		if err := probe(ctx); err != nil {
			q.release(song.ID)
			if ctx.Err() == nil {
				q.markOffline(m.Name)
				if song.JobID != "" && song.LostSince.IsZero() {
					q.update(song.ID, func(s *library.Song) {
						if s.Status == library.StatusQueued && s.LostSince.IsZero() {
							s.LostSince = q.now()
						}
					})
				}
			}
			return
		}
		q.markOnline(m.Name)
		q.stopOrphans(m)
	}
	q.renderOn(ctx, song, m)
}

// orphan keeps jobID, let go on a machine that does not answer, to stop
// once the machine answers again.
func (q *Queue) orphan(machine, jobID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.orphans[machine] = append(q.orphans[machine], jobID)
}

// stopOrphans abandons the jobs let go on m while it did not answer.
func (q *Queue) stopOrphans(m Machine) {
	q.mu.Lock()
	jobs := q.orphans[m.Name]
	delete(q.orphans, m.Name)
	q.mu.Unlock()
	for _, id := range jobs {
		if abandon(m, id) != nil {
			q.orphan(m.Name, id)
		}
	}
}

// unstick settles a queued song that cannot go where it waits to: its
// detached render is on a machine no longer set up, or on one that has not
// answered for longer than ReclaimAfter. Either way the old render is let
// go and the song is queued afresh. It reports whether it changed the song.
func (q *Queue) unstick(song *library.Song, ms []Machine) bool {
	if song.JobID == "" {
		if song.Machine != "" {
			if _, ok := find(ms, song.Machine); !ok {
				msg := fmt.Sprintf("set to render on %s, which is not one of the machines; choose another machine for it", song.Machine)
				if song.Error != msg {
					q.update(song.ID, func(s *library.Song) { s.Error = msg })
				}
			}
		}
		return false
	}
	note := ""
	if m, ok := find(ms, song.RenderingOn); ok {
		if song.LostSince.IsZero() || q.now().Sub(song.LostSince) < q.reclaim() {
			return false
		}
		q.mu.Lock()
		resting := q.resting(m.Name, q.now())
		q.mu.Unlock()
		if !resting {
			return false
		}
		q.orphan(m.Name, song.JobID)
		note = fmt.Sprintf("%s did not answer for %s, so the song was queued again", m.Name, q.reclaim())
	}
	q.update(song.ID, func(s *library.Song) {
		if s.Status == library.StatusQueued && s.JobID == song.JobID {
			clearJob(s)
			s.Error = note
		}
	})
	q.Wake()
	return true
}

// update changes a stored song in place under the queue lock and publishes it.
func (q *Queue) update(id string, fn func(*library.Song)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	saved, err := q.store.Update(id, func(s *library.Song) error {
		fn(s)
		return nil
	})
	if err != nil {
		return
	}
	q.publish(Update{SongID: id, Status: saved.Status, Err: saved.Error})
}

type waitOutcome int

const (
	machineBack waitOutcome = iota
	machineGone
	waitStopped
)

// waitForMachine keeps song rendering on m while m does not answer, until
// m answers again, ReclaimAfter passes, or ctx ends. A cancelled song's
// render is abandoned on m, best effort.
func (q *Queue) waitForMachine(ctx context.Context, song *library.Song, m Machine) waitOutcome {
	if song.LostSince.IsZero() {
		song.LostSince = q.now()
	}
	q.save(*song)
	q.publish(Update{SongID: song.ID, Status: library.StatusRendering, Progress: backend.Progress{Stage: backend.StageWaiting}})
	wait := q.lostMin
	for {
		left := song.LostSince.Add(q.reclaim()).Sub(q.now())
		if left <= 0 {
			return machineGone
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), ErrCancelled) {
				q.orphan(m.Name, song.JobID)
			}
			return waitStopped
		case <-time.After(min(wait, left)):
		}
		if m.Ping(ctx) == nil {
			song.LostSince = time.Time{}
			q.save(*song)
			return machineBack
		}
		wait = min(wait*2, q.lostMax)
	}
}

// errSilent stops a render attempt on a machine that stopped answering
// without closing its connections, as a computer that went to sleep does.
// It is a detach, so the render keeps its job there.
var errSilent = fmt.Errorf("%w: the machine stopped answering", backend.ErrDetach)

// watch pings m while a render attempt runs there, and stops the attempt
// with errSilent once m misses two pings in a row.
func (q *Queue) watch(ctx context.Context, m Machine, stop context.CancelCauseFunc) {
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(q.liveEvery):
		}
		if m.Ping(ctx) == nil {
			missed = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if missed++; missed >= 2 {
			stop(errSilent)
			return
		}
	}
}

// generate runs one render attempt on m. On another computer it also
// watches that m keeps answering, and reports a silent machine as one that
// cannot be reached.
func (q *Queue) generate(ctx context.Context, m Machine, req backend.Request, progress func(backend.Progress)) (backend.Result, error) {
	if m.Local || m.Ping == nil {
		return m.Backend.Generate(ctx, req, progress)
	}
	attempt, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		q.watch(attempt, m, stop)
	}()
	res, err := m.Backend.Generate(attempt, req, progress)
	silent := errors.Is(context.Cause(attempt), errSilent) && ctx.Err() == nil
	stop(nil)
	<-watched
	if silent {
		err = fmt.Errorf("%s stopped answering: %w", m.Name, backend.ErrUnreachable)
	}
	return res, err
}
