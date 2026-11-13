package cli

import (
	"context"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
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
// live once; the backend reads the rest of live at the start of each render.
func newRenderer(live *config.Live) *renderer {
	cfg := live.Get()
	m := server.New(cfg.ComfyUI, cfg.StateDir)
	b := backend.NewComfyMiniMax(m.Client(), backend.StarterFunc(m.Ensure), live.Get)
	store := library.NewStore(cfg.LibraryDir)
	q := queue.New(store, b)
	q.BeforeRender(func(ctx context.Context) { unloadWriter(ctx, live.Get().Ollama) })
	run := master.NewRunner()
	q.AfterRender(autoMaster(store, live.Get, run))
	return &renderer{store: store, server: m, backend: b, queue: q, master: run}
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
