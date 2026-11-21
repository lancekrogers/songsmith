package tui

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
	"github.com/lancekrogers/songsmith/internal/tailnet"
)

// tailnetRig is the machines rig with a tailnet of stand-ins: Den iMac
// ready, Kitchen PC with nothing listening, Garage Mini offline on
// Tailscale and Studio Mac, which the rig has added already.
type tailnetRig struct {
	*machinesRig
	den   *comfytest.Server
	reads atomic.Int32
	list  []machines.Candidate
}

func newTailnetRig(t *testing.T, width, height int) (app, *tailnetRig) {
	t.Helper()
	a, r := newMachinesRig(t, width, height)
	tr := &tailnetRig{machinesRig: r, den: comfytest.New(t, "den")}
	models := r.live.Get().ComfyUI
	tr.den.SetMemory(96 * comfytest.GiB)
	tr.den.SetModels(models.DiffusionModel, models.TextEncoder, models.VAE)
	kitchen := comfytest.New(t, "kitchen")
	kitchen.Close()
	tr.list = []machines.Candidate{
		{Name: "Den iMac", Online: true, OS: "macOS", Address: "100.64.0.4", URLs: []string{tr.den.URL}, Hosts: []string{"100.64.0.4"}},
		{Name: "Garage Mini", Online: false, OS: "macOS", Address: "100.64.0.7", URLs: []string{"http://127.0.0.1:1"}, Hosts: []string{"100.64.0.7"}},
		{Name: "Kitchen PC", Online: true, OS: "windows", Address: "100.64.0.6", URLs: []string{kitchen.URL}, Hosts: []string{"100.64.0.6"}},
		{Name: "Studio Mac", Online: true, OS: "macOS", Address: "100.64.0.2", URLs: []string{r.servers[studio].URL}, Hosts: []string{"100.64.0.2"}},
	}
	a.d.FindMachines = func(context.Context) ([]machines.Candidate, error) {
		tr.reads.Add(1)
		return tr.list, nil
	}
	return a, tr
}

func TestFindOnTailscaleAsksNothingBeforeEnter(t *testing.T) {
	a, r := newTailnetRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	if lineOfAnchor(a, "find machines") < 0 || lineOfAnchor(a, "add machine") < 0 {
		t.Fatalf("no Find on Tailscale or Add:\n%s", plain(a))
	}
	if r.reads.Load() != 0 || a.settings.machines.find != nil {
		t.Fatal("the tailnet was read before enter on Find on Tailscale")
	}
	rows, lines := a.settingsLayout()
	find, add := lineOfAnchor(a, "find machines"), lineOfAnchor(a, "add machine")
	for i := find + 1; i < add; i++ {
		if lines[i].focusable() {
			t.Fatalf("%q is between Find on Tailscale and Add", rows[lines[i].row].name)
		}
	}
	a = focusAnchor(t, a, "find machines")
	if got := footerLine(a); !strings.Contains(got, "enter find") {
		t.Fatalf("footer on Find: %q", got)
	}
}

func TestFoundComputersFillInOneByOne(t *testing.T) {
	a, r := newTailnetRig(t, 120, 40)
	release := make(chan struct{})
	var once sync.Once
	a.d.CheckMachine = func(ctx context.Context, url string, models config.ComfyUI) machines.Status {
		if url == r.den.URL {
			<-release
		}
		return machines.Check(ctx, url, models)
	}
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "find machines")
	a, cmd := send(t, a, key("enter"))
	if !strings.Contains(plain(a), "asking Tailscale…") {
		t.Fatalf("no reading state:\n%s", plain(a))
	}
	done := make(chan struct{})
	go func() {
		runCmds(cmd)
		close(done)
	}()
	pump := func(cond func(app) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond(a) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out:\n%s", plain(a))
			}
			if msg, ok := a.events.next(); ok {
				a, _ = send(t, a, msg)
				continue
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	pump(func(a app) bool {
		f := a.settings.machines.find
		return f != nil && !f.looking && f.results[2].Asked
	})
	view := plain(a)
	for _, want := range []string{"Den iMac", "asking…", "Kitchen PC", "ComfyUI not answering", "Garage Mini", "offline on Tailscale", "already added as Studio Mac"} {
		if !strings.Contains(view, want) {
			t.Errorf("%q not shown while Den iMac is being asked:\n%s", want, view)
		}
	}
	if !shows(a, "4 computers on Tailscale · asking each whether its ComfyUI answers") {
		t.Errorf("no note that the computers are being asked:\n%s", view)
	}
	once.Do(func() { close(release) })
	pump(func(a app) bool { _, ok := a.settings.machines.find.results[0]; return ok })
	<-done
	if shows(a, "asking each") || !shows(a, "4 computers on Tailscale · enter on one adds it") {
		t.Errorf("the note still says the computers are being asked once all have answered:\n%s", plain(a))
	}
	if !shows(a, "ready · mps · 96 GB · ComfyUI 0.3.99") {
		t.Fatalf("Den iMac's result:\n%s", plain(a))
	}
	if name, _ := focusedName(a); name != "Find on Tailscale" {
		t.Fatalf("the cursor moved off Find on Tailscale to %q", name)
	}
}

func TestAFoundComputerIsNamedCheckedAndSaved(t *testing.T) {
	a, r := newTailnetRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "find machines")
	a = pressSettings(t, a, "enter")
	a = focusAnchor(t, a, "found 100.64.0.6")
	if !shows(a, "Nothing answers at 100.64.0.6 on port") || !shows(a, "set comfyui.host to 100.64.0.6 in its songsmith config and run songsmith server start.") {
		t.Fatalf("no fix under a computer that does not answer:\n%s", plain(a))
	}
	a = focusAnchor(t, a, "found 100.64.0.7")
	if !shows(a, "Offline on Tailscale: it can still be added, and songs go to it once it answers") {
		t.Fatalf("no note under an offline computer:\n%s", plain(a))
	}
	a = focusAnchor(t, a, "found 100.64.0.2")
	a = pressSettings(t, a, "enter")
	if a.settings.edit.active {
		t.Fatal("an added computer opened the name editor")
	}

	a = focusAnchor(t, a, "found 100.64.0.4")
	if got := footerLine(a); !strings.Contains(got, "enter add") {
		t.Fatalf("footer on a found computer: %q", got)
	}
	a = pressSettings(t, a, "enter")
	if !a.settings.edit.active || a.settings.edit.label != "Name" || a.settings.edit.input.Value() != "Den iMac" {
		t.Fatalf("name editor: %+v %q", a.settings.edit.active, a.settings.edit.input.Value())
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "studio mac")
	a = pressSettings(t, a, "enter")
	if !shows(a, "There is already a machine named Studio Mac") {
		t.Fatalf("a taken name was accepted:\n%s", plain(a))
	}
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "Den iMac")
	a = pressSettings(t, a, "enter")
	view := plain(a)
	for _, want := range []string{"Add a machine · Den iMac", "ready · ComfyUI 0.3.99", r.den.URL, "96 GB · mps", "add Den iMac"} {
		if !strings.Contains(view, want) {
			t.Fatalf("%q not in the draft:\n%s", want, view)
		}
	}
	if _, ok := r.live.Get().Machine("Den iMac"); ok {
		t.Fatal("saved before Save")
	}
	a = focusAnchor(t, a, "draft save")
	a = pressSettings(t, a, "enter")
	m, ok := r.live.Get().Machine("Den iMac")
	if !ok || m.URL != r.den.URL {
		t.Fatalf("saved %+v %v", m, ok)
	}
	if saved, _ := config.Load(r.live.Path()); func() bool { _, ok := saved.Machine("Den iMac"); return !ok }() {
		t.Fatal("not in the config file")
	}
	if got := foundValue(t, a, "100.64.0.4"); got != "already added as Den iMac" {
		t.Fatalf("the found row says %q once Den iMac was saved", got)
	}

	a = focusAnchor(t, a, "machine studio mac remove")
	a = pressSettings(t, a, "enter", "y")
	if got := foundValue(t, a, "100.64.0.2"); got != "not asked" {
		t.Fatalf("the found row says %q once Studio Mac was removed", got)
	}
	a = focusAnchor(t, a, "found 100.64.0.2")
	if !shows(a, "look again to ask it") {
		t.Fatalf("no note under a computer not asked:\n%s", plain(a))
	}
	if n := r.reads.Load(); n != 1 {
		t.Fatalf("the tailnet was read %d times", n)
	}
}

func TestADiscardedFoundComputerReturnsToItsRow(t *testing.T) {
	a, _ := newTailnetRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "find machines")
	a = pressSettings(t, a, "enter")
	a = focusAnchor(t, a, "found 100.64.0.6")
	a = pressSettings(t, a, "enter", "enter")
	if !strings.Contains(plain(a), "add Kitchen PC anyway") {
		t.Fatalf("a computer that does not answer is not offered to save anyway:\n%s", plain(a))
	}
	a = focusAnchor(t, a, "draft discard")
	a = pressSettings(t, a, "enter")
	if a.settings.marks[sectionMachines].name != "found 100.64.0.6" {
		t.Fatalf("the cursor went to %+v", a.settings.marks[sectionMachines])
	}
}

func TestLookAgainReadsTheTailnetAgainAndDropsTheOldResults(t *testing.T) {
	a, r := newTailnetRig(t, 120, 40)
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "find machines")
	a = pressSettings(t, a, "enter")
	old := a.settings.machines.find.seq
	if !shows(a, "look again") {
		t.Fatalf("Find does not offer to look again:\n%s", plain(a))
	}
	r.list = r.list[:1]
	a = pressSettings(t, a, "enter")
	if n := r.reads.Load(); n != 2 {
		t.Fatalf("read %d times", n)
	}
	if name, _ := focusedName(a); name != "Find on Tailscale" {
		t.Fatalf("the cursor moved to %q", name)
	}
	if shows(a, "Kitchen PC") {
		t.Fatalf("a computer from the old run is still listed:\n%s", plain(a))
	}
	a, _ = send(t, a, tailnetReadMsg{seq: old, found: []machines.Candidate{{Name: "Stale Box", Address: "100.64.0.99"}}})
	a, _ = send(t, a, machineAskedMsg{seq: old, i: 0, found: machines.Found{Asked: true}})
	if shows(a, "Stale Box") || !shows(a, "ready · mps · 96 GB") {
		t.Fatalf("an older run's results overwrote the newer one:\n%s", plain(a))
	}
}

func TestNoTailscaleIsOneSentenceAndAddStaysThere(t *testing.T) {
	a, _ := newTailnetRig(t, 80, 24)
	r := tailnet.Reader{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }, Exists: func(string) bool { return false }}
	a.d.FindMachines = func(ctx context.Context) ([]machines.Candidate, error) {
		_, err := r.Peers(ctx)
		return nil, err
	}
	a = openSettingsOn(t, a, sectionMachines)
	a = focusAnchor(t, a, "find machines")
	a = pressSettings(t, a, "enter")
	want := "Tailscale is not installed on this computer; install it from tailscale.com, or add a machine by its address."
	views := screens(t, a)
	if !seen(views, want) {
		t.Fatalf("the reason is not shown whole:\n%s", strings.Join(views, "\n"))
	}
	assertFits(t, views, 80)
	if lineOfAnchor(a, "add machine") < 0 {
		t.Fatal("Add is gone")
	}

	a.d.FindMachines = func(context.Context) ([]machines.Candidate, error) {
		return nil, errors.New("Tailscale is stopped on this computer; start it and try again")
	}
	a = focusAnchor(t, a, "find machines")
	a = pressSettings(t, a, "enter")
	if !shows(a, "Tailscale is stopped on this computer; start it and try again.") {
		t.Fatalf("stopped:\n%s", plain(a))
	}
	a.d.FindMachines = func(context.Context) ([]machines.Candidate, error) { return nil, nil }
	a = focusAnchor(t, a, "find machines")
	a = pressSettings(t, a, "enter")
	if !shows(a, "Tailscale lists no other computers that could run ComfyUI; add one by its address below.") {
		t.Fatalf("none found:\n%s", plain(a))
	}
}

func foundValue(t *testing.T, a app, addr string) string {
	t.Helper()
	for _, r := range a.sectionRows() {
		if r.anchorName() == "found "+addr {
			return r.value
		}
	}
	t.Fatalf("no row for %s:\n%s", addr, plain(a))
	return ""
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	_, port := hostPort(t, raw)
	return strconv.Itoa(port)
}
