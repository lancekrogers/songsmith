package tui

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

const bridgeCapacity = 64

// bridgeMsg carries one message from background work into Update, which
// handles the inner message and re-arms the bridge.
type bridgeMsg struct{ msg tea.Msg }

// droppable marks progress-style messages that a later one supersedes; they
// are dropped first when the bridge is full.
type droppable interface{ droppable() }

// bridge lets background work report into the UI. Senders never block: work
// such as a download must not stall on a slow redraw.
type bridge struct {
	ch chan tea.Msg
	// mu serializes the rebuild of a full queue between concurrent senders.
	mu sync.Mutex
}

func newBridge(capacity int) *bridge {
	return &bridge{ch: make(chan tea.Msg, max(1, capacity))}
}

func (b *bridge) send(msg tea.Msg) {
	select {
	case b.ch <- msg:
		return
	default:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case b.ch <- msg:
		return
	default:
	}
	q := make([]tea.Msg, 0, cap(b.ch)+1)
drain:
	for {
		select {
		case m := <-b.ch:
			q = append(q, m)
		default:
			break drain
		}
	}
	q = fitQueue(append(q, msg), cap(b.ch))
	for _, m := range q {
		select {
		case b.ch <- m:
		default:
		}
	}
}

// fitQueue trims q to capacity, oldest droppable message first, then oldest.
func fitQueue(q []tea.Msg, capacity int) []tea.Msg {
	for len(q) > capacity {
		drop := 0
		for i, m := range q {
			if _, ok := m.(droppable); ok {
				drop = i
				break
			}
		}
		q = append(q[:drop], q[drop+1:]...)
	}
	return q
}

// wait delivers the next message; Update calls it again after each one.
func (b *bridge) wait(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-b.ch:
			return bridgeMsg{msg}
		case <-ctx.Done():
			return nil
		}
	}
}

// run starts job off the UI goroutine; job reports through send.
func (b *bridge) run(job func(send func(tea.Msg))) tea.Cmd {
	return func() tea.Msg {
		job(b.send)
		return nil
	}
}
