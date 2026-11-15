package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

// toMachineRow moves the form's focus down to Render on.
func toMachineRow(t *testing.T, a app) app {
	t.Helper()
	for range rowCount {
		if a.compose.focus == rowMachine {
			return a
		}
		a, _ = send(t, a, key("down"))
	}
	t.Fatalf("focus never reached Render on:\n%s", plain(a))
	return a
}

func TestRenderOnIsShownOnlyWithMoreThanOneMachine(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	if strings.Contains(plain(m), "Render on") {
		t.Fatalf("Render on shown with only this computer:\n%s", plain(m))
	}
	for range rowCount {
		if m.compose.focus == rowMachine {
			t.Fatal("focus landed on a hidden Render on row")
		}
		m, _ = send(t, m, key("down"))
	}

	a, r := newMachinesRig(t, 120, 50)
	if err := r.live.Set("machines", []config.Machine{{Name: studio, URL: r.servers[studio].URL, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, openComposeMsg{})
	if strings.Contains(plain(a), "Render on") {
		t.Fatalf("Render on shown with one machine getting new songs:\n%s", plain(a))
	}
}

func TestRenderOnPinsTheSongToTheChosenMachine(t *testing.T) {
	a, r := newMachinesRig(t, 120, 50)
	a, _ = send(t, a, openComposeMsg{})
	a = fillValid(a)
	view := plain(a)
	if !strings.Contains(view, "Render on ✓ any free machine    This computer    Studio Mac    Attic PC    MacBook Air") {
		t.Fatalf("Render on does not list every machine inline:\n%s", view)
	}
	a = toMachineRow(t, a)
	if got := footerLines(a); !strings.Contains(got, "←/→ choose where it renders") {
		t.Fatalf("footer on Render on: %q", got)
	}
	a, _ = send(t, a, key("right"), key("right"))
	if !strings.Contains(plain(a), "any free machine    This computer  ✓ Studio Mac") {
		t.Fatalf("✓ did not move to Studio Mac:\n%s", plain(a))
	}
	a, _ = send(t, a, key("ctrl+d"))
	songs, _ := r.store.List()
	if len(songs) != 1 || songs[0].Machine != studio {
		t.Fatalf("saved songs: %+v", songs)
	}

	a, _ = send(t, a, openComposeMsg{song: &songs[0]})
	if !strings.Contains(plain(a), "✓ Studio Mac") {
		t.Fatalf("the pin is not shown when the song is edited:\n%s", plain(a))
	}
	a = toMachineRow(t, a)
	a, _ = send(t, a, key("left"), key("left"), key("left"))
	if !a.compose.dirty() {
		t.Fatal("changing the pin left the form clean")
	}
	a, _ = send(t, a, key("ctrl+d"))
	if s, _ := r.store.Load(songs[0].ID); s.Machine != "" {
		t.Fatalf("pin after choosing any free machine: %q", s.Machine)
	}
}

func TestAPinToAMachineThatIsGoneStaysVisibleSoItCanBeChanged(t *testing.T) {
	a, r := newMachinesRig(t, 80, 40)
	if err := r.live.Set("machines", []config.Machine{}); err != nil {
		t.Fatal(err)
	}
	song := renderable("Night Drive")
	song.Machine = "Old Tower"
	song, err := r.store.Create(song)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, openComposeMsg{song: &song})
	if !shows(a, "✓ Old Tower (not one of the machines)") {
		t.Fatalf("the pin to a removed machine is hidden:\n%s", plain(a))
	}
	for _, l := range strings.Split(plain(a), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("a line is %d wide: %q", w, l)
		}
	}
	a = toMachineRow(t, a)
	a, _ = send(t, a, key("left"), key("left"))
	if a.compose.machine != config.ThisComputer && a.compose.machine != "" {
		t.Fatalf("pin after stepping back: %q", a.compose.machine)
	}
	a, _ = send(t, a, key("left"))
	a, _ = send(t, a, key("ctrl+d"))
	if s, _ := r.store.Load(song.ID); s.Machine != "" || s.Status != library.StatusDraft {
		t.Fatalf("saved %+v", s)
	}
}

func TestRenderOnWrapsAtEightyColumns(t *testing.T) {
	a, r := newMachinesRig(t, 80, 40)
	ms := r.live.Get().Machines
	ms = append(ms, config.Machine{Name: "Basement Workstation", URL: "http://127.0.0.1:2", Enabled: true},
		config.Machine{Name: "Living Room Mini", URL: "http://127.0.0.1:3", Enabled: true})
	if err := r.live.Set("machines", ms); err != nil {
		t.Fatal(err)
	}
	a, _ = send(t, a, openComposeMsg{})
	for _, name := range []string{"any free machine", "This computer", studio, attic, air, "Basement Workstation", "Living Room Mini"} {
		if !shows(a, name) {
			t.Errorf("%s is not on the Render on row:\n%s", name, plain(a))
		}
	}
	for _, l := range strings.Split(plain(a), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("a line is %d wide: %q", w, l)
		}
		if strings.Contains(strings.ReplaceAll(l, "loading installed models…", ""), "…") {
			t.Errorf("a line was cut: %q", l)
		}
	}
	t.Log("\n" + plain(a))
}
