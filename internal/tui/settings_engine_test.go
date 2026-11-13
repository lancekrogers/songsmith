package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/comfy/server"
)

func TestServerShowsTheEngineAsALineAndItsRows(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionServer)
	var names []string
	for _, r := range a.serverRows() {
		names = append(names, r.name)
		if r.expands() {
			t.Errorf("Server has a list: %q", r.name)
		}
	}
	want := []string{"Engine", "Server", "Install ComfyUI", "Install folder", "Log"}
	if !slices.Equal(names, want) {
		t.Fatalf("rows %q, want %q", names, want)
	}
	view := plain(a)
	for _, s := range []string{"Engine: ComfyUI · MiniMax Music 3", "Server           stopped  http://127.0.0.1:18188", "Log              /tmp/songsmith-test-state/comfyui.log"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Server missing %q:\n%s", s, view)
		}
	}
	if name, _ := focusedName(a); name != "Server" {
		t.Fatalf("Server opened on %q", name)
	}
}

func TestInstallComfyUIRowOnlyWhenTheFolderHasNoComfyUI(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionServer)
	if lineOf(a, "Install ComfyUI", -1) < 0 {
		t.Fatal("no Install ComfyUI row for an empty folder")
	}
	dir := a.cfg().ComfyUI.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a = pressSettings(t, a, "7", "5")
	if lineOf(a, "Install ComfyUI", -1) >= 0 {
		t.Fatalf("Install ComfyUI shown with ComfyUI present:\n%s", plain(a))
	}
}

func TestServerStartAndStopRunInTheBackground(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Server", -1)
	if got := footerLine(a); !strings.Contains(got, "enter start") {
		t.Fatalf("footer %q", got)
	}
	a, cmd := send(t, a, key("enter"))
	if !strings.Contains(plain(a), "Server           starting…") || !strings.Contains(plain(a), "Starting the ComfyUI server…") {
		t.Fatalf("while starting:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if srv.starts != 0 {
		t.Fatal("enter while starting started again")
	}
	a = settle(t, a, cmd)
	if srv.starts != 1 || !strings.Contains(plain(a), "Server running on mps") ||
		!strings.Contains(plain(a), "running  started by songsmith · http://127.0.0.1:18188") {
		t.Fatalf("after start (%d starts):\n%s", srv.starts, plain(a))
	}
	if got := footerLine(a); !strings.Contains(got, "enter stop") {
		t.Fatalf("footer %q", got)
	}
	a = pressSettings(t, a, "enter")
	if srv.stops != 1 || !strings.Contains(plain(a), "Server stopped") || !strings.Contains(plain(a), "stopped  http://") {
		t.Fatalf("after stop:\n%s", plain(a))
	}
}

func TestServerStartedOutsideSongsmithOffersNoStop(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.status = server.Status{Running: true, Device: "mps"}
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Server", -1)
	if !strings.Contains(plain(a), "running  started outside songsmith") || strings.Contains(footerLine(a), "enter stop") {
		t.Fatalf("view:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if srv.stops != 0 {
		t.Fatal("enter tried to stop a server songsmith did not start")
	}
	srv.status = server.Status{}
	srv.startErr = errors.New("comfyui not found at /x")
	a = pressSettings(t, a, "7", "5")
	a = focus(t, a, "Server", -1)
	a = pressSettings(t, a, "enter")
	if !strings.Contains(plain(a), "Could not start the server: comfyui not found at /x") {
		t.Fatalf("failed start:\n%s", plain(a))
	}
}

func TestPortIsValidatedAndAppliesAfterRestart(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionAdvanced)
	saves := recordSaves(&a)
	a = focus(t, a, "ComfyUI port", -1)
	a = pressSettings(t, a, "enter")
	for _, bad := range []string{"0", "70000", "port", "-5", ""} {
		a = clearEditor(t, a)
		a = typeIntoSettings(t, a, bad)
		a = pressSettings(t, a, "enter")
		if len(*saves) != 0 || !a.settings.edit.active || !strings.Contains(plain(a), "Port must be a whole number from 1 to 65535") {
			t.Fatalf("%q: saves %v\n%s", bad, *saves, plain(a))
		}
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "18190")
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"comfyui.port", 18190}) || a.cfg().ComfyUI.Port != 18188 {
		t.Fatalf("saves %v, running port %d", *saves, a.cfg().ComfyUI.Port)
	}
	view := plain(a)
	if !strings.Contains(view, "Applies after restart · Port: 18190") || !strings.Contains(view, "ComfyUI port  18190  applies after restart · running 18188") {
		t.Fatalf("after saving the port:\n%s", view)
	}
}

func TestFoldersAndHostSaveOnlyTheirKeyForTheNextStart(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionServer)
	base := fileLines(t, a.d.Config.Path())
	saves := recordSaves(&a)
	running := a.cfg()

	a = focus(t, a, "Install folder", -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || !strings.Contains(plain(a), "Install folder cannot be empty; type a folder") {
		t.Fatalf("empty folder: %v\n%s", *saves, plain(a))
	}
	mine := filepath.Join(t.TempDir(), "MyComfyUI")
	a = typeIntoSettings(t, a, mine)
	a = pressSettings(t, a, "enter", "enter")
	if got := changedLines(t, a.d.Config.Path(), base); !slices.Equal(got, []string{"  dir: " + mine}) {
		t.Fatalf("changed lines %q", got)
	}
	if a.cfg().ComfyUI.Dir != running.ComfyUI.Dir || a.settings.saved.ComfyUI.Dir != mine {
		t.Fatalf("running %q saved %q", a.cfg().ComfyUI.Dir, a.settings.saved.ComfyUI.Dir)
	}

	a = openSettingsOn(t, a, sectionAdvanced)
	a = focus(t, a, "ComfyUI host", -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "http://box/")
	a = pressSettings(t, a, "enter")
	if !strings.Contains(plain(a), "Type a host name or IP address without a port, such as 127.0.0.1") {
		t.Fatalf("bad host:\n%s", plain(a))
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "0.0.0.0")
	a = pressSettings(t, a, "enter")
	if a.cfg().ComfyUI.Host != running.ComfyUI.Host || !strings.Contains(plain(a), "ComfyUI host  0.0.0.0  applies after restart · running 127.0.0.1") {
		t.Fatalf("host:\n%s", plain(a))
	}

	a = pressSettings(t, a, "7")
	view := plain(a)
	for _, s := range []string{"Library folder", "State folder", "Config file"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Library missing %q:\n%s", s, view)
		}
	}
	if rows := a.libraryRows(); rows[2].kind != rowInfo || rows[2].value != a.d.Config.Path() {
		t.Fatalf("config file row %+v", rows[2])
	}
	a = focus(t, a, "Library folder", -1)
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	lib := filepath.Join(t.TempDir(), "Songs")
	a = typeIntoSettings(t, a, lib)
	a = pressSettings(t, a, "enter")
	last := (*saves)[len(*saves)-1]
	if last != (savedKV{"library_dir", lib}) || a.cfg().LibraryDir != running.LibraryDir ||
		a.libraryRows()[0].detail != "applies after restart · running "+running.LibraryDir || a.libraryRows()[0].value != lib {
		t.Fatalf("library: %v\n%s", last, plain(a))
	}
	if !strings.Contains(plain(a), "Applies after restart · Library folder:") {
		t.Fatalf("status line:\n%s", plain(a))
	}
}
