package tui

import (
	"context"
	"testing"
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
	if first := b.wait(t.Context())().(bridgeMsg).msg; first != durableTestMsg(5) {
		t.Fatalf("full of durable messages: first = %v, want the oldest dropped", first)
	}
}

func TestBridgeWaitEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if msg := newBridge(1).wait(ctx)(); msg != nil {
		t.Fatalf("got %v", msg)
	}
}
