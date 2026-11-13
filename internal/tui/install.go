package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/setup"
)

// Installer is what the TUI needs from internal/setup; *setup.Installer is one.
type Installer interface {
	Plan(ctx context.Context, cfg config.Config) setup.Plan
	InstallComfyUI(ctx context.Context, dir, logPath string, progress func(setup.Progress)) error
	Download(ctx context.Context, comfyDir string, f setup.ModelFile, progress func(setup.Progress)) error
	PullWriter(ctx context.Context, url, model string, progress func(setup.Progress)) error
}

// installTarget is one thing a job installs, with the settings it was
// started with: a job never follows a setting changed while it runs.
type installTarget struct {
	kind    setup.Kind
	name    string
	file    setup.ModelFile
	model   string
	dir     string
	logPath string
	url     string
}

func (t installTarget) key() string {
	switch t.kind {
	case setup.KindModel:
		return "model:" + t.file.Dir + "/" + t.file.Name
	case setup.KindWriter:
		return "writer:" + setup.ModelTag(t.model)
	}
	return "comfyui"
}

// resumable is true for downloads, which keep what they fetched when stopped.
func (t installTarget) resumable() bool { return t.kind != setup.KindComfyUI }

func comfyTarget(cfg config.Config) installTarget {
	return installTarget{kind: setup.KindComfyUI, name: "ComfyUI", dir: cfg.ComfyUI.Dir,
		logPath: filepath.Join(cfg.StateDir, "install.log")}
}

func modelTarget(cfg config.Config, f setup.ModelFile) installTarget {
	return installTarget{kind: setup.KindModel, name: f.Label, file: f, dir: cfg.ComfyUI.Dir}
}

func writerTarget(cfg config.Config, model string) installTarget {
	return installTarget{kind: setup.KindWriter, name: model, model: model, url: cfg.Ollama.URL}
}

// job is one running install. A cancelled job stays listed as stopping until
// it has let go of its files, so starting it again cannot collide with it.
type job struct {
	target   installTarget
	seq      int
	cancel   context.CancelFunc
	progress setup.Progress
	stopping bool
	// done runs in Update when the job ends, before the checks run again.
	done func(a *app, err error)
}

// jobsState lives on the root app, so installs keep running whichever
// screen is showing. Its maps and pointers are shared by every copy of the
// app, as Bubble Tea keeps only the latest one.
type jobsState struct {
	seq     int
	running map[string]*job
	order   []string
	// failed holds the full error of each target's last failed run.
	failed map[string]string
	seqn   *installSequence
	wg     *sync.WaitGroup
}

func newJobsState() jobsState {
	return jobsState{running: map[string]*job{}, failed: map[string]string{}, seqn: &installSequence{}, wg: &sync.WaitGroup{}}
}

// installSequence runs targets one after another and stops at the first
// that fails or is cancelled.
type installSequence struct {
	left    []installTarget
	total   int
	current string
}

func (s *installSequence) active() bool { return s.current != "" }

type (
	jobProgressMsg struct {
		key      string
		seq      int
		progress setup.Progress
	}
	jobDoneMsg struct {
		key string
		seq int
		err error
	}
)

func (jobProgressMsg) droppable() {}

func (a app) job(key string) (*job, bool) {
	j, ok := a.jobs.running[key]
	return j, ok
}

// runningJobs lists the jobs in the order they started.
func (a app) runningJobs() []*job {
	out := make([]*job, 0, len(a.jobs.order))
	for _, k := range a.jobs.order {
		if j, ok := a.jobs.running[k]; ok {
			out = append(out, j)
		}
	}
	return out
}

func (a app) downloading() bool {
	return slices.ContainsFunc(a.runningJobs(), func(j *job) bool { return j.target.resumable() })
}

// startJob runs t in the background, reporting through the bridge. A target
// that is already running is left alone.
func (a *app) startJob(t installTarget, done func(a *app, err error)) tea.Cmd {
	key := t.key()
	if _, ok := a.jobs.running[key]; ok || a.d.Installer == nil {
		return nil
	}
	a.jobs.seq++
	ctx, cancel := context.WithCancel(a.ctx)
	j := &job{target: t, seq: a.jobs.seq, cancel: cancel, done: done,
		progress: setup.Progress{Asset: t.name, Phase: setup.PhaseStart, Detail: "starting…"}}
	a.jobs.running[key] = j
	a.jobs.order = append(a.jobs.order, key)
	delete(a.jobs.failed, key)
	a.relayout()

	in, send, seq, wg := a.d.Installer, a.events.send, j.seq, a.jobs.wg
	wg.Add(1)
	run := func() {
		defer wg.Done()
		defer cancel()
		progress := func(p setup.Progress) { send(jobProgressMsg{key: key, seq: seq, progress: p}) }
		var err error
		switch t.kind {
		case setup.KindComfyUI:
			err = in.InstallComfyUI(ctx, t.dir, t.logPath, progress)
		case setup.KindModel:
			err = in.Download(ctx, t.dir, t.file, progress)
		case setup.KindWriter:
			err = in.PullWriter(ctx, t.url, t.model, progress)
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		send(jobDoneMsg{key: key, seq: seq, err: err})
	}
	// The command returns at once: the job reports through the bridge, so
	// nothing waits on it.
	return func() tea.Msg {
		go run()
		return nil
	}
}

// cancelJob asks a job to stop; it is listed as stopping until it ends.
func (a *app) cancelJob(key string) {
	j, ok := a.jobs.running[key]
	if !ok || j.stopping {
		return
	}
	j.stopping = true
	j.cancel()
	if a.jobs.seqn.current == key {
		a.jobs.seqn.left = nil
	}
}

func (a *app) cancelAllJobs() {
	a.jobs.seqn.left = nil
	for key := range a.jobs.running {
		a.cancelJob(key)
	}
}

// waitJobs waits up to limit for cancelled jobs to let go of their files.
func (s jobsState) waitJobs(limit time.Duration) {
	stopped := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(limit):
	}
}

func (a app) receiveJobProgress(msg jobProgressMsg) app {
	if j, ok := a.jobs.running[msg.key]; ok && j.seq == msg.seq && !j.stopping {
		j.progress = msg.progress
	}
	return a
}

func (a app) receiveJobDone(msg jobDoneMsg) (app, tea.Cmd) {
	j, ok := a.jobs.running[msg.key]
	if !ok || j.seq != msg.seq {
		return a, nil
	}
	delete(a.jobs.running, msg.key)
	a.jobs.order = slices.DeleteFunc(a.jobs.order, func(k string) bool { return k == msg.key })
	a.relayout()
	t := j.target
	stopped := j.stopping || errors.Is(msg.err, context.Canceled)
	inSequence := a.jobs.seqn.current == msg.key

	var cmds []tea.Cmd
	switch {
	case stopped:
		note := "Stopped " + t.name
		if t.resumable() {
			note += "; what it downloaded is kept and the next install resumes it"
		}
		a.jobNote(note, false)
	case msg.err != nil:
		a.jobs.failed[msg.key] = msg.err.Error()
		a.jobNote(t.name+" did not install; the reason is under its row in Settings → Setup", true)
	default:
		a.jobNote(installedNote(t), false)
	}
	if j.done != nil {
		j.done(&a, msg.err)
	}
	if inSequence {
		a.jobs.seqn.current = ""
		if msg.err == nil && !stopped {
			cmds = append(cmds, a.nextInSequence())
		} else {
			a.jobs.seqn.left = nil
		}
	}
	next, health := a.startHealth()
	a = next
	cmds = append(cmds, health)
	if t.kind == setup.KindWriter {
		cmds = append(cmds, a.loadModels())
	}
	a.refreshVariants()
	a.refreshComfyFound()
	return a, tea.Batch(cmds...)
}

func installedNote(t installTarget) string {
	switch t.kind {
	case setup.KindWriter:
		return "Pulled " + t.model + " into Ollama"
	case setup.KindComfyUI:
		return "ComfyUI is installed in " + t.dir
	}
	return "Installed " + t.name + " (" + t.file.Name + ", verified)"
}

// jobNote reports a job's outcome on Settings' status line and on the launcher.
func (a *app) jobNote(text string, failed bool) {
	if failed {
		a.settingsError(text)
		a.notice, a.err = "", text
		return
	}
	a.settingsNote(text)
	a.notice, a.err = text, ""
}

// startSequence installs targets one after another.
func (a *app) startSequence(targets []installTarget) tea.Cmd {
	s := a.jobs.seqn
	if s.active() {
		return nil
	}
	s.left, s.total = targets, len(targets)
	return a.nextInSequence()
}

func (a *app) nextInSequence() tea.Cmd {
	s := a.jobs.seqn
	for len(s.left) > 0 {
		t := s.left[0]
		s.left = s.left[1:]
		if _, running := a.jobs.running[t.key()]; running {
			continue
		}
		s.current = t.key()
		return a.startJob(t, nil)
	}
	s.current, s.total = "", 0
	return nil
}

// jobPercent is a job's progress as a whole percentage, or -1 when its
// phase has no byte count.
func jobPercent(p setup.Progress) int {
	if p.Total <= 0 {
		return -1
	}
	return int(p.Percent)
}

// jobStatus is a few words on where a job is.
func jobStatus(j *job) string {
	if j.stopping {
		return "stopping…"
	}
	p := j.progress
	verb := "downloading"
	switch j.target.kind {
	case setup.KindComfyUI:
		verb = "installing"
	case setup.KindWriter:
		verb = "pulling"
	}
	switch p.Phase {
	case setup.PhaseVerify:
		verb = "verifying"
	case setup.PhaseInstall:
		if j.target.kind == setup.KindWriter {
			verb = "finishing"
		}
	}
	if pct := jobPercent(p); pct >= 0 {
		return fmt.Sprintf("%s %d%%", verb, pct)
	}
	return verb + "…"
}

// jobDetail is the size done so far, or the installer's latest line.
func jobDetail(j *job) string {
	p := j.progress
	if p.Total > 0 && j.target.kind != setup.KindComfyUI {
		return fmt.Sprintf("%s of %s", setup.GB(p.Bytes), setup.GB(p.Total))
	}
	return p.Detail
}

// downloadNote is the footer line every screen shows while a download runs.
const downloadNote = "A download is running. Quitting keeps what it fetched, and the next run resumes it."

// relayout sizes every screen to the terminal less the download note.
func (a *app) relayout() {
	a.height = a.termHeight
	if a.downloading() && a.termHeight > 6 {
		a.height--
	}
	if a.width == 0 {
		return
	}
	a.compose.resize(a.width, a.height)
	a.layoutSettingsText()
	a.layoutDetail()
}
