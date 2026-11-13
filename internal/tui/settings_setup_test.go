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

func TestSetupWithNothingInstalledListsEveryMissingPiece(t *testing.T) {
	a, _ := newSettingsApp(t, nothingInstalled())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 120, Height: 50})
	a = openSettingsOn(t, a, sectionSetup)
	view := plain(a)
	for _, s := range []string{
		"Missing: ComfyUI, music model files, Ollama, ffprobe · songsmith can install ComfyUI and music",
		"model files · get Ollama and ffprobe yourself",
		"ComfyUI install         FAIL     songsmith can install this",
		"Model diffusion_models  FAIL     songsmith can install this",
		"Model text_encoders     FAIL     songsmith can install this",
		"Model vae               FAIL     songsmith can install this",
		"Ollama                  WARN     get Ollama from ollama.com; lyrics can still be written by hand",
		"ffprobe                 WARN     songsmith cannot install this; get it yourself",
		"ComfyUI nodes           skipped  checked while the ComfyUI server runs",
		"Check report, as songsmith doctor prints it:",
		"FAIL ComfyUI install: ComfyUI is not installed",
		"WARN ffprobe: not found, and songsmith cannot install it",
	} {
		if !strings.Contains(view, s) {
			t.Errorf("Setup missing %q:\n%s", s, view)
		}
	}
	a = focus(t, a, "ComfyUI install", -1)
	if !strings.Contains(plain(a), "the install action arrives with in-app setup (for now see docs/setup.md)") {
		t.Errorf("focused install row:\n%s", plain(a))
	}
}

func TestSetupRequirementRowsStartNothing(t *testing.T) {
	a, _ := newSettingsApp(t, nothingInstalled())
	a = openSettingsOn(t, a, sectionSetup)
	saves := recordSaves(&a)
	for _, c := range a.setupChecks() {
		a = focus(t, a, c.Name, -1)
		for _, k := range []string{"enter", " ", "+", "-"} {
			var cmd tea.Cmd
			a, cmd = send(t, a, settingsKey(k))
			if cmd != nil || a.health.running {
				t.Fatalf("%q on %s started something", k, c.Name)
			}
		}
		if row, _ := setupRowNamed(a, c.Name); row.kind != rowInfo || row.run != nil {
			t.Fatalf("%s is %v", c.Name, row.kind)
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
	if row, _ := setupRowNamed(a, "Model diffusion_models"); !strings.Contains(row.value, "FAIL") {
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
	if row, _ := setupRowNamed(a, "Model diffusion_models"); a.health.running || !strings.Contains(row.value, "OK") {
		t.Fatalf("after re-run: running %v row %+v", a.health.running, row)
	}
	if name, _ := focusedName(a); name != "Re-run checks" {
		t.Fatalf("cursor moved to %q when the rows changed", name)
	}
}
