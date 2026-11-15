package tui

import (
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

// fitsScreen fails the test for any line wider than width or a view taller
// than height.
func fitsScreen(t *testing.T, a app, width, height int, where string) {
	t.Helper()
	lines := strings.Split(plain(a), "\n")
	if len(lines) > height {
		t.Errorf("%s: the view is %d lines on a %d-line terminal:\n%s", where, len(lines), height, plain(a))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("%s: a line is %d wide: %q", where, w, l)
		}
	}
}

func TestTheLibraryFitsTheTerminalWithManySongsPickingUpRenders(t *testing.T) {
	for _, folder := range []string{"", "Album/Live/Takes/Best"} {
		a, r := newMachinesRig(t, 80, 30)
		for i := range 30 {
			s := renderable(fmt.Sprintf("Song %02d", i))
			s.Folder = folder
			s.Status, s.JobID, s.RenderingOn = library.StatusQueued, fmt.Sprintf("studio-%d", i), studio
			if _, err := r.store.Create(s); err != nil {
				t.Fatal(err)
			}
		}
		a = runCmd(t, a, a.loadSongs())
		a, _ = send(t, a, switchScreenMsg(screenLibrary))
		for f := folder; f != "." && f != ""; f = path.Dir(f) {
			a.library.open[f] = true
		}
		a = runCmd(t, a, a.loadSongs())
		where := func(s string) string { return fmt.Sprintf("folder %q, %s", folder, s) }
		if !listShows(a, "↳ picking up its render on Studio Mac") {
			t.Fatalf("%s: no song with its line under it:\n%s", where("top"), plain(a))
		}
		if !strings.Contains(plain(a), "♪ Library") {
			t.Fatalf("%s: the header scrolled off:\n%s", where("top"), plain(a))
		}
		fitsScreen(t, a, 80, 30, where("top"))
		for i := range 34 {
			a, _ = send(t, a, key("down"))
			if i%7 == 0 {
				fitsScreen(t, a, 80, 30, where(fmt.Sprintf("cursor on row %d", i+1)))
			}
		}
		fitsScreen(t, a, 80, 30, where("bottom"))
		for _, size := range [][2]int{{120, 40}, {100, 24}} {
			a, _ = send(t, a, windowSize(size[0], size[1]))
			fitsScreen(t, a, size[0], size[1], where(fmt.Sprintf("%dx%d", size[0], size[1])))
		}
	}
}

func TestTheQueueChipKeepsItsCountsWithOneRenderOnALongNamedMachine(t *testing.T) {
	const long = "Basement Workstation Under The Stairs"
	a, r := newMachinesRig(t, 80, 30)
	ms := append(r.live.Get().Machines, config.Machine{Name: long, URL: "http://127.0.0.1:2", Enabled: true})
	if err := r.live.Set("machines", ms); err != nil {
		t.Fatal(err)
	}
	rendering := renderable("Neon Water")
	rendering.Status, rendering.JobID, rendering.RenderingOn, rendering.Started = library.StatusRendering, "b-1", long, time.Now()
	lost := renderable("Night Drive")
	lost.Status, lost.JobID, lost.RenderingOn, lost.LostSince = library.StatusQueued, "attic-1", attic, time.Now()
	queued := renderable("Glass Harbor")
	queued.Status = library.StatusQueued
	for _, s := range []library.Song{rendering, lost, queued} {
		if _, err := r.store.Create(s); err != nil {
			t.Fatal(err)
		}
	}
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	for _, c := range []struct {
		width, height int
		want          []string
	}{
		{80, 30, []string{"Queue 1 rendering starting · 1 waiting for its machine · 1 queued"}},
		{120, 40, []string{"Queue 1 rendering starting on " + long + " · 1 waiting for its machine · 1 queued"}},
		{160, 40, []string{`Queue rendering "Neon Water" starting on ` + long + " · 1 waiting for its machine · 1 queued"}},
	} {
		a, _ = send(t, a, windowSize(c.width, c.height))
		for _, want := range c.want {
			if !shows(a, want) {
				t.Errorf("%dx%d: the queue chip lacks %q:\n%s", c.width, c.height, want, plain(a))
			}
		}
		fitsScreen(t, a, c.width, c.height, fmt.Sprintf("%dx%d", c.width, c.height))
		for _, l := range strings.Split(plain(a), "\n") {
			if strings.Contains(l, "Queue") && strings.Contains(l, "…") {
				t.Errorf("%dx%d: the queue chip was cut: %q", c.width, c.height, l)
			}
		}
	}
}

func TestLauncherHintsWrapAtEightyColumns(t *testing.T) {
	long := func(title string) renderNow {
		s := renderable(title)
		s.Status = library.StatusRendering
		return renderNow{song: s}
	}
	two := launcherState{checked: true, songs: 2, writerModel: "songwriter:7b", renders: []renderNow{
		long("The Long Road Back To The Place Where It All Started"),
		long("A Second Song With A Title That Also Runs On And On"),
	}}
	two.rendering = &two.renders[0].song
	one := launcherState{checked: true, songs: 1, writerModel: "songwriter:7b",
		renders: []renderNow{long("The Long Road Back To The Place Where It All Started")},
		where:   "Basement Workstation Under The Stairs At The Back Of The House"}
	one.rendering = &one.renders[0].song

	for _, c := range []struct {
		name   string
		st     launcherState
		action launcherAction
		hint   string
	}{
		{"two renders", two, actionNowRendering, "The Long Road Back To The Place Where It All Started starting · A Second Song With A Title That Also Runs On And On starting · open them in the library"},
		{"one render", one, actionNowRendering, "The Long Road Back To The Place Where It All Started · starting on Basement Workstation Under The Stairs At The Back Of The House · open it in the library"},
		{"quit", two, actionQuit, "Exit songsmith; each machine finishes its song and songsmith collects them next time"},
	} {
		m := launcherModel{selected: c.action}
		view := ansi.Strip(m.fullView(c.st, 80))
		var text strings.Builder
		for _, l := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(l); w > 80 {
				t.Errorf("%s: a line is %d wide: %q", c.name, w, l)
			}
			text.WriteString(l + " ")
		}
		squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
		if !strings.Contains(squash(text.String()), squash(c.hint)) {
			t.Errorf("%s: the hint is not shown whole:\n%s", c.name, view)
		}
	}
}
