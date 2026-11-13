package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

// masterAsync marks results of background work started from the Master
// screen. The app hands them over whichever screen is showing.
type masterAsync interface{ masterAsync() }

type (
	// measuredMsg is Analyze of the take being worked on.
	measuredMsg struct {
		seq     int
		measure master.Measure
		err     error
	}
	// previewDueMsg starts the preview seq once its debounce has passed.
	previewDueMsg struct{ seq int }
	// previewDoneMsg is a finished pair of excerpts: the master and the
	// original, cut at the same place.
	previewDoneMsg struct {
		seq                  int
		file, orig           string
		measure, origMeasure master.Measure
		err                  error
	}
	masterProgressMsg struct {
		songID string
		seq    int
		done   float64
	}
	masterDoneMsg struct {
		songID  string
		seq     int
		take    library.Take
		measure master.Measure
		err     error
	}
)

func (measuredMsg) masterAsync()       {}
func (previewDueMsg) masterAsync()     {}
func (previewDoneMsg) masterAsync()    {}
func (masterProgressMsg) masterAsync() {}
func (masterDoneMsg) masterAsync()     {}
func (masterProgressMsg) droppable()   {}

const (
	// previewSeconds is how long a preview excerpt runs.
	previewSeconds = 30
	// previewDebounce is how long settings must stay still before a
	// preview is prepared for them.
	previewDebounce = 350 * time.Millisecond
)

// scratch is the per-song folder previews are written to, and the files
// this screen made there: only those are ever deleted.
type scratch struct {
	mu      sync.Mutex
	dir     string
	made    bool
	created []string
}

func newScratch(dir string) (*scratch, error) {
	s := &scratch{dir: dir}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		s.made = true
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("make a folder for previews: %w", err)
	}
	return s, nil
}

// file names a new file in the folder and remembers it for cleanup.
func (s *scratch) file(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, name)
	s.created = append(s.created, p)
	return p
}

// owns reports whether path is a file this screen made.
func (s *scratch) owns(path string) bool {
	if s == nil || path == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.created {
		if p == path {
			return true
		}
	}
	return false
}

// prune deletes the files this screen made except keep, such as the
// excerpts of earlier settings once newer ones are ready.
func (s *scratch) prune(keep ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []string
	for _, p := range s.created {
		if slices.Contains(keep, p) {
			kept = append(kept, p)
			continue
		}
		_ = os.Remove(p)
	}
	s.created = kept
}

// clean deletes the files this screen made, and the folder when this screen
// made it and it is empty.
func (s *scratch) clean() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.created {
		_ = os.Remove(p)
	}
	s.created = nil
	if s.made {
		_ = os.Remove(s.dir)
	}
}

// startMeasure analyzes the take being worked on.
func (a *app) startMeasure() tea.Cmd {
	m := &a.master
	m.measureSeq++
	m.measuring, m.measureErr = true, ""
	seq, run, ctx, path := m.measureSeq, a.d.Master, a.ctx, a.masterTakePath()
	store, id, take := a.d.Store, m.songID, m.take
	return a.events.run(func(send func(tea.Msg)) {
		meas, err := run.Analyze(ctx, path)
		if err == nil {
			// Lists show the take's length and loudness from now on.
			_ = store.RecordMeasure(id, take, meas.Duration, meas.Loudness)
		}
		send(measuredMsg{seq: seq, measure: meas, err: err})
	})
}

// schedulePreview asks for a new preview once the settings and position
// have been still for previewDebounce. A preview being prepared is
// cancelled now; its result, if any arrives, is dropped.
func (a *app) schedulePreview() tea.Cmd {
	m := &a.master
	if m.defaults || m.unavailable != nil {
		return nil
	}
	m.preview.seq++
	if m.preview.cancel != nil {
		m.preview.cancel()
		m.preview.cancel = nil
	}
	m.preview.preparing, m.preview.err = true, ""
	m.preview.file, m.preview.orig = "", ""
	seq := m.preview.seq
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg { return previewDueMsg{seq: seq} })
}

// startPreview prepares preview seq: the master and the original excerpt.
func (a *app) startPreview(seq int) tea.Cmd {
	m := &a.master
	if seq != m.preview.seq || m.scratch == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(a.ctx)
	m.preview.cancel = cancel
	run, in, s, start := a.d.Master, a.masterTakePath(), m.settings, m.position
	file := m.scratch.file(fmt.Sprintf("preview-%d.flac", seq))
	orig := m.scratch.file(fmt.Sprintf("original-%d.flac", seq))
	return a.events.run(func(send func(tea.Msg)) {
		defer cancel()
		msg := previewDoneMsg{seq: seq, file: file, orig: orig}
		msg.measure, msg.err = run.Preview(ctx, in, file, s, start, previewSeconds)
		if msg.err == nil {
			msg.origMeasure, msg.err = run.Preview(ctx, in, orig, master.Original(s), start, previewSeconds)
		}
		if ctx.Err() != nil {
			msg.err = ctx.Err()
		}
		send(msg)
	})
}

// masterJob is one take being mastered. Jobs belong to the app, so they keep
// running when the Master screen is left; the map is shared by every copy
// of the app.
type masterJob struct {
	seq      int
	cancel   context.CancelFunc
	source   string
	done     float64
	stopping bool
}

type masterJobs struct {
	seq     *int
	running map[string]*masterJob
}

func newMasterJobs() masterJobs {
	return masterJobs{seq: new(int), running: map[string]*masterJob{}}
}

// startMastering masters the take being worked on into a new take.
func (a *app) startMastering() tea.Cmd {
	m := &a.master
	if _, ok := a.mastering.running[m.songID]; ok {
		a.masterNote("This take is already being mastered.")
		return nil
	}
	if err := m.settings.Check(); err != nil {
		a.masterError("Can't master with these settings: " + err.Error())
		return nil
	}
	*a.mastering.seq++
	ctx, cancel := context.WithCancel(a.ctx)
	j := &masterJob{seq: *a.mastering.seq, cancel: cancel, source: m.take}
	a.mastering.running[m.songID] = j
	store, run, id, source, s, send, wg := a.d.Store, a.d.Master, m.songID, m.take, m.settings, a.events.send, a.jobs.wg
	seq := j.seq
	a.masterNote("Mastering " + source + " into a new take…")
	// The cursor goes to Cancel, under the progress.
	m.mark = lineAnchor{set: true, name: "cancel master", option: -1}
	wg.Add(1)
	work := func() {
		defer wg.Done()
		defer cancel()
		var measure master.Measure
		render := func(ctx context.Context, in, out string, s master.Settings, progress func(float64)) (master.Measure, error) {
			var err error
			measure, err = run.Render(ctx, in, out, s, progress)
			return measure, err
		}
		progress := func(f float64) { send(masterProgressMsg{songID: id, seq: seq, done: f}) }
		take, _, err := store.MasterTake(ctx, id, source, s, render, progress)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		send(masterDoneMsg{songID: id, seq: seq, take: take, measure: measure, err: err})
	}
	return func() tea.Msg {
		go work()
		return nil
	}
}

func (a *app) cancelMastering(songID string) {
	if j, ok := a.mastering.running[songID]; ok && !j.stopping {
		j.stopping = true
		j.cancel()
	}
}

// receiveMaster applies a background result from the Master screen, on
// whichever screen is showing.
func (a app) receiveMaster(msg masterAsync) (app, tea.Cmd) {
	m := &a.master
	switch msg := msg.(type) {
	case measuredMsg:
		if msg.seq != m.measureSeq {
			return a, nil
		}
		m.measuring = false
		if msg.err != nil {
			m.measureErr = msg.err.Error()
			return a, nil
		}
		m.before, m.measured = msg.measure, true
		m.position = min(m.position, max(m.settings.Length(m.before.Info)-1, 0))
		return a, a.loadSongs()
	case previewDueMsg:
		return a, a.startPreview(msg.seq)
	case previewDoneMsg:
		return a.receivePreview(msg)
	case masterProgressMsg:
		if j, ok := a.mastering.running[msg.songID]; ok && j.seq == msg.seq && !j.stopping {
			j.done = msg.done
			a.layoutDetail()
		}
	case masterDoneMsg:
		return a.receiveMastered(msg)
	}
	return a, nil
}

func (a app) receivePreview(msg previewDoneMsg) (app, tea.Cmd) {
	m := &a.master
	if msg.seq != m.preview.seq || a.screen != screenMaster {
		// A newer preview replaced this one; its files are not played.
		if p := a.d.Player.Current(); p != msg.file && p != msg.orig {
			_ = os.Remove(msg.file)
			_ = os.Remove(msg.orig)
		}
		return a, nil
	}
	m.preview.preparing, m.preview.cancel = false, nil
	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) {
			m.preview.err = msg.err.Error()
		}
		return a, nil
	}
	m.preview.file, m.preview.orig = msg.file, msg.orig
	m.scratch.prune(msg.file, msg.orig, a.d.Player.Current())
	m.after, m.afterOrig, m.afterScope = msg.measure, msg.origMeasure, scopeExcerpt
	m.afterAt = m.position
	// Whatever was playing goes on from the new excerpt; a key pressed
	// while it was being prepared plays now.
	want := m.preview.pending
	if cur := a.d.Player.Current(); want == noExcerpt && m.scratch.owns(cur) {
		want = kindOf(cur)
	}
	m.preview.pending = noExcerpt
	if want != noExcerpt {
		a.playExcerpt(want)
	}
	return a, nil
}

func (a app) receiveMastered(msg masterDoneMsg) (app, tea.Cmd) {
	j, ok := a.mastering.running[msg.songID]
	if !ok || j.seq != msg.seq {
		return a, nil
	}
	delete(a.mastering.running, msg.songID)
	stopped := j.stopping || errors.Is(msg.err, context.Canceled)
	title := a.songTitle(msg.songID)
	here := a.screen == screenMaster && a.master.songID == msg.songID
	switch {
	case stopped:
		a.masterNote("Stopped mastering; nothing was saved.")
		if !here {
			a.notice, a.err = "Stopped mastering "+title+".", ""
		}
	case msg.err != nil:
		a.masterError("Mastering failed: " + msg.err.Error())
		if !here {
			a.notice, a.err = "", "Mastering "+title+" failed: "+msg.err.Error()
		}
	default:
		if here {
			m := &a.master
			m.made = msg.take.File
			m.after, m.afterScope = msg.measure, scopeWhole
			m.mark = lineAnchor{set: true, name: "make current", option: -1}
			a.masterNote("Saved " + msg.take.File + " as a new take. The library still plays " + a.currentTakeName() + " until you make the new one current.")
		} else {
			a.notice, a.err = "Mastered "+title+" into "+msg.take.File+"; t switches the take the library plays.", ""
		}
	}
	a.layoutDetail()
	return a, a.loadSongs()
}
