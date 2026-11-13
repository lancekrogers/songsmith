package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/health"
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
}

type healthDoneMsg struct {
	seq    int
	checks []health.Check
}

// startHealth runs the checks in the background against the current settings.
func (a app) startHealth() (app, tea.Cmd) {
	a.health.seq++
	a.health.running = true
	seq, run, cfg, ctx := a.health.seq, a.d.Checks, a.cfg(), a.ctx
	return a, a.events.run(func(send func(tea.Msg)) {
		ctx, cancel := context.WithTimeout(ctx, healthTimeout)
		defer cancel()
		send(healthDoneMsg{seq: seq, checks: run(ctx, cfg)})
	})
}
