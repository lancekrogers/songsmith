package tui

import (
	"fmt"
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
		fitsScreen(t, a, size[0], size[1], fmt.Sprintf("%dx%d", size[0], size[1]))

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
	a, _ = send(t, a, windowSize(80, 30))
	if !shows(a, "Queue 2 rendering · 1 waiting for its machine · 1 queued") {
		t.Fatalf("queue chip at 80 columns:\n%s", plain(a))
	}
	for _, l := range strings.Split(plain(a), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("a line is %d wide: %q", w, l)
		}
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

func TestTheMasterScreenSaysWhereEachTakeWasRendered(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	song := masterSong(t, store, "song.flac", "song-2.flac", "song.master-1.flac")
	song.Takes = []library.Take{
		{File: "song.flac", Kind: library.TakeRender, Machine: studio},
		{File: "song-2.flac", Kind: library.TakeRender},
		{File: "song.master-1.flac", Kind: library.TakeMaster, Source: "song.flac"},
	}
	if err := store.Save(song); err != nil {
		t.Fatal(err)
	}
	a = openMasterOn(t, a, 120, 40)
	row := masterRow(t, a, anchorTakes)
	if len(row.extras) != 3 || !strings.Contains(row.extras[0], "on Studio Mac") ||
		strings.Contains(row.extras[1], "This computer") || !strings.Contains(row.extras[2], "from a render on Studio Mac") {
		t.Fatalf("take extras without machines added: %q", row.extras)
	}
	if err := a.d.Config.Set("machines", []config.Machine{{Name: studio, URL: "http://127.0.0.1:2"}}); err != nil {
		t.Fatal(err)
	}
	if row := masterRow(t, a, anchorTakes); !strings.Contains(row.extras[1], "on This computer") {
		t.Fatalf("a take rendered here does not say so once machines are added: %q", row.extras)
	}
}

func TestAQueuedSongSaysWhyNoMachineTakesIt(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	if err := a.d.Config.Set("local.enabled", false); err != nil {
		t.Fatal(err)
	}
	s, err := store.Create(renderable("Solo"))
	if err != nil {
		t.Fatal(err)
	}
	s.Status = library.StatusQueued
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	a = selectTitle(t, a, "Solo")
	if d := strings.Join(strings.Fields(ansi.Strip(a.library.detail.View())), " "); !strings.Contains(d, "Waits: every machine is turned off. Turn one on in Settings → Machines.") {
		t.Errorf("detail of a song no machine takes:\n%s", d)
	}
	if !listShows(a, "↳ waits: every machine is turned off") {
		t.Errorf("list:\n%s", plain(a))
	}
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	a = finishHealth(t, a)
	if v := plain(a); !shows(a, "Every machine is turned off; songs wait until one is on (Settings → Machines)") || strings.Contains(v, "Ready to render") {
		t.Errorf("launcher with every machine off:\n%s", v)
	}

	if err := a.d.Config.Set("local.enabled", true); err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	a = selectTitle(t, a, "Solo")
	if v := plain(a); strings.Contains(v, "every machine is turned off") || strings.Contains(v, "↳") {
		t.Errorf("a reason is still shown once this computer is on:\n%s", v)
	}
}

func TestAQueuedSongNeverClaimsAFreeMachineWhenNoneIsOn(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		queuedWithNoMachineOn(t, size[0], size[1])
	}
}

func queuedWithNoMachineOn(t *testing.T, width, height int) {
	where := fmt.Sprintf("%dx%d", width, height)
	a, r := newMachinesRig(t, width, height)
	ms := r.live.Get().Machines
	for i := range ms {
		ms[i].Enabled = false
	}
	ms = append(ms, config.Machine{Name: "Loft", URL: "http://127.0.0.1:3", Enabled: false})
	for _, kv := range []savedKV{{"machines", ms}, {"local.enabled", false}} {
		if err := r.live.Set(kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	free, err := r.store.Create(renderable("Free Song"))
	if err != nil {
		t.Fatal(err)
	}
	free.Status = library.StatusQueued
	pinned := renderable("Pinned Song")
	pinned.Status, pinned.Machine = library.StatusQueued, studio
	gone := renderable("Gone Song")
	gone.Status, gone.Machine = library.StatusQueued, "Old Laptop"
	for _, s := range []library.Song{free, pinned, gone} {
		if s.ID == "" {
			if _, err := r.store.Create(s); err != nil {
				t.Fatal(err)
			}
		} else if err := r.store.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	for title, want := range map[string]string{
		"Free Song":   "Waits: every machine is turned off.",
		"Pinned Song": "Waits for Studio Mac, the machine it is set to render on, which is turned off.",
		"Gone Song":   "Waits for Old Laptop, the machine it is set to render on, which is not one of the machines.",
	} {
		a = selectTitle(t, a, title)
		d := strings.Join(strings.Fields(ansi.Strip(a.library.detail.View())), " ")
		if !strings.Contains(d, want) || strings.Contains(d, "Goes to the first free machine") {
			t.Errorf("%s: %s: detail lacks %q:\n%s", where, title, want, d)
		}
		fitsScreen(t, a, width, height, where+" "+title)
	}
	for _, want := range []string{"↳ waits: every machine is turned off", "↳ waits: Studio Mac is turned off", "↳ waits: Old Laptop is not one of the machines"} {
		if !listShows(a, want) {
			t.Errorf("%s: list lacks %q:\n%s", where, want, plain(a))
		}
	}
	a, _ = send(t, a, switchScreenMsg(screenLauncher))
	if !shows(a, "Every machine is turned off; songs wait until one is on (Settings → Machines)") {
		t.Errorf("%s: launcher:\n%s", where, plain(a))
	}
	fitsScreen(t, a, width, height, where+" launcher")
}

func TestCancellingASongOnASilentMachineDoesNotHoldUpTheKeys(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	s := renderable("Night Drive")
	s.Status, s.JobID, s.RenderingOn = library.StatusQueued, "studio-1", studio
	s, err := r.store.Create(s)
	if err != nil {
		t.Fatal(err)
	}
	r.servers[studio].SetSilent(true)
	a = runCmd(t, a, a.loadSongs())
	a, _ = send(t, a, switchScreenMsg(screenLibrary))
	a = selectSong(t, a, s.ID)
	for _, k := range []string{"x", "down", "up"} {
		start := time.Now()
		a, _ = send(t, a, key(k))
		if d := time.Since(start); d > time.Second {
			t.Fatalf("%s took %s while the machine did not answer", k, d)
		}
	}
	if got, _ := r.store.Load(s.ID); got.JobID != "" || got.Status != library.StatusDraft {
		t.Fatalf("after cancelling: %+v", got)
	}
}
