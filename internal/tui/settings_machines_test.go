package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

func TestMachinesShowsEachMachineAndWhatItsCheckFound(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		width := size[0]
		a, r := newMachinesRig(t, width, size[1])
		a = openSettingsOn(t, a, sectionMachines)
		views := screens(t, a)
		for _, want := range []string{
			"This computer", "ready · ComfyUI 0.3.99", "41.0 s per second of audio",
			studio, r.servers[studio].URL, "18.0 s per second of audio", "64 GB · mps",
			attic, "offline", "connection refused", "Check that the computer is awake",
			air, "models missing", "Missing model files: minimax_music3_dav.safetensors",
			"Renders new songs", "Remove", "stop rendering on " + air,
			"Add", "type a name, then its address",
			"songsmith setup --install there is the easy way", "run songsmith server start",
			"ComfyUI has no login", "private network, such as the computer's Tailscale name",
		} {
			if !seen(views, want) {
				t.Errorf("width %d: %q is never shown:\n%s", width, want, strings.Join(views, "\n"))
			}
		}
		for _, v := range views {
			for _, l := range strings.Split(v, "\n") {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("width %d: a line is %d wide: %q", width, w, l)
				}
				if strings.Contains(l, "…") && !strings.Contains(l, "checking…") {
					t.Errorf("width %d: a line was cut: %q", width, l)
				}
			}
		}
	}
}

func TestEachMachineRowSaysWhenAChangeApplies(t *testing.T) {
	a, _ := newMachinesRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	for _, anchor := range []string{"machine this computer toggle", "machine studio mac toggle", "machine studio mac remove", "add machine"} {
		a = focusAnchor(t, a, anchor)
		if !strings.Contains(plain(a), "Applies to the next song sent") {
			t.Errorf("%s does not say when it applies:\n%s", anchor, plain(a))
		}
	}
}

func TestTheToggleSendsNewSongsElsewhere(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "machine this computer toggle")
	a = pressSettings(t, a, "enter")
	if saved, _ := config.Load(r.live.Path()); saved.Local.Enabled || r.live.Get().Local.Enabled {
		t.Fatalf("local.enabled still on")
	}
	if !strings.Contains(plain(a), "Applies to the next song sent · This computer renders new songs: OFF") {
		t.Fatalf("status line:\n%s", plain(a))
	}
	a = focusAnchor(t, a, "machine attic pc toggle")
	a = pressSettings(t, a, " ")
	m, _ := r.live.Get().Machine(attic)
	if m.Enabled {
		t.Fatalf("Attic PC still enabled")
	}
	if !strings.Contains(plain(a), "Attic PC · gets no new songs") {
		t.Fatalf("the heading does not say it is off:\n%s", plain(a))
	}
}

func TestAddingAMachineChecksItAndSavesOnlyOnSave(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	den := comfytest.New(t, "den")
	den.SetMemory(128 * comfytest.GiB)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "add machine")
	a = pressSettings(t, a, "enter")
	if !strings.Contains(plain(a), "Name:") {
		t.Fatalf("no name editor:\n%s", plain(a))
	}
	a = typeIntoSettings(t, a, "Den iMac")
	a = pressSettings(t, a, "enter")
	if !strings.Contains(plain(a), "Address of Den iMac:") {
		t.Fatalf("no address editor after the name:\n%s", plain(a))
	}
	addr := strings.TrimPrefix(den.URL, "http://")
	a = typeIntoSettings(t, a, addr)
	if !strings.Contains(plain(a), "checks "+den.URL+" before saving") {
		t.Fatalf("the editor does not say what it will check:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	view := plain(a)
	for _, want := range []string{"Add a machine · Den iMac", "ready · ComfyUI 0.3.99", den.URL, "128 GB · mps", "add Den iMac", "don't add it"} {
		if !strings.Contains(view, want) {
			t.Fatalf("%q not shown after the check:\n%s", want, view)
		}
	}
	if _, ok := r.live.Get().Machine("Den iMac"); ok {
		t.Fatal("saved before Save")
	}
	if saved, _ := os.ReadFile(r.live.Path()); strings.Contains(string(saved), "Den iMac") {
		t.Fatal("written to the config file before Save")
	}
	a = focusAnchor(t, a, "draft save")
	if got := footerLine(a); !strings.Contains(got, "enter save") {
		t.Fatalf("footer on Save: %q", got)
	}
	a = pressSettings(t, a, "enter")
	m, ok := r.live.Get().Machine("Den iMac")
	if !ok || m.URL != den.URL || !m.Enabled {
		t.Fatalf("running config: %+v %v", m, ok)
	}
	saved, err := config.Load(r.live.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Machine("Den iMac"); !ok {
		t.Fatal("not in the config file")
	}
	view = plain(a)
	if !strings.Contains(view, "Applies to the next song sent · Added Den iMac at "+den.URL) {
		t.Fatalf("status line after Save:\n%s", view)
	}
	if strings.Contains(view, "Add a machine · Den iMac") {
		t.Fatalf("the draft stayed after Save:\n%s", view)
	}
	if name, _ := focusedName(a); name != "Status" || a.settings.marks[sectionMachines].name != "machine den imac status" {
		t.Fatalf("cursor after Save: %v", a.settings.marks[sectionMachines])
	}
}

func TestADiscardedMachineIsNotSaved(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "add machine")
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, "Den iMac")
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, "127.0.0.1:1")
	a = pressSettings(t, a, "enter")
	if !strings.Contains(plain(a), "add Den iMac anyway") {
		t.Fatalf("an offline machine is not offered to save anyway:\n%s", plain(a))
	}
	a = focusAnchor(t, a, "draft discard")
	a = pressSettings(t, a, "enter")
	if _, ok := r.live.Get().Machine("Den iMac"); ok || a.settings.machines.add != nil {
		t.Fatal("a discarded machine was kept")
	}
	if !strings.Contains(plain(a), "Did not add Den iMac") {
		t.Fatalf("status line:\n%s", plain(a))
	}
}

func TestAddSaysWhyANameOrAddressCannotBeUsed(t *testing.T) {
	a, r := newMachinesRig(t, 80, 40)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "add machine")
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, "this computer")
	a = pressSettings(t, a, "enter")
	if !shows(a, "is the name songsmith uses for the computer it runs on") || !a.settings.edit.active {
		t.Fatalf("this computer's name was taken:\n%s", plain(a))
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "studio mac")
	a = pressSettings(t, a, "enter")
	if !shows(a, "There is already a machine named Studio Mac") {
		t.Fatalf("a duplicate name was taken:\n%s", plain(a))
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "Den iMac")
	a = pressSettings(t, a, "enter")
	for _, bad := range []struct{ typed, why string }{
		{r.servers[studio].URL, "is also the address of machine \"Studio Mac\""},
		{r.local.URL, "is this computer's own ComfyUI server"},
		{"ftp://den:8188", "a ComfyUI address starts with http:// or https://"},
	} {
		a = clearEditor(t, a)
		a = typeIntoSettings(t, a, bad.typed)
		a = pressSettings(t, a, "enter")
		if !shows(a, bad.why) || !a.settings.edit.active || a.settings.machines.add != nil {
			t.Fatalf("%s: %q not shown, or the editor closed:\n%s", bad.typed, bad.why, plain(a))
		}
		for _, l := range strings.Split(plain(a), "\n") {
			if strings.Contains(l, "…") {
				t.Fatalf("the reason was cut: %q", l)
			}
		}
	}
	long := strings.Repeat("a", 1500)
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, long)
	if got := a.settings.edit.input.Value(); got != long {
		t.Fatalf("the address was capped at %d characters", len(got))
	}
}

func TestASlowMachineNeverHoldsUpTheScreen(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	r.servers[studio].SetSilent(true)
	a, cmd := send(t, a, switchScreenMsg(screenSettings))
	a = settle(t, a, cmd)
	a, cmd = send(t, a, settingsKey(string(rune('1'+sectionMachines))))
	// The checks are left to run on their own, as Bubble Tea runs them.
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCmds(cmd) }()
	if !strings.Contains(plain(a), "checking…") {
		t.Fatalf("no checking state:\n%s", plain(a))
	}
	for _, k := range []string{"down", "down", "right", "left", "down"} {
		start := time.Now()
		a, _ = send(t, a, settingsKey(k))
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Fatalf("%s took %s while a machine was silent", k, d)
		}
	}
	a = focusAnchor(t, a, "add machine")
	a, _ = send(t, a, key("enter"))
	a = typeIntoSettings(t, a, "Den")
	if got := a.settings.edit.input.Value(); got != "Den" {
		t.Fatalf("typing during a check gave %q", got)
	}
	a, _ = send(t, a, key("esc"))
	r.servers[studio].SetSilent(false)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the check never ended")
	}
	a = settle(t, a, nil)
	for {
		msg, ok := a.events.next()
		if !ok {
			break
		}
		a, _ = send(t, a, msg)
	}
	if c := a.settings.machines.checks["studio mac"]; c.checking {
		t.Fatalf("still checking after the server answered: %+v", c)
	}
}

func TestRemoveAsksFirstAndAnyOtherKeyKeepsTheMachine(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	pinned := renderable("Night Drive")
	pinned.Machine = attic
	pinned.Status = library.StatusQueued
	if _, err := r.store.Create(pinned); err != nil {
		t.Fatal(err)
	}
	held := renderable("Coast Road")
	held.Status, held.JobID, held.RenderingOn, held.LostSince = library.StatusQueued, "attic-3", attic, time.Now()
	held, err := r.store.Create(held)
	if err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "machine attic pc remove")
	if got := footerLine(a); !strings.Contains(got, "enter remove") {
		t.Fatalf("footer on Remove: %q", got)
	}
	a = pressSettings(t, a, "enter")
	view := plain(a)
	if !shows(a, "Remove Attic PC? Coast Road is queued again for another machine, and its render there is left alone. "+
		"1 song set to render on it waits until you choose another machine for it.") {
		t.Fatalf("the question does not say what happens to its songs:\n%s", view)
	}
	if got := footerLine(a); !strings.Contains(got, "y remove Attic PC") || !strings.Contains(got, "any other key keeps it") || strings.Contains(got, "←/→") {
		t.Fatalf("footer while asking: %q", got)
	}
	a = pressSettings(t, a, "right")
	if a.settings.section != sectionMachines {
		t.Fatal("a key other than y did more than keep the machine")
	}
	if _, ok := r.live.Get().Machine(attic); !ok || !strings.Contains(plain(a), "Kept Attic PC") {
		t.Fatalf("removed without y:\n%s", plain(a))
	}
	r.run()
	a = pressSettings(t, a, "enter", "y")
	if _, ok := r.live.Get().Machine(attic); ok {
		t.Fatal("still in the running config after y")
	}
	eventually(t, func() bool {
		s, _ := r.store.Load(held.ID)
		return s.JobID == "" && s.RenderingOn != attic
	})
	if saved, _ := config.Load(r.live.Path()); len(saved.Machines) != 2 {
		t.Fatalf("config file machines: %+v", saved.Machines)
	}
	if lineOfAnchor(a, "machine attic pc status") >= 0 {
		t.Fatalf("Attic PC still shown:\n%s", plain(a))
	}
	if !strings.Contains(plain(a), "Applies to the next song sent · Removed Attic PC") {
		t.Fatalf("status line:\n%s", plain(a))
	}
}

func TestAMachineAddedOrRemovedHereIsUsedWithoutARestart(t *testing.T) {
	a, r := newMachinesRig(t, 120, 40)
	for _, kv := range []savedKV{{"local.enabled", false}, {"machines", []config.Machine{}}} {
		if err := r.live.Set(kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	r.run()
	song, err := r.store.Create(renderable("Glass Harbor"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.queue.Enqueue(song); err != nil {
		t.Fatal(err)
	}
	den := comfytest.New(t, "den")
	den.SetHold(true)

	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "add machine")
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, "Den iMac")
	a = pressSettings(t, a, "enter")
	a = typeIntoSettings(t, a, den.URL)
	a = pressSettings(t, a, "enter")
	a = focusAnchor(t, a, "draft save")
	a = pressSettings(t, a, "enter")

	var job string
	select {
	case job = <-den.Queued():
	case <-time.After(10 * time.Second):
		t.Fatal("the song never went to the machine added in Settings")
	}
	eventually(t, func() bool {
		s, _ := r.store.Load(song.ID)
		return s.Status == library.StatusRendering && s.RenderingOn == "Den iMac"
	})
	den.Finish(job)
	eventually(t, func() bool {
		s, _ := r.store.Load(song.ID)
		return s.Status == library.StatusDone
	})

	a = focusAnchor(t, a, "machine den imac remove")
	a = pressSettings(t, a, "enter", "y")
	a = focusAnchor(t, a, "machine this computer toggle")
	a = pressSettings(t, a, "enter")
	r.local.SetHold(true)
	next, err := r.store.Create(renderable("Second Harbor"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.queue.Enqueue(next); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.local.Queued():
	case <-time.After(10 * time.Second):
		t.Fatal("the next song did not go to this computer once Den iMac was removed")
	}
	if got := len(den.Prompts()); got != 1 {
		t.Fatalf("Den iMac got %d prompts after it was removed", got)
	}
}

// focusAnchor moves the cursor onto the line anchored anchor.
func focusAnchor(t *testing.T, a app, anchor string) app {
	t.Helper()
	a.clearSettingsNote()
	i := lineOfAnchor(a, anchor)
	if i < 0 {
		t.Fatalf("no line anchored %q:\n%s", anchor, plain(a))
	}
	rows, lines := a.settingsLayout()
	a.placeSettingsCursor(rows, lines, i)
	return a
}

func lineOfAnchor(a app, anchor string) int {
	rows, lines := a.settingsLayout()
	for i, l := range lines {
		if l.focusable() && rows[l.row].anchorName() == anchor {
			return i
		}
	}
	return -1
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAComfyUIThatIsTooOldSaysWhichNodeItLacks(t *testing.T) {
	a, r := newMachinesRig(t, 80, 24)
	r.servers[studio].SetMissingNode("MiniMaxMusic3TextEncode")
	a = openSettingsOn(t, a, sectionMachines)
	views := screens(t, a)
	for _, want := range []string{"ComfyUI too old", "Missing nodes: MiniMaxMusic3TextEncode", "Update ComfyUI on that computer"} {
		if !seen(views, want) {
			t.Errorf("%q is never shown:\n%s", want, strings.Join(views, "\n"))
		}
	}
	for _, v := range views {
		for _, l := range strings.Split(v, "\n") {
			if w := ansi.StringWidth(l); w > 80 {
				t.Errorf("a line is %d wide: %q", w, l)
			}
		}
	}
}
