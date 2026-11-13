package comfy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type EventType string

const (
	EventStart       EventType = "execution_start"
	EventExecuting   EventType = "executing"
	EventProgress    EventType = "progress"
	EventSuccess     EventType = "execution_success"
	EventError       EventType = "execution_error"
	EventInterrupted EventType = "execution_interrupted"
)

// Event is a websocket message for one prompt.
type Event struct {
	Type     EventType
	PromptID string
	Node     string
	Value    int
	Max      int
	Message  string
}

type wsMessage struct {
	Type string `json:"type"`
	Data struct {
		PromptID         string `json:"prompt_id"`
		Node             string `json:"node"`
		NodeID           string `json:"node_id"`
		Value            int    `json:"value"`
		Max              int    `json:"max"`
		ExceptionType    string `json:"exception_type"`
		ExceptionMessage string `json:"exception_message"`
	} `json:"data"`
}

func parseEvent(data []byte) (Event, bool) {
	var m wsMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return Event{}, false
	}
	t := EventType(m.Type)
	switch t {
	case EventStart, EventExecuting, EventProgress, EventSuccess, EventError, EventInterrupted:
	default:
		return Event{}, false
	}
	ev := Event{Type: t, PromptID: m.Data.PromptID, Node: m.Data.Node, Value: m.Data.Value, Max: m.Data.Max}
	if t == EventError {
		ev.Node = m.Data.NodeID
		ev.Message = strings.TrimSpace(m.Data.ExceptionType + ": " + m.Data.ExceptionMessage)
	}
	return ev, true
}

func newClientID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (c *Client) dial(ctx context.Context, clientID string) (*websocket.Conn, error) {
	wsURL := "ws" + strings.TrimPrefix(c.base, "http") + "/ws?clientId=" + clientID
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: &http.Client{}})
	if err != nil {
		return nil, fmt.Errorf("connect websocket: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	return conn, nil
}

// ErrInterrupted reports that ComfyUI stopped the prompt before it finished.
var ErrInterrupted = errors.New("render interrupted")

// ExecutionError carries ComfyUI's failure message for a prompt.
type ExecutionError struct {
	Node    string
	Message string
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("comfyui failed at node %s: %s", e.Node, e.Message)
}

// Run queues g, reports its events to onEvent, and returns the finished
// history entry. Cancelling ctx interrupts the running prompt.
func (c *Client) Run(ctx context.Context, g Graph, onEvent func(Event)) (HistoryEntry, error) {
	clientID := newClientID()
	conn, err := c.dial(ctx, clientID)
	if err != nil {
		return HistoryEntry{}, err
	}
	defer conn.CloseNow()

	promptID, err := c.QueuePrompt(ctx, g, clientID)
	if err != nil {
		return HistoryEntry{}, err
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return HistoryEntry{}, c.interruptFor(ctx)
			}
			return c.waitHistory(ctx, promptID)
		}
		if typ != websocket.MessageText {
			continue
		}
		ev, ok := parseEvent(data)
		if !ok || ev.PromptID != promptID {
			continue
		}
		if onEvent != nil {
			onEvent(ev)
		}
		switch ev.Type {
		case EventSuccess:
			return c.waitHistory(ctx, promptID)
		case EventError:
			return HistoryEntry{}, &ExecutionError{Node: ev.Node, Message: ev.Message}
		case EventInterrupted:
			return HistoryEntry{}, ErrInterrupted
		}
	}
}

// waitHistory polls until the prompt appears in history. It also covers a
// dropped websocket mid-render.
func (c *Client) waitHistory(ctx context.Context, promptID string) (HistoryEntry, error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		h, ok, err := c.History(ctx, promptID)
		if err != nil && ctx.Err() == nil {
			return HistoryEntry{}, err
		}
		if ok {
			if h.Status.StatusStr == "error" {
				return h, &ExecutionError{Node: "?", Message: "see ComfyUI log"}
			}
			return h, nil
		}
		select {
		case <-ctx.Done():
			return HistoryEntry{}, c.interruptFor(ctx)
		case <-tick.C:
		}
	}
}

// interruptFor stops the running prompt after ctx was cancelled and returns
// the error Run reports.
func (c *Client) interruptFor(ctx context.Context) error {
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.Interrupt(ictx); err != nil {
		return fmt.Errorf("%w (%w); also failed to stop ComfyUI: %w", ErrInterrupted, context.Cause(ctx), err)
	}
	return fmt.Errorf("%w: %w", ErrInterrupted, context.Cause(ctx))
}
