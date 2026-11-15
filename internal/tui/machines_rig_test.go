package tui

import (
	"context"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/machines"
	"github.com/lancekrogers/songsmith/internal/queue"
)

const (
	studio = "Studio Mac"
	attic  = "Attic PC"
	air    = "MacBook Air"
)

// machinesRig is the app over stand-in ComfyUI servers on loopback: this
// computer, Studio Mac (ready, measured), Attic PC (its address refuses
// connections) and MacBook Air (its ComfyUI cannot see the VAE file). The
// queue lists its machines from the running config as songsmith does.
type machinesRig struct {
	t       *testing.T
	live    *config.Live
	store   *library.Store
	queue   *queue.Queue
	local   *comfytest.Server
	servers map[string]*comfytest.Server
}

func newMachinesRig(t *testing.T, width, height int) (app, *machinesRig) {
	t.Helper()
	r := &machinesRig{t: t, local: comfytest.New(t, "local"), servers: map[string]*comfytest.Server{}}
	r.servers[studio] = comfytest.New(t, "studio")
	gone := comfytest.New(t, "attic")
	gone.Close()
	r.servers[air] = comfytest.New(t, "air")

	dir := t.TempDir()
	cfg := config.Default()
	cfg.LibraryDir = filepath.Join(dir, "library")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.ComfyUI.Dir = filepath.Join(dir, "comfyui")
	cfg.ComfyUI.Host, cfg.ComfyUI.Port = hostPort(t, r.local.URL)
	cfg.Ollama.URL = "http://127.0.0.1:1"
	r.servers[air].SetModels(cfg.ComfyUI.DiffusionModel, cfg.ComfyUI.TextEncoder)
	cfg.Machines = []config.Machine{
		{Name: studio, URL: r.servers[studio].URL, Enabled: true},
		{Name: attic, URL: gone.URL, Enabled: true},
		{Name: air, URL: r.servers[air].URL, Enabled: true},
	}
	r.live = config.NewLive(filepath.Join(dir, "config.yaml"), cfg)
	writeTestConfig(t, r.live)
	for _, kv := range []savedKV{{"comfyui.host", cfg.ComfyUI.Host}, {"machines", cfg.Machines}} {
		if err := r.live.Set(kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	speeds := machines.NewSpeeds(cfg.StateDir)
	if err := speeds.Record(studio, 18*time.Minute, 60); err != nil {
		t.Fatal(err)
	}
	if err := speeds.Record(config.ThisComputer, 41*time.Minute, 60); err != nil {
		t.Fatal(err)
	}

	r.store = library.NewStore(cfg.LibraryDir)
	localBackend := backend.NewComfyMiniMax(comfy.NewClient(r.local.URL), nil, r.live.Get)
	r.queue = queue.New(r.store, localBackend)
	r.queue.SetMachines(queue.ConfigMachines(r.live.Get, localBackend))
	r.queue.ReclaimAfter(func() time.Duration { return r.live.Get().ReclaimAfter })
	updates, stop := r.queue.Subscribe()
	t.Cleanup(stop)
	deps := testDeps(t, r.store, r.queue, fakeDrafter{})
	deps.Config = r.live
	deps.Server = &fakeServer{}
	deps.Checks = func(context.Context, config.Config) []health.Check { return readyChecks() }
	a := newTestApp(t, deps, updates)
	a.settings.models.list = fakeModels(testModel)
	a, _ = send(t, a, windowSize(width, height))
	return finishHealth(t, a), r
}

func hostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), port
}

// run renders the queue until the test ends.
func (r *machinesRig) run() {
	ctx, cancel := context.WithCancel(r.t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.queue.Run(ctx)
	}()
	r.t.Cleanup(func() {
		cancel()
		<-done
	})
}

// renderable is a song with everything a render needs.
func renderable(title string) library.Song {
	return library.Song{Title: title, Lyrics: sampleDraft.Lyrics, Caption: sampleDraft.Caption, Seconds: 30}
}

// screens collects the section as the cursor walks every line, top to
// bottom, so text below the fold is seen too.
func screens(t *testing.T, a app) []string {
	t.Helper()
	a = pressSettings(t, a, "home")
	var out []string
	for range 80 {
		out = append(out, plain(a))
		before := a.settings.cursor
		a = pressSettings(t, a, "down")
		if a.settings.cursor == before {
			break
		}
	}
	return out
}

// seen reports whether text is on any of views, however it is wrapped.
func seen(views []string, text string) bool {
	squash := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "▸", "")), "") }
	for _, v := range views {
		var b strings.Builder
		for _, l := range strings.Split(v, "\n") {
			if _, rest, ok := strings.Cut(l, "│"); ok {
				l = rest
			}
			b.WriteString(l)
		}
		if strings.Contains(squash(b.String()), squash(text)) {
			return true
		}
	}
	return false
}

func windowSize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }
