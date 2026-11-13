package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/setup"
)

// fakeRun is one call into fakeInstaller. The test reports progress through
// it and ends it with finish; until then the call blocks like a download.
type fakeRun struct {
	key      string
	ctx      context.Context
	progress func(setup.Progress)
	result   chan error
}

func (r *fakeRun) finish(err error) { r.result <- err }

// fakeInstaller stands in for internal/setup, so no test installs anything.
type fakeInstaller struct {
	mu      sync.Mutex
	plan    setup.Plan
	started []string
	runs    chan *fakeRun
	// onDone runs when a call ends without being cancelled, as the real
	// installer would leave files behind.
	onDone func(key string, err error)
}

func newFakeInstaller(plan setup.Plan) *fakeInstaller {
	return &fakeInstaller{plan: plan, runs: make(chan *fakeRun, 16)}
}

func (f *fakeInstaller) Plan(context.Context, config.Config) setup.Plan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plan
}

func (f *fakeInstaller) setPlan(p setup.Plan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plan = p
}

func (f *fakeInstaller) call(ctx context.Context, key string, progress func(setup.Progress)) error {
	f.mu.Lock()
	f.started = append(f.started, key)
	f.mu.Unlock()
	r := &fakeRun{key: key, ctx: ctx, progress: progress, result: make(chan error, 1)}
	f.runs <- r
	select {
	case err := <-r.result:
		if f.onDone != nil {
			f.onDone(key, err)
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeInstaller) InstallComfyUI(ctx context.Context, _, _ string, progress func(setup.Progress)) error {
	return f.call(ctx, "comfyui", progress)
}

func (f *fakeInstaller) Download(ctx context.Context, _ string, file setup.ModelFile, progress func(setup.Progress)) error {
	return f.call(ctx, "model:"+file.Dir+"/"+file.Name, progress)
}

func (f *fakeInstaller) PullWriter(ctx context.Context, _, model string, progress func(setup.Progress)) error {
	return f.call(ctx, "writer:"+setup.ModelTag(model), progress)
}

func (f *fakeInstaller) startedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

// next waits for the next call to start.
func (f *fakeInstaller) next(t *testing.T) *fakeRun {
	t.Helper()
	select {
	case r := <-f.runs:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no install started")
		return nil
	}
}

func (f *fakeInstaller) noneStarted(t *testing.T) {
	t.Helper()
	select {
	case r := <-f.runs:
		t.Fatalf("%s started", r.key)
	case <-time.After(50 * time.Millisecond):
	}
}

func manifestFile(dir, name string) setup.ModelFile {
	f, _ := setup.Lookup(dir, name)
	return f
}

// missingPlan is the plan on a machine with nothing installed and Ollama
// running without the writer model.
func missingPlan(cfg config.Config) setup.Plan {
	dit := manifestFile(setup.DirDiffusion, cfg.ComfyUI.DiffusionModel)
	te := manifestFile(setup.DirTextEncoder, cfg.ComfyUI.TextEncoder)
	vae := manifestFile(setup.DirVAE, cfg.ComfyUI.VAE)
	return setup.Plan{Actions: []setup.Action{
		{ID: setup.ActionComfyUI, Kind: setup.KindComfyUI, Name: "ComfyUI", Status: setup.StatusMissing, Required: true, DownloadBytes: 3e9, Detail: "not installed"},
		{ID: setup.ActionDiffusion, Kind: setup.KindModel, Name: "Diffusion model", Status: setup.StatusMissing, Required: true, File: &dit, DownloadBytes: dit.Size},
		{ID: setup.ActionTextEncoder, Kind: setup.KindModel, Name: "Text encoder", Status: setup.StatusMissing, Required: true, File: &te, DownloadBytes: te.Size},
		{ID: setup.ActionVAE, Kind: setup.KindModel, Name: "VAE", Status: setup.StatusMissing, Required: true, File: &vae, DownloadBytes: vae.Size},
		{ID: setup.ActionWriter, Kind: setup.KindWriter, Name: "Writer model", Status: setup.StatusMissing, Model: cfg.Ollama.Model},
	}}
}

// missingChecks match missingPlan.
func missingChecks() []health.Check {
	checks := nothingInstalled()
	checks[5] = health.Check{ID: health.IDWriter, Name: "Ollama", Result: health.Warn, Fix: health.FixWriterModel,
		Missing: "writer model", Detail: "model is not installed"}
	return checks
}

// newInstallApp is Settings → Setup with a fake installer whose plan says
// everything is missing. The ComfyUI folder holds a checkout unless bare.
func newInstallApp(t *testing.T, bare bool) (app, *fakeInstaller) {
	t.Helper()
	a, _ := newSettingsApp(t, missingChecks())
	cfg := a.cfg()
	if !bare {
		sparseFile(t, filepath.Join(cfg.ComfyUI.Dir, "main.py"), 1)
	}
	fake := newFakeInstaller(missingPlan(cfg))
	a.d.Installer = fake
	a = finishHealth(t, a)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 140, Height: 60})
	return openSettingsOn(t, a, sectionSetup), fake
}

// pump delivers what background work has sent, until done reports true.
func pump(t *testing.T, a app, done func(app) bool) app {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done(a) {
		msg, ok := a.events.next()
		if !ok {
			if time.Now().After(deadline) {
				t.Fatal("pump: timed out")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		var cmd tea.Cmd
		a, cmd = send(t, a, msg)
		for _, m := range runCmds(cmd) {
			a, _ = send(t, a, m)
		}
	}
	return a
}

func noJob(key string) func(app) bool {
	return func(a app) bool { _, ok := a.job(key); return !ok }
}

// hit sends one key and runs what it returns without waiting on a job.
func hit(t *testing.T, a app, k string) app {
	t.Helper()
	a, cmd := send(t, a, settingsKey(k))
	for _, m := range runCmds(cmd) {
		a, _ = send(t, a, m)
	}
	return a
}

const vaeKey = "model:vae/minimax_music3_dav.safetensors"

func TestSetupRowStartsItsInstallAndKeepsReportingAcrossScreens(t *testing.T) {
	a, fake := newInstallApp(t, false)
	a = focus(t, a, "VAE", -1)
	if !strings.Contains(plain(a), "enter downloads 0.22 GB") || !strings.Contains(plain(a), "enter install") {
		t.Fatalf("install row:\n%s", plain(a))
	}
	a = hit(t, a, "enter")
	run := fake.next(t)
	if run.key != vaeKey {
		t.Fatalf("started %s", run.key)
	}
	a = hit(t, a, "enter")
	fake.noneStarted(t)

	a = hit(t, a, "esc")
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	run.progress(setup.Progress{Phase: setup.PhaseDownload, Bytes: 34, Total: 100, Percent: 34})
	a = pump(t, a, func(a app) bool { j, _ := a.job(vaeKey); return j.progress.Percent == 34 })
	if !strings.Contains(plain(a), "Music model downloading 34%") {
		t.Fatalf("launcher chip:\n%s", plain(a))
	}
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	run.progress(setup.Progress{Phase: setup.PhaseDownload, Bytes: 61, Total: 100, Percent: 61})
	a = pump(t, a, func(a app) bool { j, _ := a.job(vaeKey); return j.progress.Percent == 61 })
	if !strings.Contains(plain(a), downloadNote) {
		t.Fatalf("library footer:\n%s", plain(a))
	}
	a = openSettingsOn(t, a, sectionSetup)
	if !strings.Contains(plain(a), "downloading 61%") || !strings.Contains(plain(a), "stop VAE  what is downloaded is kept") {
		t.Fatalf("Setup while downloading:\n%s", plain(a))
	}

	healthRuns := a.health.seq
	run.finish(nil)
	a = pump(t, a, noJob(vaeKey))
	if a.health.seq != healthRuns+1 || !a.health.running {
		t.Fatal("finishing an install did not re-run the checks")
	}
	if !strings.Contains(a.settings.message, "Installed VAE") || strings.Contains(plain(a), downloadNote) {
		t.Fatalf("after:\n%s", plain(a))
	}
}

func TestCancelStopsTheJobKeepsItsPartAndIgnoresLateProgress(t *testing.T) {
	a, fake := newInstallApp(t, false)
	part := setup.PartPath(a.cfg().ComfyUI.Dir, manifestFile(setup.DirVAE, a.cfg().ComfyUI.VAE))
	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	run := fake.next(t)
	sparseFile(t, part, 1000)
	run.progress(setup.Progress{Phase: setup.PhaseDownload, Bytes: 10, Total: 100, Percent: 10})
	a = pump(t, a, func(a app) bool { j, _ := a.job(vaeKey); return j.progress.Percent == 10 })
	seq := a.jobs.running[vaeKey].seq

	a = focus(t, a, "Cancel", -1)
	a = hit(t, a, "enter")
	if run.ctx.Err() == nil {
		t.Fatal("cancel did not stop the install")
	}
	a, _ = send(t, a, jobProgressMsg{key: vaeKey, seq: seq, progress: setup.Progress{Bytes: 90, Total: 100, Percent: 90}})
	if j, _ := a.job(vaeKey); !j.stopping || j.progress.Percent != 10 || !strings.Contains(plain(a), "stopping…") {
		t.Fatalf("late progress shown or not stopping:\n%s", plain(a))
	}
	a = focus(t, a, "Diffusion model", -1)
	a = pump(t, a, noJob(vaeKey))
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("the partial download is gone: %v", err)
	}
	if !strings.Contains(a.settings.message, "Stopped VAE; what it downloaded is kept") {
		t.Fatalf("message %q", a.settings.message)
	}
	a, _ = send(t, a, jobProgressMsg{key: vaeKey, seq: seq, progress: setup.Progress{Percent: 95}})
	a, _ = send(t, a, jobDoneMsg{key: vaeKey, seq: seq})
	if _, ok := a.job(vaeKey); ok || strings.Contains(a.settings.message, "Installed") {
		t.Fatal("a finished job's messages changed the state")
	}

	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	again := fake.next(t)
	a, _ = send(t, a, jobProgressMsg{key: vaeKey, seq: seq, progress: setup.Progress{Percent: 77, Total: 100}})
	if j, _ := a.job(vaeKey); j.progress.Percent == 77 {
		t.Fatal("progress from the cancelled run reached the new one")
	}
	again.finish(nil)
	pump(t, a, noJob(vaeKey))
}

func TestQuitCancelsRunningJobs(t *testing.T) {
	a, fake := newInstallApp(t, false)
	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	run := fake.next(t)
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	a, cmd := send(t, a, key("q"))
	a, cmd = send(t, a, cmd())
	if !isQuit(cmd) || run.ctx.Err() == nil {
		t.Fatal("quitting left the download running")
	}
	a.jobs.waitJobs(5 * time.Second)
}

func TestBrowsingStartsNothing(t *testing.T) {
	a, fake := newInstallApp(t, false)
	for range 30 {
		a = hit(t, a, "down")
	}
	for range 30 {
		a = hit(t, a, "up")
	}
	for _, sec := range []string{"2", "3", "6", "1"} {
		a = hit(t, a, sec)
		for range 10 {
			a = hit(t, a, "down")
		}
	}
	fake.noneStarted(t)
}

func TestInstallEverythingRunsThePlanInOrderAndStopsAtAFailure(t *testing.T) {
	a, fake := newInstallApp(t, true)
	a = focus(t, a, "Install everything missing", -1)
	if !strings.Contains(plain(a), "5 items · about 24.8 GB") {
		t.Fatalf("total:\n%s", plain(a))
	}
	a = hit(t, a, "enter")
	want := []string{"comfyui", "model:diffusion_models/minimax_music3_dit_fp16.safetensors"}
	for i, key := range want {
		run := fake.next(t)
		if run.key != key {
			t.Fatalf("step %d started %s, want %s", i, run.key, key)
		}
		if !strings.Contains(plain(a), "Install everything missing") || !strings.Contains(plain(a), "Cancel everything") {
			t.Fatalf("no progress row:\n%s", plain(a))
		}
		if i == 0 {
			run.finish(nil)
			a = pump(t, a, func(a app) bool { _, ok := a.job(want[1]); return ok })
			continue
		}
		run.finish(errors.New("huggingface.co answered HTTP 503 for diffusion_models/minimax_music3_dit_fp16.safetensors, a long reason that must not be cut short"))
		a = pump(t, a, noJob(key))
	}
	fake.noneStarted(t)
	if a.jobs.seqn.active() {
		t.Fatal("the sequence carried on after a failure")
	}
	if view := strings.Join(strings.Fields(ansi.Strip(strings.Join(sectionText(a), " "))), " "); !strings.Contains(view, "Last try failed: huggingface.co answered HTTP 503 for diffusion_models/minimax_music3_dit_fp16.safetensors, a long reason that must not be cut short") {
		t.Fatalf("full error not shown:\n%s", plain(a))
	}
}

func TestCancelEverythingStopsTheRest(t *testing.T) {
	a, fake := newInstallApp(t, false)
	a = focus(t, a, "Install everything missing", -1)
	a = hit(t, a, "enter")
	run := fake.next(t)
	a = focus(t, a, "Cancel everything", -1)
	a = hit(t, a, "enter")
	if run.ctx.Err() == nil {
		t.Fatal("not cancelled")
	}
	a = pump(t, a, noJob(run.key))
	fake.noneStarted(t)
}

func TestARowSetupCannotInstallShowsWhyAndIgnoresEnter(t *testing.T) {
	a, fake := newInstallApp(t, true)
	p := missingPlan(a.cfg())
	reason := "git is not installed; songsmith needs it to download ComfyUI. Install it with `xcode-select --install` (or `brew install git`), then run setup again."
	p.Actions[0].Status, p.Actions[0].Detail = setup.StatusUnavailable, reason
	fake.setPlan(p)
	a = pump(t, finishHealth(t, a), func(app) bool { return true })
	a = focus(t, a, "ComfyUI", -1)
	if row, _, _ := a.focusedSetting(); row.kind != rowInfo || row.run != nil {
		t.Fatalf("unavailable row is actionable: %+v", row)
	}
	a = hit(t, a, "enter")
	if !strings.Contains(strings.ReplaceAll(plain(a), "\n", " "), "then run setup again.") || !strings.Contains(plain(a), "Cannot install: git is not installed") {
		t.Fatalf("reason not shown in full:\n%s", plain(a))
	}
	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	if !strings.Contains(a.settings.message, "cannot be installed here") {
		t.Fatalf("message %q", a.settings.message)
	}
	fake.noneStarted(t)
}

func TestAModelRowInstallsComfyUIFirstWhenItIsMissing(t *testing.T) {
	a, fake := newInstallApp(t, true)
	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	if run := fake.next(t); run.key != "comfyui" {
		t.Fatalf("started %s", run.key)
	} else {
		sparseFile(t, filepath.Join(a.cfg().ComfyUI.Dir, "main.py"), 1)
		run.finish(nil)
	}
	a = pump(t, a, func(a app) bool { _, ok := a.job(vaeKey); return ok })
	if run := fake.next(t); run.key != vaeKey {
		t.Fatalf("then started %s", run.key)
	} else {
		run.finish(nil)
	}
	pump(t, a, noJob(vaeKey))
}

func TestWriterPullSavesOnlyWhenItSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		end    func(a app, r *fakeRun) app
		saved  bool
		reason string
	}{
		{"success", func(a app, r *fakeRun) app { r.finish(nil); return a }, true, ""},
		{"failure", func(a app, r *fakeRun) app {
			r.finish(errors.New("pull model manifest: file does not exist"))
			return a
		}, false, "file does not exist"},
		{"cancel", func(a app, r *fakeRun) app {
			a = focus(t, a, "Cancel", -1)
			return hit(t, a, "enter")
		}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newInstallApp(t, false)
			a = openSettingsOn(t, a, sectionWriter)
			saves := recordSaves(&a)
			a = focus(t, a, "Other model", -1)
			a = hit(t, a, "enter")
			a = typeIntoSettings(t, a, "newcomer:2b")
			a = hit(t, a, "enter")
			run := fake.next(t)
			if run.key != "writer:newcomer:2b" || len(*saves) != 0 {
				t.Fatalf("started %s, saves %v", run.key, *saves)
			}
			run.progress(setup.Progress{Phase: setup.PhaseDownload, Bytes: 5, Total: 10, Percent: 50})
			a = pump(t, a, func(a app) bool { j, _ := a.job(run.key); return j.progress.Percent == 50 })
			if !strings.Contains(plain(a), "Pulling newcomer:2b") || !strings.Contains(a.launcherState().writerJob, "pulling 50%") {
				t.Fatalf("pull progress:\n%s", plain(a))
			}
			a = tc.end(a, run)
			a = pump(t, a, noJob(run.key))
			got := slices.Contains(*saves, savedKV{"ollama.model", "newcomer:2b"})
			if got != tc.saved || (a.cfg().Ollama.Model == "newcomer:2b") != tc.saved {
				t.Fatalf("saved %v, model %q", *saves, a.cfg().Ollama.Model)
			}
			if tc.reason != "" && !strings.Contains(plain(a), tc.reason) {
				t.Fatalf("reason missing:\n%s", plain(a))
			}
		})
	}
}

func TestChoosingAVariantNotOnDiskDownloadsItAndSavesOnlyOnceVerified(t *testing.T) {
	for _, ok := range []bool{true, false} {
		a, fake := newInstallApp(t, false)
		cfg := a.cfg()
		fp32 := manifestFile(setup.DirDiffusion, "minimax_music3_dit_fp32.safetensors")
		fake.onDone = func(_ string, err error) {
			if err == nil {
				sparseFile(t, filepath.Join(cfg.ComfyUI.Dir, "models", fp32.Dir, fp32.Name), fp32.Size)
			}
		}
		a = openSettingsOn(t, a, sectionMusicModel)
		saves := recordSaves(&a)
		a = focus(t, a, "Diffusion model", 1)
		a = hit(t, a, "enter")
		run := fake.next(t)
		if run.key != "model:diffusion_models/"+fp32.Name || len(*saves) != 0 {
			t.Fatalf("started %s, saves %v", run.key, *saves)
		}
		run.progress(setup.Progress{Phase: setup.PhaseDownload, Bytes: 1, Total: 4, Percent: 25})
		a = pump(t, a, func(a app) bool { j, _ := a.job(run.key); return j.progress.Percent == 25 })
		if !strings.Contains(plain(a), "fp32 · 9.8 GB · downloading 25%") || !strings.Contains(plain(a), "stop DiT fp32") {
			t.Fatalf("while downloading:\n%s", plain(a))
		}
		if ok {
			run.finish(nil)
		} else {
			run.finish(errors.New("minimax_music3_dit_fp32.safetensors failed verification"))
		}
		a = pump(t, a, noJob(run.key))
		saved := slices.Contains(*saves, savedKV{"comfyui.diffusion_model", fp32.Name})
		if saved != ok || (a.cfg().ComfyUI.DiffusionModel == fp32.Name) != ok {
			t.Fatalf("download ok=%v: saves %v", ok, *saves)
		}
		if ok && !strings.Contains(plain(a), "✓ fp32 · 9.8 GB · installed") {
			t.Fatalf("after:\n%s", plain(a))
		}
	}
}

func TestEngineOffersToInstallComfyUIWithItsLog(t *testing.T) {
	a, fake := newInstallApp(t, true)
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Install ComfyUI", -1)
	a = hit(t, a, "enter")
	run := fake.next(t)
	run.progress(setup.Progress{Phase: setup.PhaseInstall, Detail: "Collecting torch"})
	a = pump(t, a, func(a app) bool { j, _ := a.job("comfyui"); return j.progress.Detail == "Collecting torch" })
	if !strings.Contains(plain(a), "installing…  Collecting torch") || !strings.Contains(plain(a), "stop ComfyUI") {
		t.Fatalf("Music engine while installing:\n%s", plain(a))
	}
	if st := a.launcherState(); st.comfyJob != "installing…" || a.downloading() {
		t.Fatalf("launcher state %+v", st)
	}
	run.finish(nil)
	pump(t, a, noJob("comfyui"))
}

func TestTheDownloadNoteKeepsEveryScreenInTheTerminal(t *testing.T) {
	a, fake := newInstallApp(t, false)
	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	run := fake.next(t)
	for _, h := range []int{10, 24, 40} {
		a, _ = send(t, a, tea.WindowSizeMsg{Width: 90, Height: h})
		for _, s := range []screen{screenLauncher, screenLibrary, screenSettings} {
			a, _ = send(t, a, switchScreenMsg(s))
			view := plain(a)
			lines := strings.Split(view, "\n")
			if len(lines) > h || !strings.Contains(lines[len(lines)-1], "A download is running") {
				t.Errorf("height %d screen %v: %d lines:\n%s", h, s, len(lines), view)
			}
		}
		a, _ = send(t, a, openComposeMsg{})
		if lines := strings.Split(plain(a), "\n"); len(lines) > h {
			t.Errorf("height %d song form: %d lines", h, len(lines))
		}
		a, _ = send(t, a, key("esc"))
	}
	run.finish(nil)
	pump(t, a, noJob(vaeKey))
}

func TestAModelRowWaitsForAComfyUIInstallThatIsRunning(t *testing.T) {
	a, fake := newInstallApp(t, true)
	a = focus(t, a, "ComfyUI", -1)
	a = hit(t, a, "enter")
	run := fake.next(t)
	a = focus(t, a, "VAE", -1)
	a = hit(t, a, "enter")
	fake.noneStarted(t)
	if !strings.Contains(a.settings.message, "ComfyUI is still installing") {
		t.Fatalf("message %q", a.settings.message)
	}
	run.finish(nil)
	pump(t, a, noJob("comfyui"))
}
