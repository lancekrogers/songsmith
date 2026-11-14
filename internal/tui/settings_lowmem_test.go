package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
)

func TestLowMemoryRowShowsWhatAutoChose(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.status = server.Status{Running: true, Device: "mps", GPUOnly: true, Memory: 32 << 30}
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 24})
	a = openSettingsOn(t, a, sectionRender)
	a = focus(t, a, "Low-memory rendering", -1)
	text := plain(a)
	for _, l := range strings.Split(text, "\n") {
		if lipgloss.Width(l) > 80 {
			t.Fatalf("line wider than 80 columns (%d):\n%s", lipgloss.Width(l), l)
		}
	}
	for _, want := range []string{"[auto]", "auto · on for this machine, 32 GB",
		"Frees the language model before decoding. Slower by a", "needed on machines with about 32 GB."} {
		if !strings.Contains(text, want) {
			t.Fatalf("Render lacks %q:\n%s", want, text)
		}
	}

	saves := recordSaves(&a)
	a = pressSettings(t, a, " ")
	if a.cfg().Render.LowMemory != config.LowMemoryOn || len(*saves) != 1 || (*saves)[0].value != config.LowMemoryOn {
		t.Fatalf("space: running %q, saves %+v", a.cfg().Render.LowMemory, *saves)
	}
	if text := plain(a); !strings.Contains(text, "[on]") || strings.Contains(text, "for this machine") {
		t.Fatalf("after choosing on:\n%s", text)
	}
	a = pressSettings(t, a, "+")
	if saved, _ := config.Load(a.d.Config.Path()); a.cfg().Render.LowMemory != config.LowMemoryOff || saved.Render.LowMemory != config.LowMemoryOff {
		t.Fatalf("+: running %q, saved %q", a.cfg().Render.LowMemory, saved.Render.LowMemory)
	}
}

func TestLowMemoryRowWithoutARunningServer(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	if text := plain(a); !strings.Contains(text, "auto · decided at each render") {
		t.Fatalf("Render:\n%s", text)
	}
}
