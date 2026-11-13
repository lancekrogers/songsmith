package tui

import (
	"context"
	"slices"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

const bridgeCapacity = 64

// bridgeMsg carries one message from background work into Update, which
// handles the inner message and re-arms the bridge.
type bridgeMsg struct{ msg tea.Msg }

// droppable marks progress-style messages that a later one supersedes; only
// these are dropped when the bridge is full.
type droppable interface{ droppable() }

// bridge lets background work report into the UI. Senders never block: work
// such as a download must not stall on a slow redraw, and nothing may wait on
// a program that has stopped. Every other message is kept, in the order sent.
type bridge struct {
	capacity int
	// ready holds a signal while msgs may be non-empty.
	ready chan struct{}

	mu   sync.Mutex
	msgs []tea.Msg
}

func newBridge(capacity int) *bridge {
	return &bridge{capacity: max(1, capacity), ready: make(chan struct{}, 1)}
}

func (b *bridge) send(msg tea.Msg) {
	b.mu.Lock()
	b.msgs = fitQueue(append(b.msgs, msg), b.capacity)
	b.mu.Unlock()
	b.signal()
}

func (b *bridge) signal() {
	select {
	case b.ready <- struct{}{}:
	default:
	}
}

func (b *bridge) next() (tea.Msg, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) == 0 {
		return nil, false
	}
	msg := b.msgs[0]
	b.msgs[0] = nil
	b.msgs = b.msgs[1:]
	if len(b.msgs) > 0 {
		b.signal()
	}
	return msg, true
}

// fitQueue drops the oldest droppable messages while q is over capacity. A
// queue of messages that must be kept may grow past it.
func fitQueue(q []tea.Msg, capacity int) []tea.Msg {
	for len(q) > capacity {
		drop := slices.IndexFunc(q, func(m tea.Msg) bool {
			_, ok := m.(droppable)
			return ok
		})
		if drop < 0 {
			break
		}
		q = slices.Delete(q, drop, drop+1)
	}
	return q
}

// wait delivers the next message; Update calls it again after each one.
func (b *bridge) wait(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		for {
			if msg, ok := b.next(); ok {
				return bridgeMsg{msg}
			}
			select {
			case <-b.ready:
			case <-ctx.Done():
				return nil
			}
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
