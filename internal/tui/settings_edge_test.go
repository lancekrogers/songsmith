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
	a = openSettingsOn(t, a, sectionWriter)
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
	a = openSettingsOn(t, a, sectionEngine)
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
	a = openSettingsOn(t, a, sectionEngine)
	a = focus(t, a, "Server", -1)
	a, cmd := send(t, a, key("enter"))
	runCmds(cmd)
	msg, ok := a.events.next()
	if !ok {
		t.Fatal("no action result")
	}
	a, follow := send(t, a, msg)
	if !strings.Contains(plain(a), "running at http://127.0.0.1:18188") || strings.Contains(footerLine(a), "enter") && strings.Contains(footerLine(a), "stop") {
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
	a = openSettingsOn(t, a, sectionEngine)
	saves := recordSaves(&a)
	for _, bad := range []string{"localhost:9000", "[::1]", "a b", "-bad.example"} {
		a = commitText(t, a, "Host", bad)
		if len(*saves) != 0 {
			t.Fatalf("%q was accepted", bad)
		}
		a = pressSettings(t, a, "esc")
	}
	for _, good := range []string{"::1", "studio.local", "10.0.0.2"} {
		a = commitText(t, a, "Host", good)
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
	a = openSettingsOn(t, a, sectionEngine)
	a = commitText(t, a, "Port", "18190")
	if !strings.Contains(a.settings.message, "the running server keeps port 18188 until you stop it here") {
		t.Fatalf("message %q", a.settings.message)
	}
	a = focus(t, a, "Port", -1)
	if !strings.Contains(a.focusedHelp(), "Stop the server") {
		t.Fatalf("port help %q", a.focusedHelp())
	}
	a = pressSettings(t, a, "6")
	a = commitText(t, a, "State folder", filepath.Join(t.TempDir(), "state2"))
	a = focus(t, a, "State folder", -1)
	if !strings.Contains(a.focusedHelp(), "Stop the server") {
		t.Fatalf("state help %q", a.focusedHelp())
	}
}

func TestAServerOnTheOldPortCanStillBeStopped(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.status = server.Status{OtherPort: 18100}
	a = openSettingsOn(t, a, sectionEngine)
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

func TestASliderValueOutsideItsRangeDoesNotJump(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	if err := a.d.Config.Set("render.steps", 80); err != nil {
		t.Fatal(err)
	}
	a = openSettingsOn(t, a, sectionRender)
	saves := recordSaves(&a)
	a = focus(t, a, "Steps", -1)
	a = pressSettings(t, a, "+")
	if len(*saves) != 0 || !strings.Contains(a.settings.message, "above the slider's range") {
		t.Fatalf("+ above max: %v %q", *saves, a.settings.message)
	}
	a = pressSettings(t, a, "-")
	if len(*saves) != 1 || (*saves)[0].value != 60 {
		t.Fatalf("- above max: %v", *saves)
	}
	if err := a.d.Config.Set("ollama.temperature", -0.5); err != nil {
		t.Fatal(err)
	}
	a = pressSettings(t, a, "2")
	a = focus(t, a, "Temperature", -1)
	a = pressSettings(t, a, "-")
	if len(*saves) != 1 || !strings.Contains(a.settings.message, "below the slider's range") {
		t.Fatalf("- below min: %v %q", *saves, a.settings.message)
	}
}

func TestHomeAndEndGoToTheFirstAndLastActionableLine(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	for _, c := range []struct{ key, want string }{{"end", "Default length"}, {"home", "Steps"}, {"G", "Default length"}, {"g", "Steps"}} {
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
	a.settings.models.list = func(context.Context, string) ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"older:1b"}, nil
		}
		return []string{"newer:1b"}, nil
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
	a = openSettingsOn(t, a, sectionEngine)
	first := a.loadServerStatus()
	runCmds(first)
	firstMsg, _ := a.events.next()
	srv.status = server.Status{Running: true, Managed: true, Device: "mps"}
	second := a.loadServerStatus()
	runCmds(second)
	secondMsg, _ := a.events.next()
	a, _ = send(t, a, secondMsg)
	a, _ = send(t, a, firstMsg)
	if !strings.Contains(plain(a), "running at") {
		t.Fatalf("the older check won:\n%s", plain(a))
	}
}

func TestANewOllamaURLHidesTheOldList(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	if !strings.Contains(plain(a), "zeta:3b") {
		t.Fatalf("no list:\n%s", plain(a))
	}
	a = focus(t, a, "Ollama URL", -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "http://127.0.0.1:2")
	a, _ = send(t, a, key("enter"))
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
