package tui

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/queue"
)

func readyChecks() []health.Check {
	return []health.Check{
		{ID: health.IDComfyUI, Name: "ComfyUI install", Result: health.OK, Blocking: true},
		{ID: health.IDDiffusionModel, Name: "Model diffusion_models", Result: health.OK, Blocking: true},
		{ID: health.IDTextEncoder, Name: "Model text_encoders", Result: health.OK, Blocking: true},
		{ID: health.IDVAE, Name: "Model vae", Result: health.OK, Blocking: true},
		{ID: health.IDServer, Name: "ComfyUI server", Result: health.OK},
		{ID: health.IDWriter, Name: "Ollama", Result: health.OK},
	}
}

func missingModelChecks() []health.Check {
	checks := readyChecks()
	checks[1].Result, checks[1].Detail, checks[1].Fix = health.Fail, "missing /x/dit.safetensors", health.FixMusicModel
	checks[4].Result = health.Warn
	return checks
}

// newLauncherApp starts on the launcher with the given checks already run.
func newLauncherApp(t *testing.T, checks []health.Check) (app, *library.Store) {
	t.Helper()
	store := library.NewStore(t.TempDir())
	q := queue.New(store, nopBackend{})
	updates, stop := q.Subscribe()
	t.Cleanup(stop)
	deps := testDeps(t, store, q, fakeDrafter{})
	deps.Checks = func(context.Context, config.Config) []health.Check { return checks }
	a := newTestApp(t, deps, updates)
	return finishHealth(t, a), store
}

// finishHealth starts a check run and delivers its result through the bridge.
func finishHealth(t *testing.T, a app) app {
	t.Helper()
	a, cmd := send(t, a, runHealthMsg{})
	cmd()
	a, _ = send(t, a, a.events.wait(t.Context())())
	return a
}

func menuLabels(a app) []string {
	var out []string
	for _, it := range a.launcherState().items() {
		out = append(out, it.label)
	}
	return out
}

func selectedLabel(a app) string {
	items := a.launcherState().items()
	return items[a.launcher.cursor(items)].label
}

func TestLauncherMenuWhenReady(t *testing.T) {
	a, _ := newLauncherApp(t, readyChecks())
	want := []string{"New song", "Library (0)", "Settings", "Quit"}
	if got := menuLabels(a); !slices.Equal(got, want) {
		t.Fatalf("menu = %q, want %q", got, want)
	}
	if selectedLabel(a) != "New song" {
		t.Fatalf("selected %q", selectedLabel(a))
	}
	view := plain(a)
	model := config.Default().Ollama.Model
	for _, s := range []string{"SONGSMITH", "Writer " + model, "Music model ready", "Server running", "Queue idle", "Ready to render", model + " drafts lyrics", "↑/↓ navigate   enter select   q quit", "Music by MiniMax-Music3"} {
		if !strings.Contains(view, s) {
			t.Errorf("launcher missing %q:\n%s", s, view)
		}
	}
	if strings.Contains(view, "Finish setup") {
		t.Fatalf("Finish setup shown with nothing failing:\n%s", view)
	}
}

func TestLauncherOffersFinishSetupWhileABlockingCheckFails(t *testing.T) {
	a, _ := newLauncherApp(t, missingModelChecks())
	want := []string{"Finish setup", "New song", "Library (0)", "Settings", "Quit"}
	if got := menuLabels(a); !slices.Equal(got, want) {
		t.Fatalf("menu = %q, want %q", got, want)
	}
	if selectedLabel(a) != "Finish setup" {
		t.Fatalf("Finish setup not preselected; selected %q", selectedLabel(a))
	}
	view := plain(a)
	for _, s := range []string{"Music model files missing", "Server stopped", "▲ Model diffusion_models: missing /x/dit.safetensors"} {
		if !strings.Contains(view, s) {
			t.Errorf("launcher missing %q:\n%s", s, view)
		}
	}

	a = nav(t, a, "enter")
	if a.screen != screenSettings || a.settings.section != sectionSetup || !strings.Contains(plain(a), "✕   Diffusion model") {
		t.Fatalf("Finish setup opened %v:\n%s", a.screen, plain(a))
	}
}

func TestFinishSetupIsPreselectedOnlyWhenItAppears(t *testing.T) {
	a, _ := newLauncherApp(t, missingModelChecks())
	a, _ = send(t, a, key("down"))
	if selectedLabel(a) != "New song" {
		t.Fatalf("selected %q", selectedLabel(a))
	}
	// A re-run that still fails must not pull the selection back.
	a = finishHealth(t, a)
	if selectedLabel(a) != "New song" {
		t.Fatalf("re-run moved the selection to %q", selectedLabel(a))
	}
}

// nothingInstalled is the first run on a fresh machine: no ComfyUI, no model
// files, no Ollama and no ffprobe.
func nothingInstalled() []health.Check {
	return []health.Check{
		{ID: health.IDComfyUI, Name: "ComfyUI install", Result: health.Fail, Blocking: true, Fix: health.FixComfyUI, Missing: "ComfyUI", Detail: "ComfyUI is not installed in /home/someone/.local/share/songsmith/ComfyUI"},
		{ID: health.IDDiffusionModel, Name: "Model diffusion_models", Result: health.Fail, Blocking: true, Fix: health.FixMusicModel, Missing: "music model files"},
		{ID: health.IDTextEncoder, Name: "Model text_encoders", Result: health.Fail, Blocking: true, Fix: health.FixMusicModel, Missing: "music model files"},
		{ID: health.IDVAE, Name: "Model vae", Result: health.Fail, Blocking: true, Fix: health.FixMusicModel, Missing: "music model files"},
		{ID: health.IDServer, Name: "ComfyUI server", Result: health.Warn, Detail: "not running"},
		{ID: health.IDWriter, Name: "Ollama", Result: health.Warn, Missing: "Ollama", Detail: "Ollama is not reachable, and songsmith cannot install it: get it from https://ollama.com"},
		{ID: health.IDFFprobe, Name: "ffprobe", Result: health.Warn, Missing: "ffprobe", Detail: "not found, and songsmith cannot install it"},
	}
}

func TestLauncherWithNothingInstalled(t *testing.T) {
	a, _ := newLauncherApp(t, nothingInstalled())
	if got := menuLabels(a); !slices.Equal(got, []string{"Finish setup", "New song", "Library (0)", "Settings", "Quit"}) {
		t.Fatalf("menu = %q", got)
	}
	if selectedLabel(a) != "Finish setup" {
		t.Fatalf("selected %q", selectedLabel(a))
	}
	view := plain(a)
	for _, s := range []string{
		"Writer Ollama not reachable", "Music model files missing", "Server not installed", "Queue idle",
		"▲ ComfyUI install (+3 more): ComfyUI is not installed",
		"Missing: ComfyUI, music model files, Ollama, ffprobe",
		"songsmith can install ComfyUI and music model files (Finish setup) · get Ollama and ffprobe yourself",
	} {
		if !strings.Contains(view, s) {
			t.Errorf("launcher missing %q:\n%s", s, view)
		}
	}
	if hint := a.launcherState().items()[1].hint; !strings.Contains(hint, "Write lyrics and a style yourself") {
		t.Errorf("New song hint = %q", hint)
	}

	a = nav(t, a, "enter")
	report := plain(a)
	for _, s := range []string{
		"▲ Rendering is blocked by ComfyUI and the music model files",
		"✕ ComfyUI", "✕   VAE", "! Writer model", "! ffprobe",
		"Ollama is not reachable, and songsmith cannot install it",
	} {
		if !strings.Contains(report, s) {
			t.Errorf("report missing %q:\n%s", s, report)
		}
	}
}

func TestLauncherNotifiesWriterProblemsWithoutBlocking(t *testing.T) {
	checks := readyChecks()
	model := config.Default().Ollama.Model
	checks[5].Missing = "writer model " + model
	checks[5].Result, checks[5].Fix, checks[5].Detail = health.Warn, health.FixWriterModel, "model "+model+" is not installed, but 2 other models are: pick one in Settings → Writer"
	a, _ := newLauncherApp(t, checks)
	view := plain(a)
	if strings.Contains(view, "Finish setup") || !strings.Contains(view, "Writer "+model+" not pulled") || !strings.Contains(view, "! Ollama: model "+model+" is not installed, but 2 other models are: pick one in Settings → Writer") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestLauncherSaysCheckingUntilResultsArrive(t *testing.T) {
	store := library.NewStore(t.TempDir())
	q := queue.New(store, nopBackend{})
	a := newTestApp(t, testDeps(t, store, q, fakeDrafter{}), nil)
	view := plain(a)
	if strings.Count(view, "checking…") != 3 || strings.Contains(view, "Finish setup") || strings.Contains(view, "ready") {
		t.Fatalf("view before checks:\n%s", view)
	}
	a, _ = send(t, a, runHealthMsg{})
	if !strings.Contains(plain(a), "Checking ComfyUI") {
		t.Fatalf("view while checking:\n%s", plain(a))
	}
}

func TestStaleHealthResultsAreDropped(t *testing.T) {
	store := library.NewStore(t.TempDir())
	q := queue.New(store, nopBackend{})
	deps := testDeps(t, store, q, fakeDrafter{})
	var runs atomic.Int32
	deps.Checks = func(context.Context, config.Config) []health.Check {
		if runs.Add(1) == 1 {
			return missingModelChecks()
		}
		return readyChecks()
	}
	a := newTestApp(t, deps, nil)
	a, first := send(t, a, runHealthMsg{})
	a, second := send(t, a, runHealthMsg{})
	first()
	second()
	older, newer := a.events.wait(t.Context())(), a.events.wait(t.Context())()

	a, _ = send(t, a, newer)
	if !a.health.done || a.health.running {
		t.Fatalf("newest result not applied: %+v", a.health)
	}
	a, _ = send(t, a, older)
	if _, blocked := health.Blocker(a.health.checks); blocked || slices.Contains(menuLabels(a), "Finish setup") {
		t.Fatalf("stale result replaced the newer one: %+v", a.health.checks)
	}

	// A stale result arriving before the newest one does not end the run.
	a, cmd := send(t, a, runHealthMsg{})
	cmd()
	a, _ = send(t, a, bridgeMsg{healthDoneMsg{seq: a.health.seq - 1, checks: missingModelChecks()}})
	if !a.health.running {
		t.Fatal("stale result ended the current run")
	}
}

func TestLauncherLibraryAndBack(t *testing.T) {
	a, store := newLauncherApp(t, readyChecks())
	if _, err := store.Create(library.Song{Title: "Neon Water", Status: library.StatusDone, Duration: 20}); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, key("down"))
	if selectedLabel(a) != "Library (1)" {
		t.Fatalf("selected %q", selectedLabel(a))
	}
	a = nav(t, a, "enter")
	if a.screen != screenLibrary || !strings.Contains(plain(a), "Neon Water") {
		t.Fatalf("screen %v:\n%s", a.screen, plain(a))
	}
	a = nav(t, a, "esc")
	if a.screen != screenLauncher || selectedLabel(a) != "Library (1)" {
		t.Fatalf("back on %v with %q selected", a.screen, selectedLabel(a))
	}
}

func TestNewSongFromLauncherReturnsToLauncher(t *testing.T) {
	a, _ := newLauncherApp(t, readyChecks())
	a = nav(t, a, "enter")
	if a.screen != screenCompose || a.compose.focus != rowIdea {
		t.Fatalf("New song opened %v", a.screen)
	}
	a, _ = send(t, a, key("q"))
	if a.screen != screenCompose || a.compose.idea.Value() != "q" {
		t.Fatalf("q should type into the idea, not quit: screen %v idea %q", a.screen, a.compose.idea.Value())
	}
	a, _ = send(t, a, key("esc"))
	if a.screen != screenCompose {
		t.Fatal("esc on a changed form should warn first")
	}
	a, _ = send(t, a, key("esc"))
	if a.screen != screenLauncher {
		t.Fatalf("esc went to %v", a.screen)
	}
}

func TestNowRenderingOpensTheLibraryOnThatSong(t *testing.T) {
	a, store := newLauncherApp(t, readyChecks())
	if _, err := store.Create(library.Song{Title: "First", Status: library.StatusDone}); err != nil {
		t.Fatal(err)
	}
	busy, err := store.Create(library.Song{Title: "Busy Song", Status: library.StatusRendering})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(library.Song{Title: "Later", Status: library.StatusQueued}); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, updateMsg{SongID: busy.ID, Status: library.StatusRendering, Progress: backend.Progress{Stage: backend.StageRendering, Value: 21, Max: 50}})
	if got := menuLabels(a); !slices.Equal(got, []string{"New song", "Now rendering", "Library (3)", "Settings", "Quit"}) {
		t.Fatalf("menu = %q", got)
	}
	if view := plain(a); !strings.Contains(view, `Queue rendering "Busy Song" 42% · 1 queued`) {
		t.Fatalf("queue chip:\n%s", view)
	}
	a, _ = send(t, a, key("down"))
	a = nav(t, a, "enter")
	if s, _ := a.selected(); a.screen != screenLibrary || s.ID != busy.ID {
		t.Fatalf("screen %v selected %q", a.screen, s.Title)
	}
}

func TestLauncherCompactLayout(t *testing.T) {
	a, _ := newLauncherApp(t, missingModelChecks())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 60, Height: 10})
	view := plain(a)
	lines := strings.Split(view, "\n")
	if len(lines) > 10 {
		t.Fatalf("%d lines in a 10-line terminal:\n%s", len(lines), view)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 60 {
			t.Fatalf("line %d wide: %q", w, l)
		}
	}
	if !strings.Contains(view, "▸ Finish setup") || !strings.Contains(view, "↑/↓ · enter · q") {
		t.Fatalf("compact view:\n%s", view)
	}
}

func TestReadyWithStoppedServerSaysReady(t *testing.T) {
	checks := readyChecks()
	checks[4].Result, checks[4].Detail = health.Warn, "not running; songsmith starts it on the first render"
	a, _ := newLauncherApp(t, checks)
	view := plain(a)
	if !strings.Contains(view, "✓ Ready to render") || !strings.Contains(view, "Server stopped") || strings.Contains(view, "not running") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestLauncherFitsCommonSizes(t *testing.T) {
	a, _ := newLauncherApp(t, nothingInstalled())
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}, {130, 34}, {60, 30}}
	for h := 10; h < 24; h++ {
		sizes = append(sizes, [2]int{80, h})
	}
	for _, size := range sizes {
		a, _ = send(t, a, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(plain(a), "\n")
		if len(lines) > size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %d wide: %q", size[0], size[1], w, l)
			}
		}
	}
}

func TestSecondOpenComposeKeepsTheOpenForm(t *testing.T) {
	a, _ := newLauncherApp(t, readyChecks())
	a = nav(t, a, "enter")
	a, _ = send(t, a, key("a"), key("b"))
	a, _ = send(t, a, openComposeMsg{})
	if a.screen != screenCompose || a.compose.idea.Value() != "ab" {
		t.Fatalf("second open reset the form: screen %v idea %q", a.screen, a.compose.idea.Value())
	}
	a, _ = send(t, a, key("esc"))
	if a.screen != screenCompose || !strings.Contains(plain(a), unsavedChanges) {
		t.Fatalf("first esc on the changed form should warn: screen %v", a.screen)
	}
	a, _ = send(t, a, key("esc"))
	if a.screen != screenLauncher {
		t.Fatalf("second esc went to %v, not where the form was opened from", a.screen)
	}
}

// nodesBroken has a blocking failure that names nothing missing, next to a
// missing non-blocking tool.
func nodesBroken() []health.Check {
	checks := append(readyChecks(),
		health.Check{ID: health.IDNodes, Name: "ComfyUI nodes", Result: health.Fail, Blocking: true, Detail: "missing TextEncodeMiniMaxMusic"},
		health.Check{ID: health.IDFFprobe, Name: "ffprobe", Result: health.Warn, Missing: "ffprobe", Detail: "not found"},
	)
	return checks
}

func TestNoticeLeadsWithTheBlockingFailure(t *testing.T) {
	a, _ := newLauncherApp(t, nodesBroken())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 40})
	lines := a.launcherState().noticeLines(80)
	if first := ansi.Strip(lines[0]); !strings.Contains(first, "▲ ComfyUI nodes: missing TextEncodeMiniMaxMusic") {
		t.Fatalf("first notice line %q", first)
	}
	if view := plain(a); !strings.Contains(view, "Missing: ffprobe") {
		t.Fatalf("non-blocking missing item dropped:\n%s", view)
	}
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 12})
	if view := plain(a); !strings.Contains(view, "▲ ComfyUI nodes: missing") {
		t.Fatalf("compact view hides the blocker:\n%s", view)
	}
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 40})
	a = nav(t, a, "enter")
	if a.screen != screenSettings || a.settings.section != sectionSetup {
		t.Fatalf("Finish setup opened %v", a.screen)
	}
	if first := ansi.Strip(a.settingsContent()[0]); !strings.Contains(first, "▲ Rendering is blocked by ComfyUI nodes") {
		t.Fatalf("Setup does not lead with the blocker: %q\n%s", first, plain(a))
	}
}

func TestSetupSaysNothingBlocks(t *testing.T) {
	a, _ := newLauncherApp(t, readyChecks())
	a, _ = send(t, a, switchScreenMsg(screenSetup))
	if first := ansi.Strip(a.settingsContent()[0]); !strings.Contains(first, "Ready to render") {
		t.Fatalf("first Setup line %q", first)
	}
}

func TestNoticeSaysWhenMoreChecksBlock(t *testing.T) {
	a, _ := newLauncherApp(t, nothingInstalled())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 40})
	if first := ansi.Strip(a.launcherState().noticeLines(80)[0]); !strings.HasPrefix(first, "▲ ComfyUI install (+3 more): ComfyUI is not installed") {
		t.Fatalf("first notice line %q", first)
	}
}

func TestCompactLauncherShowsTheMessage(t *testing.T) {
	a, store := newLauncherApp(t, missingModelChecks())
	song, err := store.Create(library.Song{Title: "Broken", Status: library.StatusFailed})
	if err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, updateMsg{SongID: song.ID, Status: library.StatusFailed, Err: "out of memory"})
	for h := 10; h <= 22; h++ {
		a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: h})
		view := plain(a)
		lines := strings.Split(view, "\n")
		if len(lines) > h {
			t.Errorf("height %d: %d lines:\n%s", h, len(lines), view)
		}
		if !strings.Contains(view, "render failed: out of memory") || !strings.Contains(view, "Finish setup") {
			t.Errorf("height %d: message or selection missing:\n%s", h, view)
		}
	}
}

func TestLauncherWarnsAboutABadInstructionsFile(t *testing.T) {
	checks := append(readyChecks(), health.Check{ID: health.IDInstructions, Name: "Writing instructions", Result: health.Warn,
		Detail: "read writing instructions: no such file; drafts use the built-in instructions"})
	a, _ := newLauncherApp(t, checks)
	view := plain(a)
	if strings.Contains(view, "Ready to render") || !strings.Contains(view, "! Writing instructions: read writing instructions") {
		t.Fatalf("view:\n%s", view)
	}
}
