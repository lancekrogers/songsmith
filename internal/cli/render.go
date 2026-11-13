package cli

import (
	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/queue"
)

type renderer struct {
	store   *library.Store
	server  *server.Manager
	backend backend.Backend
	queue   *queue.Queue
}

// newRenderer builds the render stack. The folders, host and port come from
// live once; the backend reads the rest of live at the start of each render.
func newRenderer(live *config.Live) *renderer {
	cfg := live.Get()
	m := server.New(cfg.ComfyUI, cfg.StateDir)
	b := backend.NewComfyMiniMax(m.Client(), backend.StarterFunc(m.Ensure), live.Get)
	store := library.NewStore(cfg.LibraryDir)
	return &renderer{store: store, server: m, backend: b, queue: queue.New(store, b)}
}
