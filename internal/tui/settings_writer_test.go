package tui

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/tui/vim"
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
	for _, s := range []string{"Model · " + testModel + " · 3 installed", "alpha:1b", "▸ ✓ " + testModel, "zeta:3b", "Applies to the next draft"} {
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
	row := a.writerModelRows()[0]
	if !slices.Equal(row.options, []string{"alpha:1b", "zeta:3b", testModel + " · not pulled"}) || row.current != 2 || !row.muted[2] {
		t.Fatalf("row %+v", row)
	}
	if name, opt := focusedName(a); name != "Model" || opt != 2 {
		t.Fatalf("cursor on %q %d", name, opt)
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || !strings.Contains(plain(a), testModel+" cannot be pulled from here") {
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
	a, _ = send(t, a, modelsLoadedMsg{seq: a.settings.models.seq - 1, infos: []ollama.ModelInfo{{Name: "stale:1b"}}})
	if strings.Contains(plain(a), "stale:1b") || !strings.Contains(plain(a), "zeta:3b") {
		t.Fatalf("stale list shown:\n%s", plain(a))
	}
}

func TestWriterOtherModelSavesAPulledTagAndNeverAnUnpulledOne(t *testing.T) {
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
	if len(*saves) != 0 || !strings.Contains(a.settings.message, "newcomer:2b cannot be pulled from here") {
		t.Fatalf("unpulled tag without an installer: saves %v, message %q", *saves, a.settings.message)
	}
	a = focus(t, a, "Other model", -1)
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, "zeta:3b")
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.model", "zeta:3b"}) {
		t.Fatalf("saves %v", *saves)
	}
}

func TestWriterURLIsValidatedAndReloadsTheList(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionAdvanced)
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
	if a = pressSettings(t, a, "2"); !strings.Contains(plain(a), "fresh:1b") {
		t.Fatalf("list not reloaded from the new URL:\n%s", plain(a))
	}
}

// typeInSettings sends each rune of text as its own key press.
func typeInSettings(t *testing.T, a app, text string) app {
	t.Helper()
	for _, r := range text {
		a, _ = send(t, a, key(string(r)))
	}
	return a
}

func numberedInstructions(t *testing.T, dir string) []string {
	t.Helper()
	found, _ := filepath.Glob(filepath.Join(dir, "instructions-*.txt"))
	return found
}

func TestWritingInstructionsEditOneFileInTheApp(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	dir := filepath.Dir(a.d.Config.Path())
	path := filepath.Join(dir, "instructions.txt")

	// No file yet: the built-in prompt is copied there and opened here.
	a = focus(t, a, "Writing instructions", -1)
	a = pressSettings(t, a, "enter")
	if a.settings.text == nil || a.cfg().Ollama.InstructionsFile != path || a.settings.text.ed.text() != strings.TrimRight(writer.BuiltinInstructions, "\n") {
		t.Fatalf("open: text %v key %q", a.settings.text != nil, a.cfg().Ollama.InstructionsFile)
	}
	if !strings.Contains(plain(a), "Writing instructions") || !strings.Contains(plain(a), "INSERT") {
		t.Fatalf("editor not drawn:\n%s", plain(a))
	}
	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	a = pressSettings(t, a, "esc")
	a = typeInSettings(t, a, ":q")
	a = pressSettings(t, a, "enter")

	a = pressSettings(t, a, "enter")
	if a.settings.text == nil || a.settings.text.ed.text() != "keep me" {
		t.Fatalf("reopen should read the file")
	}
	a = typeInSettings(t, a, " now")
	a, cmd := send(t, a, key("ctrl+c"))
	if quits(cmd) || a.settings.text == nil || a.settings.text.ed.ed.Mode() == vim.ModeInsert {
		t.Fatal("ctrl+c in the editor should act like esc, not quit")
	}
	a = pressSettings(t, a, "enter")
	if a.settings.text != nil {
		t.Fatal("normal-mode enter should save and return")
	}
	if data, _ := os.ReadFile(path); string(data) != "keep me now\n" {
		t.Fatalf("file %q", data)
	}
	if !strings.Contains(a.settings.message, "Writing instructions saved in "+path) {
		t.Fatalf("message %q", a.settings.message)
	}

	a = pressSettings(t, a, "enter")
	a = typeInSettings(t, a, " zzz")
	a = pressSettings(t, a, "esc")
	a = typeInSettings(t, a, ":q")
	a = pressSettings(t, a, "enter")
	if data, _ := os.ReadFile(path); a.settings.text != nil || string(data) != "keep me now\n" {
		t.Fatalf(":q should discard: %q", data)
	}

	// Reset clears only the key; the next edit brings the same file back.
	a = focus(t, a, "Reset instructions", -1)
	a = pressSettings(t, a, "enter")
	if a.cfg().Ollama.InstructionsFile != "" || !strings.Contains(a.settings.message, "your file is kept at "+path) {
		t.Fatalf("reset: %q %q", a.cfg().Ollama.InstructionsFile, a.settings.message)
	}
	if !strings.Contains(plain(a), "Writing instructions  built-in") {
		t.Fatalf("row after reset:\n%s", plain(a))
	}
	a = focus(t, a, "Writing instructions", -1)
	a = pressSettings(t, a, "enter")
	if a.settings.text == nil || a.settings.text.ed.text() != "keep me now" || a.cfg().Ollama.InstructionsFile != path {
		t.Fatalf("after reset the user's file should come back")
	}
	if n := numberedInstructions(t, dir); len(n) != 0 {
		t.Fatalf("numbered copies made: %v", n)
	}

	// An empty file is saved but drafts keep the built-in prompt.
	a = pressSettings(t, a, "esc")
	a = typeInSettings(t, a, "ggVGd:w")
	a = pressSettings(t, a, "enter")
	if a.settings.text != nil || !a.settings.failed || !strings.Contains(a.settings.message, "drafts use the built-in instructions until it has text") {
		t.Fatalf("empty file: %q", a.settings.message)
	}

	// $EDITOR still reports back.
	if err := os.WriteFile(path, []byte("Write short punk songs."), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, instructionsEditedMsg{path: path})
	if got, err := writer.Instructions(a.cfg().Ollama.InstructionsFile); err != nil || got != "Write short punk songs." {
		t.Fatalf("next draft would use %q, %v", got, err)
	}
}

func TestWritingInstructionsRemovesOnlyTheFileItJustMadeWhenSavingFails(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	path := filepath.Join(filepath.Dir(a.d.Config.Path()), "instructions.txt")
	a.settings.save = func(string, any) error { return errors.New("disk full") }
	a = focus(t, a, "Writing instructions", -1)
	a = pressSettings(t, a, "enter")
	if _, err := os.Stat(path); !os.IsNotExist(err) || a.settings.text != nil || !a.settings.failed {
		t.Fatalf("a file made for a failed save was left: %v", err)
	}
	if err := os.WriteFile(path, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	a = pressSettings(t, a, "enter")
	if data, err := os.ReadFile(path); err != nil || string(data) != "mine" {
		t.Fatalf("the user's file was removed or changed: %q %v", data, err)
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

// A model /api/tags says nothing about is asked about with /api/show; one
// show cannot answer stays in the list.
func TestModelListAsksShowAndKeepsModelsItCannotAsk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"writer:1b"},{"name":"vectors:1b"},{"name":"mystery:1b"},{"name":"tagged:1b","capabilities":["completion"]}]}`))
		case "/api/show":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body["model"] {
			case "writer:1b":
				_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
			case "vectors:1b":
				_, _ = w.Write([]byte(`{"capabilities":["embedding"]}`))
			case "tagged:1b":
				t.Error("show asked about a model tags described")
			default:
				http.Error(w, "boom", http.StatusInternalServerError)
			}
		}
	}))
	t.Cleanup(srv.Close)
	infos, err := listOllamaModels(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var chat []string
	for _, m := range infos {
		if m.CanChat() {
			chat = append(chat, m.Name)
		}
	}
	if !slices.Equal(chat, []string{"writer:1b", "mystery:1b", "tagged:1b"}) {
		t.Fatalf("chat models %q", chat)
	}
}

// With no model in Ollama, the configured one is a list of one that enter
// still pulls.
func TestAWriterListOfOneStillActs(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a.settings.models.list = fakeModels([]string{}...)
	a = openSettingsOn(t, a, sectionWriter)
	row := a.writerModelRows()[0]
	if row.expands() || len(row.options) != 1 || !strings.Contains(plain(a), testModel+" · not pulled") {
		t.Fatalf("row %+v:\n%s", row, plain(a))
	}
	a = focus(t, a, "Model", -1)
	if !strings.Contains(footerLine(a), "enter choose") || !strings.Contains(plain(a), "enter pulls it through Ollama") {
		t.Fatalf("footer %q:\n%s", footerLine(a), plain(a))
	}
	a = pressSettings(t, a, "enter")
	if !strings.Contains(a.settings.message, testModel+" cannot be pulled from here") {
		t.Fatalf("enter did nothing: %q", a.settings.message)
	}
}
