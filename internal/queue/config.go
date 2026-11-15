package queue

import (
	"context"
	"errors"
	"sync"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
)

// ConfigMachines lists the machines get's config sets up: this computer,
// rendering with local, then each added machine in config order. get is
// read on every call, so a machine added or removed while songsmith runs is
// used from the next song sent.
func ConfigMachines(get func() config.Config, local backend.Backend) func() []Machine {
	remotes := &remoteBackends{settings: get, byKey: map[string]*backend.ComfyMiniMax{}}
	return func() []Machine {
		cfg := get()
		out := []Machine{{Name: config.ThisComputer, Backend: local, Enabled: cfg.Local.Enabled, Local: true}}
		for _, mc := range cfg.Machines {
			url, models := mc.URL, mc.Models(cfg.ComfyUI)
			out = append(out, Machine{
				Name:    mc.Name,
				Backend: remotes.backend(mc.Name, url),
				Enabled: mc.Enabled,
				Ping:    func(ctx context.Context) error { return machines.Ping(ctx, url) },
				Ready: func(ctx context.Context) error {
					if st := machines.Check(ctx, url, models); st.State != machines.Ready {
						return errors.New(st.Detail)
					}
					return nil
				},
			})
		}
		return out
	}
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
