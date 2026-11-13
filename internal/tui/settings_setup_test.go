package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
)

func setupRowNamed(a app, name string) (settingRow, bool) {
	for _, r := range a.setupRows() {
		if r.name == name {
			return r, true
		}
	}
	return settingRow{}, false
}

// sectionText is the whole section as drawn, one string per line, whatever
// fits on the screen.
func sectionText(a app) []string {
	rows, lines := a.settingsLayout()
	disp, _ := a.settingsDisplay(rows, lines)
	out := make([]string, len(disp))
	for i, d := range disp {
		switch d.kind {
		case displayRow:
			out[i] = a.settingLineView(rows, lines, d.line, nameColumns(rows), a.settingsValueWidths(rows))
		default:
			out[i] = d.text
		}
	}
	return out
}

func TestSetupWhenEverythingIsReadyIsAFewLines(t *testing.T) {
	checks := readyChecks()
	checks = append(checks, health.Check{ID: health.IDFFprobe, Name: "ffprobe", Result: health.OK, Detail: "found"},
		health.Check{ID: health.IDNodes, Name: "ComfyUI nodes", Result: health.OK, Detail: "MiniMax Music 3 nodes available"})
	checks[4].Detail = "running at http://127.0.0.1:18188 on mps"
	a, _ := newSettingsApp(t, checks)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 24})
	a = openSettingsOn(t, a, sectionSetup)
	view := plain(a)
	for _, s := range []string{
		"Ready to render",
		"✓ ComfyUI",
		"✓ Music model files  3 files · 21.8 GB",
		"✓ ComfyUI server     running  at http://127.0.0.1:18188",
		"✓ Writer model       " + testModel,
		"✓ ffprobe            found",
		"Re-run checks",
	} {
		if !strings.Contains(view, s) {
			t.Errorf("Setup missing %q:\n%s", s, view)
		}
	}
	for _, s := range []string{"doctor", "OK  ", "Model diffusion_models", "ComfyUI nodes", "▲"} {
		if strings.Contains(view, s) {
			t.Errorf("Setup shows %q when everything is ready:\n%s", s, view)
		}
	}
	if n := len(sectionText(a)); n > 10 {
		t.Errorf("ready Setup is %d lines:\n%s", n, view)
	}
}

func TestSetupShowsAFailingChecksTextUnderThatRowOnly(t *testing.T) {
	checks := readyChecks()
	checks = append(checks, health.Check{ID: health.IDFFprobe, Name: "ffprobe", Result: health.Warn, Missing: "ffprobe",
		Detail: "not found, and songsmith cannot install it: install ffmpeg"})
	checks[0].Detail = "/tmp/songsmith-test-comfy"
	a, _ := newSettingsApp(t, checks)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 100, Height: 30})
	a = openSettingsOn(t, a, sectionSetup)
	text := sectionText(a)
	at := -1
	for i, l := range text {
		if strings.Contains(l, "! ffprobe") {
			at = i
		}
	}
	if at < 0 || at+1 >= len(text) || !strings.Contains(text[at+1], "not found, and songsmith cannot install it") {
		t.Fatalf("no detail under ffprobe:\n%s", strings.Join(text, "\n"))
	}
	if strings.Contains(plain(a), "/tmp/songsmith-test-comfy\n") || strings.Count(plain(a), "not found, and songsmith") != 1 {
		t.Fatalf("detail shown more than once or for an OK check:\n%s", plain(a))
	}
}

func TestSetupWithNothingInstalledListsEveryMissingPiece(t *testing.T) {
	a, _ := newSettingsApp(t, nothingInstalled())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 120, Height: 50})
	a = openSettingsOn(t, a, sectionSetup)
	view := plain(a)
	for _, s := range []string{
		"▲ Rendering is blocked by ComfyUI and the music model files",
		"✕ ComfyUI            not installed  songsmith can install this",
		"ComfyUI is not installed in /home/someone/.local/share/songsmith/ComfyUI",
		"✕ Music model files  missing",
		"✕   Diffusion model  missing  songsmith can install this",
		"✕   VAE              missing",
		"! Writer model       Ollama not reachable",
		"Ollama is not reachable, and songsmith cannot install it",
		"! ffprobe            not found  get it yourself",
		"! ComfyUI server     stopped  starts on the first render",
	} {
		if !strings.Contains(view, s) {
			t.Errorf("Setup missing %q:\n%s", s, view)
		}
	}
	if strings.Contains(view, "ComfyUI nodes") || strings.Contains(view, "doctor") {
		t.Errorf("skipped or duplicated checks shown:\n%s", view)
	}
}

func TestSetupRequirementRowsStartNothing(t *testing.T) {
	a, _ := newSettingsApp(t, nothingInstalled())
	a = openSettingsOn(t, a, sectionSetup)
	saves := recordSaves(&a)
	for _, r := range a.setupRows() {
		if r.mark == "" {
			continue
		}
		a = focus(t, a, r.name, -1)
		for _, k := range []string{"enter", " ", "+", "-"} {
			var cmd tea.Cmd
			a, cmd = send(t, a, settingsKey(k))
			if cmd != nil || a.health.running {
				t.Fatalf("%q on %s started something", k, r.name)
			}
		}
		if row, _ := setupRowNamed(a, r.name); row.kind != rowInfo || row.run != nil {
			t.Fatalf("%s is %v", r.name, row.kind)
		}
	}
	if len(*saves) != 0 {
		t.Fatalf("saved %v", *saves)
	}
}

func TestSetupRerunReplacesRowsAndDropsStaleResults(t *testing.T) {
	store, q := newStoreQueue(t)
	deps := testDeps(t, store, q, fakeDrafter{})
	deps.Server = &fakeServer{}
	writeTestConfig(t, deps.Config)
	var runs atomic.Int32
	deps.Checks = func(context.Context, config.Config) []health.Check {
		if runs.Add(1) == 1 {
			return missingModelChecks()
		}
		return readyChecks()
	}
	a := newTestApp(t, deps, nil)
	a = finishHealth(t, a)
	a = openSettingsOn(t, a, sectionSetup)
	if row, _ := setupRowNamed(a, "Diffusion model"); !strings.Contains(row.mark, "✕") {
		t.Fatalf("first run: %+v", row)
	}

	a = focus(t, a, "Re-run checks", -1)
	a, run := send(t, a, key("enter"))
	if !a.health.running || !strings.Contains(plain(a), "checking… ◐") || !strings.Contains(plain(a), "Checking…") {
		t.Fatalf("re-run did not start:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if !strings.Contains(plain(a), "The checks are already running") {
		t.Fatalf("second enter while running:\n%s", plain(a))
	}
	a, _ = send(t, a, healthDoneMsg{seq: a.health.seq - 1, checks: missingModelChecks()})
	if !a.health.running {
		t.Fatal("a stale result ended the run")
	}
	a = settle(t, a, run)
	if row, _ := setupRowNamed(a, "Music model files"); a.health.running || !strings.Contains(row.mark, "✓") {
		t.Fatalf("after re-run: running %v row %+v", a.health.running, row)
	}
	if _, ok := setupRowNamed(a, "Diffusion model"); ok {
		t.Fatal("the file rows stay once every file is installed")
	}
	if name, _ := focusedName(a); name != "Re-run checks" {
		t.Fatalf("cursor moved to %q when the rows changed", name)
	}
}
