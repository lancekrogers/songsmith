package cli

import (
	"context"
	"sync"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/machines"
	"github.com/lancekrogers/songsmith/internal/master"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/queue"
)

type renderer struct {
	store   *library.Store
	server  *server.Manager
	backend backend.Backend
	queue   *queue.Queue
	master  *master.Runner
}

// newRenderer builds the render stack. The folders, host and port come from
// live once; the backend reads the rest of live at the start of each render,
// and the queue reads the machines each time it sends a song.
func newRenderer(live *config.Live) *renderer {
	cfg := live.Get()
	m := server.New(cfg.ComfyUI, cfg.StateDir)
	b := backend.NewComfyMiniMax(m.Client(), backend.StarterFunc(m.Ensure), live.Get)
	store := library.NewStore(cfg.LibraryDir)
	q := queue.New(store, b)
	remotes := &remoteBackends{settings: live.Get, byKey: map[string]*backend.ComfyMiniMax{}}
	q.SetMachines(func() []queue.Machine { return machineList(live.Get(), b, remotes) })
	q.SetSpeeds(machines.NewSpeeds(cfg.StateDir))
	q.ReclaimAfter(func() time.Duration { return live.Get().ReclaimAfter })
	q.BeforeRender(func(ctx context.Context) { unloadWriter(ctx, live.Get().Ollama) })
	run := master.NewRunner()
	q.AfterRender(autoMaster(store, live.Get, run))
	return &renderer{store: store, server: m, backend: b, queue: q, master: run}
}

// machineList is this computer, then each added machine in config order.
func machineList(cfg config.Config, local backend.Backend, remotes *remoteBackends) []queue.Machine {
	out := []queue.Machine{{Name: config.ThisComputer, Backend: local, Enabled: cfg.Local.Enabled, Local: true}}
	for _, mc := range cfg.Machines {
		url := mc.URL
		out = append(out, queue.Machine{
			Name:    mc.Name,
			Backend: remotes.backend(mc.Name, url),
			Enabled: mc.Enabled,
			Ping:    func(ctx context.Context) error { return machines.Ping(ctx, url) },
		})
	}
	return out
}

// remoteBackends keeps one backend per added machine and address.
type remoteBackends struct {
	settings func() config.Config
	mu       sync.Mutex
	byKey    map[string]*backend.ComfyMiniMax
}

func (r *remoteBackends) backend(name, url string) *backend.ComfyMiniMax {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := name + "\x00" + url
	b, ok := r.byKey[key]
	if !ok {
		b = machines.Backend(name, url, r.settings)
		r.byKey[key] = b
	}
	return b
}

// unloadWriter asks Ollama to drop the writer model before a render, when
// the config says to. It is best effort: an Ollama that is not running, or
// one that does not answer, changes nothing.
func unloadWriter(ctx context.Context, o config.Ollama) {
	if !o.UnloadBeforeRender || o.Model == "" {
		return
	}
	_ = ollama.NewClient(o.URL).Unload(ctx, o.Model)
}
