package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/ollama"
)

// commitText opens the one-line editor on the named row, types value and
// presses enter, settling what that starts.
func commitText(t *testing.T, a app, row, value string) app {
	t.Helper()
	a = focus(t, a, row, -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, value)
	return pressSettings(t, a, "enter")
}

func TestOllamaURLRejectsAPathAndDropsATrailingSlash(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionAdvanced)
	saves := recordSaves(&a)
	for _, bad := range []string{"http://127.0.0.1:11434/api", "http://127.0.0.1:11434/?x=1", "http://127.0.0.1:11434#top", "ftp://host"} {
		a = commitText(t, a, "Ollama URL", bad)
		if len(*saves) != 0 || !a.settings.edit.active {
			t.Fatalf("%q was accepted: %v", bad, *saves)
		}
		a = pressSettings(t, a, "esc")
	}
	a = commitText(t, a, "Ollama URL", "http://127.0.0.1:1/")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.url", "http://127.0.0.1:1"}) {
		t.Fatalf("saves %v", *saves)
	}
}

func TestServerRowCannotStopWhileASongRenders(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.status = server.Status{Running: true, Managed: true, Device: "mps"}
	if _, err := a.d.Store.Create(library.Song{Title: "Busy", Status: library.StatusRendering}); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Server", -1)
	if !strings.Contains(plain(a), "Rendering — stop is unavailable until the song finishes or is cancelled") || strings.Contains(footerLine(a), "enter stop") {
		t.Fatalf("while rendering:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if srv.stops != 0 {
		t.Fatal("enter stopped the server mid-render")
	}
}

func TestAServerActionAppliesItsStatusAndWaitsForTheCheck(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Server", -1)
	a, cmd := send(t, a, key("enter"))
	runCmds(cmd)
	msg, ok := a.events.next()
	if !ok {
		t.Fatal("no action result")
	}
	a, follow := send(t, a, msg)
	if !strings.Contains(plain(a), "running  started by songsmith · http://127.0.0.1:18188") || strings.Contains(footerLine(a), "enter") && strings.Contains(footerLine(a), "stop") {
		t.Fatalf("before the check lands:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if srv.starts != 1 || srv.stops != 0 {
		t.Fatalf("enter acted before the check: starts %d stops %d", srv.starts, srv.stops)
	}
	a = settle(t, a, follow)
	if !strings.Contains(footerLine(a), "enter stop") {
		t.Fatalf("after the check: %q", footerLine(a))
	}
}

func TestCompactSettingsSaysWhenAChangeApplies(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 100, Height: 10})
	a = focus(t, a, "Steps", -1)
	if !strings.Contains(plain(a), "Applies to the next render") {
		t.Fatalf("compact view:\n%s", plain(a))
	}
}

func TestHostTakesANameOrAddressWithoutAPort(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionAdvanced)
	saves := recordSaves(&a)
	for _, bad := range []string{"localhost:9000", "[::1]", "a b", "-bad.example"} {
		a = commitText(t, a, "ComfyUI host", bad)
		if len(*saves) != 0 {
			t.Fatalf("%q was accepted", bad)
		}
		a = pressSettings(t, a, "esc")
	}
	for _, good := range []string{"::1", "studio.local", "10.0.0.2"} {
		a = commitText(t, a, "ComfyUI host", good)
	}
	if len(*saves) != 3 || (*saves)[0].value != "::1" {
		t.Fatalf("saves %v", *saves)
	}
}

func TestFoldersExpandTheHomeFolderAndMustBeAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionLibrary)
	saves := recordSaves(&a)
	a = commitText(t, a, "Library folder", "relative/songs")
	if len(*saves) != 0 || !a.settings.edit.active || !strings.Contains(plain(a), "must be a full path") {
		t.Fatalf("relative: %v\n%s", *saves, plain(a))
	}
	a = pressSettings(t, a, "esc")
	a = commitText(t, a, "Library folder", "~")
	a = commitText(t, a, "Library folder", "~/Songs")
	if len(*saves) != 2 || (*saves)[0].value != home || (*saves)[1].value != "~/Songs" {
		t.Fatalf("saves %v", *saves)
	}
	if a.settings.saved.LibraryDir != filepath.Join(home, "Songs") {
		t.Fatalf("loaded %q", a.settings.saved.LibraryDir)
	}
}

func TestSettingsFitsTinyTerminals(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	a = focus(t, a, "Top-k", -1)
	for _, size := range [][2]int{{15, 24}, {10, 8}, {80, 3}, {80, 4}, {40, 5}} {
		w, h := size[0], size[1]
		b, _ := send(t, a, tea.WindowSizeMsg{Width: w, Height: h})
		lines := strings.Split(b.View(), "\n")
		if len(lines) > h {
			t.Errorf("%dx%d: %d lines", w, h, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Errorf("%dx%d: %q is wider than the terminal", w, h, ansi.Strip(l))
				break
			}
		}
		if w >= 40 && !strings.Contains(plain(b), "Top-k") {
			t.Errorf("%dx%d: the focused line is not shown:\n%s", w, h, plain(b))
		}
	}
}

func TestPortAndStateFolderSayToStopTheServerFirst(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.status = server.Status{Running: true, Managed: true, Device: "mps"}
	a = openSettingsOn(t, a, sectionServer)
	a = openSettingsOn(t, a, sectionAdvanced)
	a = commitText(t, a, "ComfyUI port", "18190")
	if !strings.Contains(a.settings.message, "the running server keeps port 18188 until you stop it here") {
		t.Fatalf("message %q", a.settings.message)
	}
	a = focus(t, a, "ComfyUI port", -1)
	if !strings.Contains(a.focusedHelp(), "Stop the server") {
		t.Fatalf("port help %q", a.focusedHelp())
	}
	a = pressSettings(t, a, "8")
	a = commitText(t, a, "State folder", filepath.Join(t.TempDir(), "state2"))
	a = focus(t, a, "State folder", -1)
	if !strings.Contains(a.focusedHelp(), "Stop the server") {
		t.Fatalf("state help %q", a.focusedHelp())
	}
}

func TestAServerOnTheOldPortCanStillBeStopped(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.status = server.Status{OtherPort: 18100}
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Server", -1)
	if !strings.Contains(plain(a), "still runs on port 18100") || !strings.Contains(footerLine(a), "enter stop") {
		t.Fatalf("view:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if srv.stops != 1 {
		t.Fatal("enter did not stop it")
	}
}

func TestAFailedSaveKeepsTheEditorAndTheTypedValue(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionLibrary)
	a.settings.save = func(string, any) error { return errors.New("read-only file system") }
	a = commitText(t, a, "State folder", "/tmp/songsmith-test-other")
	if !a.settings.edit.active || a.settings.edit.input.Value() != "/tmp/songsmith-test-other" || !strings.Contains(plain(a), "Not saved: read-only file system") {
		t.Fatalf("editor %v value %q\n%s", a.settings.edit.active, a.settings.edit.input.Value(), plain(a))
	}
}

// A slider's range is for stepping: a value outside it steps on from where
// it is, and enter types any value the render or the writer accepts.
func TestSlidersAreNotLimitedToTheirRange(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	if err := a.d.Config.Set("render.steps", 80); err != nil {
		t.Fatal(err)
	}
	a = openSettingsOn(t, a, sectionRender)
	saves := recordSaves(&a)
	a = focus(t, a, "Steps", -1)
	a = pressSettings(t, a, "+", "-", "-")
	if got := *saves; len(got) != 3 || got[0].value != 81 || got[2].value != 79 {
		t.Fatalf("steps outside the range: %v", got)
	}
	a = commitText(t, a, "Steps", "250")
	if a.cfg().Render.Steps != 250 || a.settings.edit.active {
		t.Fatalf("typed steps: %d", a.cfg().Render.Steps)
	}
	for _, bad := range []string{"0", "-3", "2.5", "many"} {
		a = commitText(t, a, "Steps", bad)
		if !a.settings.edit.active || !strings.Contains(plain(a), "Steps must be a whole number of 1 or more") {
			t.Fatalf("%q accepted:\n%s", bad, plain(a))
		}
		a = pressSettings(t, a, "esc")
	}
	a = commitText(t, a, "CFG scale", "7.5")
	a = commitText(t, a, "Top-k", "1000")
	if r := a.cfg().Render; r.CFGScale != 7.5 || r.TopK != 1000 {
		t.Fatalf("typed values: %+v", r)
	}
	a = commitText(t, a, "CFG scale", "0")
	if !a.settings.edit.active || a.cfg().Render.CFGScale != 7.5 {
		t.Fatal("a CFG scale of 0 was accepted")
	}
	a = pressSettings(t, a, "esc")
	a = commitText(t, a, "Default length", "1:45")
	if a.cfg().Render.DefaultSeconds != 105 || !strings.Contains(plain(a), "1:45 ·") {
		t.Fatalf("typed length %d:\n%s", a.cfg().Render.DefaultSeconds, plain(a))
	}
	a = commitText(t, a, "Default length", "6:00")
	if !a.settings.edit.active || a.cfg().Render.DefaultSeconds != 105 {
		t.Fatal("a length past 5:00 was accepted")
	}
	a = pressSettings(t, a, "esc", "2")
	a = commitText(t, a, "Temperature", "2.4")
	if a.cfg().Ollama.Temperature != 2.4 {
		t.Fatalf("typed temperature %v", a.cfg().Ollama.Temperature)
	}
	a = commitText(t, a, "Temperature", "-0.1")
	if !a.settings.edit.active {
		t.Fatal("a negative temperature was accepted")
	}
}

func TestContextIsLeftToOllamaUnlessTyped(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	if !strings.Contains(plain(a), "Context       Ollama's setting (not overridden)") && !strings.Contains(plain(a), "Ollama's setting (not overridden)") {
		t.Fatalf("Writer:\n%s", plain(a))
	}
	for typed, want := range map[string]int{"262144": 262144, "8k": 8192, "128K": 131072, "1": 1} {
		a = commitText(t, a, "Context", typed)
		if a.cfg().Ollama.Context != want {
			t.Fatalf("%q saved %d", typed, a.cfg().Ollama.Context)
		}
	}
	for _, bad := range []string{"0", "-5", "big", "1.5k"} {
		a = commitText(t, a, "Context", bad)
		if !a.settings.edit.active {
			t.Fatalf("%q accepted", bad)
		}
		a = pressSettings(t, a, "esc")
	}
	a = commitText(t, a, "Context", "")
	if a.cfg().Ollama.Context != 0 || !strings.Contains(plain(a), "Ollama's setting (not overridden)") {
		t.Fatalf("blank context: %d", a.cfg().Ollama.Context)
	}
}

func TestUnloadBeforeRenderToggles(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionAdvanced)
	a = focus(t, a, "Unload the writer before rendering", -1)
	if !a.cfg().Ollama.UnloadBeforeRender || !strings.Contains(plain(a), "frees the writer model's memory while a song renders") {
		t.Fatalf("Advanced:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if a.cfg().Ollama.UnloadBeforeRender || !strings.Contains(plain(a), "OFF") {
		t.Fatalf("enter did not turn it off:\n%s", plain(a))
	}
	a = pressSettings(t, a, " ")
	if !a.cfg().Ollama.UnloadBeforeRender {
		t.Fatal("space did not turn it back on")
	}
}

func TestHomeAndEndGoToTheFirstAndLastActionableLine(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	for _, c := range []struct{ key, want string }{{"end", "Low-memory rendering"}, {"home", "Steps"}, {"G", "Low-memory rendering"}, {"g", "Steps"}} {
		a = pressSettings(t, a, c.key)
		if name, _ := focusedName(a); name != c.want {
			t.Fatalf("%s: on %q, want %q", c.key, name, c.want)
		}
	}
}

func TestOverlappingModelLoadsKeepTheNewest(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	calls := 0
	a.settings.models.list = func(context.Context, string) ([]ollama.ModelInfo, error) {
		calls++
		if calls == 1 {
			return []ollama.ModelInfo{{Name: "older:1b"}, {Name: "other:1b"}}, nil
		}
		return []ollama.ModelInfo{{Name: "newer:1b"}, {Name: "other:1b"}}, nil
	}
	first, second := a.loadModels(), a.loadModels()
	runCmds(first)
	firstMsg, _ := a.events.next()
	runCmds(second)
	secondMsg, _ := a.events.next()
	a, _ = send(t, a, secondMsg)
	a, _ = send(t, a, firstMsg)
	if view := plain(a); strings.Contains(view, "older:1b") || !strings.Contains(view, "newer:1b") {
		t.Fatalf("the older load won:\n%s", view)
	}
}

func TestOverlappingServerChecksKeepTheNewest(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionServer)
	first := a.loadServerStatus()
	runCmds(first)
	firstMsg, _ := a.events.next()
	srv.status = server.Status{Running: true, Managed: true, Device: "mps"}
	second := a.loadServerStatus()
	runCmds(second)
	secondMsg, _ := a.events.next()
	a, _ = send(t, a, secondMsg)
	a, _ = send(t, a, firstMsg)
	if !strings.Contains(plain(a), "running  started by songsmith") {
		t.Fatalf("the older check won:\n%s", plain(a))
	}
}

func TestANewOllamaURLHidesTheOldList(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	if !strings.Contains(plain(a), "zeta:3b") {
		t.Fatalf("no list:\n%s", plain(a))
	}
	a = openSettingsOn(t, a, sectionAdvanced)
	a = focus(t, a, "Ollama URL", -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "http://127.0.0.1:2")
	a, _ = send(t, a, key("enter"))
	a, _ = send(t, a, key("2"))
	if view := plain(a); strings.Contains(view, "zeta:3b") || !strings.Contains(view, "loading installed models…") {
		t.Fatalf("old list still offered:\n%s", view)
	}
	if lineOf(a, "Model", 0) >= 0 {
		t.Fatal("an option of the old list is selectable")
	}
}

func TestOllamaURLWithATrailingSlashReachesTheTagsEndpoint(t *testing.T) {
	srv := fakeOllama(t, "slash:1b")
	a, _ := newSettingsApp(t, readyChecks())
	a.settings.models.list = nil
	if err := a.d.Config.Set("ollama.url", srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	a = openSettingsOn(t, a, sectionWriter)
	if !strings.Contains(plain(a), "slash:1b") {
		t.Fatalf("list:\n%s", plain(a))
	}
}
