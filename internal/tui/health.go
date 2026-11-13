package tui

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/setup"
)

// healthTimeout bounds one run of the checks; the server and Ollama probes
// are network calls.
const healthTimeout = 30 * time.Second

type healthState struct {
	// seq identifies the newest run; results from older runs are dropped.
	seq     int
	running bool
	// done is set once any run has finished.
	done   bool
	checks []health.Check
	// plan is what songsmith setup would install, read with the checks;
	// planned is false until a run with an Installer has finished.
	plan    setup.Plan
	planned bool
}

type healthDoneMsg struct {
	seq     int
	checks  []health.Check
	plan    setup.Plan
	planned bool
}

// startHealth runs the checks in the background against the current settings.
func (a app) startHealth() (app, tea.Cmd) {
	a.health.seq++
	a.health.running = true
	seq, run, cfg, ctx, in := a.health.seq, a.d.Checks, a.cfg(), a.ctx, a.d.Installer
	return a, a.events.run(func(send func(tea.Msg)) {
		ctx, cancel := context.WithTimeout(ctx, healthTimeout)
		defer cancel()
		msg := healthDoneMsg{seq: seq}
		var wg sync.WaitGroup
		if in != nil {
			wg.Go(func() { msg.plan, msg.planned = in.Plan(ctx, cfg), true })
		}
		msg.checks = run(ctx, cfg)
		wg.Wait()
		send(msg)
	})
}
