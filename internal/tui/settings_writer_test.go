package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// fakeOllama serves /api/tags with names.
func fakeOllama(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Models []map[string]string `json:"models"`
		}
		for _, n := range names {
			body.Models = append(body.Models, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// configLines is the config file without the lines in base.
func changedLines(t *testing.T, path string, base []string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !slices.Contains(base, l) {
			out = append(out, l)
		}
	}
	return out
}

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestWriterListsOllamaModelsAndSavesOnlyTheModel(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	ollama := fakeOllama(t, "zeta:3b", testModel, "alpha:1b")
	if err := a.d.Config.Set("ollama.url", ollama.URL); err != nil {
		t.Fatal(err)
	}
	a.settings.models.list = nil
	base := fileLines(t, a.d.Config.Path())
	a = openSettingsOn(t, a, sectionWriter)
	view := plain(a)
	for _, s := range []string{"Model\n", "alpha:1b", "▸ ✓ " + testModel, "zeta:3b", "Applies to the next draft"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Writer missing %q:\n%s", s, view)
		}
	}
	saves := recordSaves(&a)
	a = pressSettings(t, a, "up", "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.model", "alpha:1b"}) || a.cfg().Ollama.Model != "alpha:1b" {
		t.Fatalf("saves %v, model %q", *saves, a.cfg().Ollama.Model)
	}
	if got := changedLines(t, a.d.Config.Path(), base); !slices.Equal(got, []string{"  model: alpha:1b"}) {
		t.Fatalf("changed lines %q", got)
	}
	if !strings.Contains(plain(a), "Applies to the next draft · Writer model: alpha:1b") || !strings.Contains(plain(a), "✓ alpha:1b") {
		t.Fatalf("after choosing:\n%s", plain(a))
	}
}

func TestWriterListsAConfiguredModelThatIsNotPulledDim(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a.settings.models.list = fakeModels("alpha:1b", "zeta:3b")
	a = openSettingsOn(t, a, sectionWriter)
	saves := recordSaves(&a)
	row := a.writerModelRow()
	if !slices.Equal(row.options, []string{"alpha:1b", "zeta:3b", testModel + " · not pulled"}) || row.current != 2 || !row.muted[2] {
		t.Fatalf("row %+v", row)
	}
	if name, opt := focusedName(a); name != "Model" || opt != 2 {
		t.Fatalf("cursor on %q %d", name, opt)
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || !strings.Contains(plain(a), testModel+" is not pulled. Pulling it from here arrives with in-app setup; for now run `ollama pull "+testModel+"`") {
		t.Fatalf("saves %v:\n%s", *saves, plain(a))
	}
}

func TestWriterSaysWhenOllamaIsUnreachable(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a.settings.models.list = fakeModels()
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 140, Height: 30})
	a = openSettingsOn(t, a, sectionWriter)
	view := plain(a)
	for _, s := range []string{"Ollama is not reachable at http://127.0.0.1:1", "https://ollama.com/download", "lyrics can still be written by hand"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Writer missing %q:\n%s", s, view)
		}
	}
	if strings.Contains(view, "✓") {
		t.Fatalf("a list is shown while Ollama is unreachable:\n%s", view)
	}
	a = pressSettings(t, a, "1")
	a, _ = send(t, a, settingsKey("2"))
	if view := plain(a); strings.Contains(view, "not pulled") || !strings.Contains(view, "Ollama is not reachable") {
		t.Fatalf("while reloading:\n%s", view)
	}
}

func TestWriterShowsLoadingAndDropsStaleLists(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a, first := send(t, a, switchScreenMsg(screenSettings))
	a, second := send(t, a, settingsKey("2"))
	if !strings.Contains(plain(a), "loading installed models…") {
		t.Fatalf("no loading line:\n%s", plain(a))
	}
	_ = first
	a = settle(t, a, second)
	a, _ = send(t, a, modelsLoadedMsg{seq: a.settings.models.seq - 1, names: []string{"stale:1b"}})
	if strings.Contains(plain(a), "stale:1b") || !strings.Contains(plain(a), "zeta:3b") {
		t.Fatalf("stale list shown:\n%s", plain(a))
	}
}

func TestWriterOtherModelSavesAnyTag(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	saves := recordSaves(&a)
	a = focus(t, a, "Other model", -1)
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, "two words")
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || !a.settings.edit.active || !strings.Contains(plain(a), "Type one model tag, such as name:tag") {
		t.Fatalf("bad tag: %v\n%s", *saves, plain(a))
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "newcomer:2b")
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.model", "newcomer:2b"}) {
		t.Fatalf("saves %v", *saves)
	}
	if !strings.Contains(plain(a), "newcomer:2b · not pulled") {
		t.Fatalf("not listed as not pulled:\n%s", plain(a))
	}
	if !strings.Contains(a.settings.message, "newcomer:2b is not pulled") {
		t.Fatalf("message %q", a.settings.message)
	}
}

func TestWriterURLIsValidatedAndReloadsTheList(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	saves := recordSaves(&a)
	a = focus(t, a, "Ollama URL", -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "localhost 11434")
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || !strings.Contains(plain(a), "Type a URL such as http://127.0.0.1:11434") {
		t.Fatalf("bad URL: %v\n%s", *saves, plain(a))
	}
	ollama := fakeOllama(t, "fresh:1b")
	a.settings.models.list = nil
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, ollama.URL)
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.url", ollama.URL}) || a.cfg().Ollama.URL != ollama.URL {
		t.Fatalf("saves %v", *saves)
	}
	if !strings.Contains(plain(a), "fresh:1b") {
		t.Fatalf("list not reloaded from the new URL:\n%s", plain(a))
	}
}

func TestWritingInstructionsRoundTrip(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	dir := filepath.Dir(a.d.Config.Path())
	earlier := filepath.Join(dir, "instructions.txt")
	if err := os.WriteFile(earlier, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	a = focus(t, a, "Writing instructions", -1)
	a, cmd := send(t, a, key("enter"))
	path := filepath.Join(dir, "instructions-2.txt")
	if cmd == nil || a.cfg().Ollama.InstructionsFile != path {
		t.Fatalf("cmd %v, instructions file %q", cmd, a.cfg().Ollama.InstructionsFile)
	}
	if data, _ := os.ReadFile(path); string(data) != writer.BuiltinInstructions {
		t.Fatalf("new file does not hold the built-in prompt:\n%s", data)
	}
	if data, _ := os.ReadFile(earlier); string(data) != "keep me" {
		t.Fatalf("earlier file overwritten: %q", data)
	}

	if err := os.WriteFile(path, []byte("Write short punk songs."), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, instructionsEditedMsg{path: path})
	if !strings.Contains(a.settings.message, "Applies to the next draft · Writing instructions saved in "+path) {
		t.Fatalf("message %q", a.settings.message)
	}
	if got, err := writer.Instructions(a.cfg().Ollama.InstructionsFile); err != nil || got != "Write short punk songs." {
		t.Fatalf("next draft would use %q, %v", got, err)
	}

	a, cmd = send(t, a, key("enter"))
	if cmd == nil || a.cfg().Ollama.InstructionsFile != path {
		t.Fatalf("second open made a new file: %q", a.cfg().Ollama.InstructionsFile)
	}

	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, instructionsEditedMsg{path: path})
	if !a.settings.failed || !strings.Contains(a.settings.message, "drafts use the built-in instructions until it has text") {
		t.Fatalf("empty file message %q", a.settings.message)
	}

	a = focus(t, a, "Reset instructions", -1)
	a = pressSettings(t, a, "enter")
	if a.cfg().Ollama.InstructionsFile != "" || !strings.Contains(plain(a), "your file stays at") {
		t.Fatalf("reset: %q\n%s", a.cfg().Ollama.InstructionsFile, plain(a))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reset removed the file: %v", err)
	}
	if !strings.Contains(plain(a), "Writing instructions  built-in") {
		t.Fatalf("row after reset:\n%s", plain(a))
	}
}

// Picking an installed writer model runs the checks again on the way out,
// so the launcher stops saying the old model is missing.
func TestChoosingAWriterModelRefreshesTheChecks(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a.d.Checks = func(_ context.Context, cfg config.Config) []health.Check {
		checks := readyChecks()
		if cfg.Ollama.Model != "zeta:3b" {
			checks[5].Result, checks[5].Fix, checks[5].Missing = health.Warn, health.FixWriterModel, "writer model "+cfg.Ollama.Model
		}
		return checks
	}
	a = finishHealth(t, a)
	if !strings.Contains(plain(a), "not pulled") {
		t.Fatalf("launcher before:\n%s", plain(a))
	}
	a = openSettingsOn(t, a, sectionWriter)
	a = focus(t, a, "Model", 2)
	a = pressSettings(t, a, "enter", "esc")
	view := plain(a)
	if a.screen != screenLauncher || strings.Contains(view, "not pulled") || !strings.Contains(view, "Writer zeta:3b") {
		t.Fatalf("launcher after:\n%s", view)
	}
}
