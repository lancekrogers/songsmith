package cli

import (
	"context"
	"errors"
	"sync"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/player"
	"github.com/lancekrogers/songsmith/internal/tui"
	"github.com/lancekrogers/songsmith/internal/writer"
)

func (a *app) runTUI(ctx context.Context) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	r := newRenderer(cfg)
	deps := tui.Deps{
		Store:      r.store,
		Queue:      r.queue,
		Drafter:    writer.New(ollama.NewClient(cfg.Ollama.URL), cfg.Ollama.Model),
		Player:     player.New(),
		DraftModel: cfg.Ollama.Model,
	}

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	lock, err := r.store.TryLock()
	switch {
	case errors.Is(err, library.ErrLocked):
		deps.WorkerNote = "another songsmith is rendering; songs queued here render there"
	case err != nil:
		cancel()
		return err
	default:
		defer lock.Release()
		wg.Go(func() { _ = r.queue.Run(ctx) })
	}
	err = tui.Run(ctx, deps)
	cancel()
	wg.Wait()
	return err
}
