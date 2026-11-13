package cli

import (
	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/queue"
)

type renderer struct {
	cfg     config.Config
	store   *library.Store
	server  *server.Manager
	backend backend.Backend
	queue   *queue.Queue
}

func newRenderer(cfg config.Config) *renderer {
	m := server.New(cfg.ComfyUI, cfg.StateDir)
	b := backend.NewComfyMiniMax(m.Client(), backend.StarterFunc(m.Ensure), cfg.ComfyUI, cfg.Render)
	store := library.NewStore(cfg.LibraryDir)
	return &renderer{cfg: cfg, store: store, server: m, backend: b, queue: queue.New(store, b)}
}
