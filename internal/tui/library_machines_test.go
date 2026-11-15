package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

// busyLibrary is the rig rendering two songs at once, on this computer and
// Studio Mac, with Night Drive waiting for Attic PC, which stopped answering
// mid-render, Glass Harbor queued for Studio Mac, and Paper Moon finished on
// Studio Mac. It returns the app and the IDs of the two songs rendering, by
// machine.
func busyLibrary(t *testing.T, width, height int) (app, *machinesRig, map[string]string) {
	t.Helper()
	a, r := newMachinesRig(t, width, height)
	r.local.SetHold(true)
	r.servers[studio].SetHold(true)

	done, err := r.store.Create(renderable("Paper Moon"))
	if err != nil {
		t.Fatal(err)
	}
	done.Audio = "song.flac"
	if err := os.WriteFile(r.store.AudioPath(done), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	done.Status, done.Duration = library.StatusDone, 58
	done.Takes = []library.Take{{File: done.Audio, Kind: library.TakeRender, Machine: studio, Created: time.Now()}}
	if err := r.store.Save(done); err != nil {
		t.Fatal(err)
	}
	lost := renderable("Night Drive")
	lost.Status, lost.JobID, lost.RenderingOn, lost.LostSince = library.StatusQueued, "attic-1", attic, time.Now()
	if _, err := r.store.Create(lost); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Neon Water", "Lantern Road"} {
		s, err := r.store.Create(renderable(title))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.queue.Enqueue(s); err != nil {
			t.Fatal(err)
		}
	}
	r.run()
	jobs := map[string]string{}
	for name, srv := range map[string]interface{ Queued() <-chan string }{config.ThisComputer: r.local, studio: r.servers[studio]} {
		select {
		case jobs[name] = <-srv.Queued():
		case <-time.After(10 * time.Second):
			t.Fatalf("%s got no song", name)
		}
	}
	pinned := renderable("Glass Harbor")
	pinned.Machine = studio
	pinned, err = r.store.Create(pinned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.queue.Enqueue(pinned); err != nil {
		t.Fatal(err)
	}
	r.local.Progress(jobs[config.ThisComputer], 15, 30)
	r.servers[studio].Progress(jobs[studio], 6, 30)

	ids := map[string]string{}
	eventually(t, func() bool {
		songs, _ := r.store.List()
		for _, s := range songs {
			if s.Status == library.StatusRendering {
				ids[cmpOr(s.RenderingOn, config.ThisComputer)] = s.ID
			}
		}
		return len(ids) == 2
	})
	a = drainUpdates(t, a, func(a app) bool {
		return a.library.progress[ids[config.ThisComputer]].Value == 15 && a.library.progress[ids[studio]].Value == 6
	})
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	return a, r, ids
}

// listShows reports whether the list pane holds text, however it wraps.
func listShows(a app, text string) bool {
	squash := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "│", "")), "") }
	return strings.Contains(squash(listRows(a)), squash(text))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// drainUpdates feeds queue updates to the app until cond holds.
func drainUpdates(t *testing.T, a app, cond func(app) bool) app {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond(a) {
		select {
		case u := <-a.updates:
			a, _ = send(t, a, updateMsg(u))
		case <-deadline:
			t.Fatalf("updates never got there; progress %+v", a.library.progress)
		}
	}
	return a
}

// selectTitle puts the library cursor on the song called title.
func selectTitle(t *testing.T, a app, title string) app {
	t.Helper()
	for _, s := range a.library.songs {
		if s.Title == title {
			a = selectSong(t, a, s.ID)
			a.layoutDetail()
			return a
		}
	}
	t.Fatalf("no song %q in the list", title)
	return a
}

func TestTheLibraryShowsEachRenderOnItsMachineWithItsOwnProgress(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 30}} {
		a, _, ids := busyLibrary(t, size[0], size[1])
		view := plain(a)
		for _, want := range []string{
			"↳ on This computer · 50%", "↳ on Studio Mac · 20%",
			"↳ waiting for Attic PC to come back", "↳ waits for Studio Mac",
		} {
			if !listShows(a, want) {
				t.Errorf("%dx%d: list does not show %q:\n%s", size[0], size[1], want, view)
			}
		}
		for _, l := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: a line is %d wide: %q", size[0], size[1], w, l)
			}
		}

		studioSong, _ := a.songByID(ids[studio])
		a = selectTitle(t, a, studioSong.Title)
		detail := ansi.Strip(a.library.detail.View())
		if !strings.Contains(detail, "Rendering on Studio Mac") || !strings.Contains(detail, "20%") {
			t.Errorf("detail of the song on Studio Mac:\n%s", detail)
		}
		a = selectTitle(t, a, "Night Drive")
		if d := strings.Join(strings.Fields(ansi.Strip(a.library.detail.View())), " "); !strings.Contains(d, "Waiting for Attic PC to come back.") ||
			!strings.Contains(d, "queues the song again after 10m") {
			t.Errorf("detail of the song waiting for Attic PC:\n%s", d)
		}
		a = selectTitle(t, a, "Glass Harbor")
		if d := strings.Join(strings.Fields(ansi.Strip(a.library.detail.View())), " "); !strings.Contains(d, "Waits for Studio Mac, the machine it is set to render on") {
			t.Errorf("detail of the song pinned to Studio Mac:\n%s", d)
		}
		a = selectTitle(t, a, "Paper Moon")
		if d := ansi.Strip(a.library.detail.View()); !strings.Contains(d, "take 1 of 1 · render on Studio Mac") {
			t.Errorf("detail of a take rendered on Studio Mac:\n%s", d)
		}
	}
}

func TestCancelStopsOnlyTheSelectedRender(t *testing.T) {
	a, r, ids := busyLibrary(t, 120, 40)
	studioSong, _ := a.songByID(ids[studio])
	a = selectTitle(t, a, studioSong.Title)
	if got := footerLine(a); !strings.Contains(plain(a), "x cancel") {
		t.Fatalf("no cancel key: %q", got)
	}
	a, _ = send(t, a, key("x"))
	eventually(t, func() bool {
		s, _ := r.store.Load(ids[studio])
		return s.Status != library.StatusRendering
	})
	if s, _ := r.store.Load(ids[config.ThisComputer]); s.Status != library.StatusRendering {
		t.Fatalf("the render on this computer stopped too: %s", s.Status)
	}
	if !strings.Contains(plain(a), "Cancelled "+studioSong.Title) {
		t.Fatalf("no note:\n%s", plain(a))
	}
}

func TestTheLauncherCountsRendersInFlight(t *testing.T) {
	a, _, _ := busyLibrary(t, 120, 40)
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	view := plain(a)
	if !strings.Contains(view, "Queue 2 rendering · 1 waiting for its machine · 1 queued") {
		t.Fatalf("queue chip:\n%s", view)
	}
	if !strings.Contains(view, "Now rendering (2)") {
		t.Fatalf("no Now rendering item for both:\n%s", view)
	}
}

func TestWithoutMachinesTheLibraryAndLauncherReadAsBefore(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	s, err := store.Create(renderable("Solo"))
	if err != nil {
		t.Fatal(err)
	}
	s.Status, s.Started = library.StatusRendering, time.Now()
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	if v := plain(a); strings.Contains(v, "↳") || strings.Contains(v, "Rendering on") || strings.Contains(v, "This computer") {
		t.Fatalf("machine text without machines:\n%s", v)
	}
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	if v := plain(a); !strings.Contains(v, `Queue rendering "Solo" starting`) || strings.Contains(v, " on This computer") {
		t.Fatalf("queue chip without machines:\n%s", v)
	}
}
