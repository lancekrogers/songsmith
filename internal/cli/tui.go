package cli

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/player"
	"github.com/lancekrogers/songsmith/internal/queue"
	"github.com/lancekrogers/songsmith/internal/tui"
)

const waitingNote = "another songsmith is rendering; queued songs render when it finishes"

func (a *app) runTUI(ctx context.Context) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	live := config.NewLive(a.configPath, cfg)
	r := newRenderer(live)
	var note atomic.Value
	note.Store("")
	deps := tui.Deps{
		Store:      r.store,
		Queue:      r.queue,
		Drafter:    liveDrafter{live},
		Player:     player.New(),
		Config:     live,
		Checks:     health.Run,
		WorkerNote: func() string { return note.Load().(string) },
		Server:     tui.NewComfyServer(r.server),
	}

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { runWorker(ctx, r.store, r.queue, func(s string) { note.Store(s) }) })
	err = tui.Run(ctx, deps)
	cancel()
	wg.Wait()
	return err
}

// runWorker renders the queue while holding the library lock. If another
// process holds it, it retries until that process lets go.
func runWorker(ctx context.Context, store *library.Store, q *queue.Queue, setNote func(string)) {
	for {
		lock, err := store.TryLock()
		switch {
		case err == nil:
			setNote("")
			_ = q.Run(ctx)
			lock.Release()
			return
		case errors.Is(err, library.ErrLocked):
			setNote(waitingNote)
		default:
			setNote("cannot render: " + err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(queue.PollInterval):
		}
	}
}
