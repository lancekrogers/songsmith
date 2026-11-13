package tui

import (
	"context"
	"sync"
	"testing"
	"time"
)

type progressTestMsg int

func (progressTestMsg) droppable() {}

type durableTestMsg int

func TestBridgeDropsProgressBeforeDurableMessages(t *testing.T) {
	b := newBridge(3)
	b.send(durableTestMsg(1))
	b.send(progressTestMsg(1))
	b.send(durableTestMsg(2))
	b.send(durableTestMsg(3))
	var got []any
	for range 3 {
		got = append(got, b.wait(t.Context())().(bridgeMsg).msg)
	}
	want := []any{durableTestMsg(1), durableTestMsg(2), durableTestMsg(3)}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	b.send(durableTestMsg(4))
	b.send(durableTestMsg(5))
	b.send(durableTestMsg(6))
	b.send(durableTestMsg(7))
	b.send(progressTestMsg(2))
	for want := durableTestMsg(4); want <= 7; want++ {
		if got := b.wait(t.Context())().(bridgeMsg).msg; got != want {
			t.Fatalf("full of durable messages: got %v, want %v", got, want)
		}
	}
}

type senderTestMsg struct{ sender, seq int }

func TestBridgeKeepsEachSendersDurableMessagesInOrder(t *testing.T) {
	const senders, perSender = 8, 500
	b := newBridge(4)
	var wg sync.WaitGroup
	for s := range senders {
		wg.Go(func() {
			for i := range perSender {
				b.send(progressTestMsg(i))
				b.send(senderTestMsg{s, i})
			}
		})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	next := make([]int, senders)
	for got := 0; got < senders*perSender; {
		msg := b.wait(ctx)()
		if msg == nil {
			t.Fatalf("timed out after %d durable messages; next per sender %v", got, next)
		}
		m, ok := msg.(bridgeMsg).msg.(senderTestMsg)
		if !ok {
			continue
		}
		if m.seq != next[m.sender] {
			t.Fatalf("sender %d: got message %d, want %d", m.sender, m.seq, next[m.sender])
		}
		next[m.sender]++
		got++
	}
	wg.Wait()
}

func TestBridgeSendNeverBlocks(t *testing.T) {
	b := newBridge(1)
	done := make(chan struct{})
	go func() {
		for i := range 100 {
			b.send(durableTestMsg(i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send blocked with nobody receiving")
	}
}

func TestBridgeWaitEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if msg := newBridge(1).wait(ctx)(); msg != nil {
		t.Fatalf("got %v", msg)
	}
}
