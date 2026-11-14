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
		ev.Message = errorMessage(m)
	}
	return ev, true
}

func errorMessage(m wsMessage) string {
	return strings.TrimSpace(m.Data.ExceptionType + ": " + m.Data.ExceptionMessage)
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

// ErrPromptLost means ComfyUI no longer knows a prompt, for example after the
// server restarted mid-render.
var ErrPromptLost = errors.New("comfyui lost the prompt (server restarted?)")

// ErrDetach, used as a context cancellation cause, makes Run return without
// stopping the prompt, so a later Run with ResumeID can pick up its result.
var ErrDetach = errors.New("detached from render")

type RunOptions struct {
	// ResumeID reattaches to a prompt queued earlier, if ComfyUI still has it.
	ResumeID string
	// OnQueued receives the prompt ID once the prompt is queued or reattached.
	OnQueued func(promptID string)
	OnEvent  func(Event)
}

// Run queues g (or reattaches to opts.ResumeID), reports its events and
// returns the finished history entry. Cancelling ctx stops only this prompt.
func (c *Client) Run(ctx context.Context, g Graph, opts RunOptions) (HistoryEntry, error) {
	if opts.ResumeID != "" {
		if h, ok, err := c.History(ctx, opts.ResumeID); err == nil && ok {
			if opts.OnQueued != nil {
				opts.OnQueued(opts.ResumeID)
			}
			return finished(h)
		}
		if known, err := c.QueueContains(ctx, opts.ResumeID); err == nil && known {
			if opts.OnQueued != nil {
				opts.OnQueued(opts.ResumeID)
			}
			// Progress events go to the client that queued the prompt, so a
			// reattached render can only be followed through history.
			return c.waitHistory(ctx, opts.ResumeID)
		}
	}

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
	if opts.OnQueued != nil {
		opts.OnQueued(promptID)
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return HistoryEntry{}, c.stopPrompt(ctx, promptID)
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
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
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

// OutOfMemory reports whether err is ComfyUI running out of memory at node.
// It matches PyTorch's wording for CUDA and for Apple's MPS backend.
func OutOfMemory(err error, node string) bool {
	var ee *ExecutionError
	if !errors.As(err, &ee) || ee.Node != node {
		return false
	}
	msg := strings.ToLower(ee.Message)
	return strings.Contains(msg, "out of memory") || strings.Contains(msg, "outofmemory")
}

func finished(h HistoryEntry) (HistoryEntry, error) {
	if h.Status.StatusStr == "error" {
		return h, historyError(h)
	}
	return h, nil
}

// historyError is the failure a finished prompt recorded, for a prompt
// whose events were not followed.
func historyError(h HistoryEntry) *ExecutionError {
	for _, raw := range h.Status.Messages {
		var msg []json.RawMessage
		var event string
		if json.Unmarshal(raw, &msg) != nil || len(msg) != 2 || json.Unmarshal(msg[0], &event) != nil || event != string(EventError) {
			continue
		}
		var m wsMessage
		if json.Unmarshal(msg[1], &m.Data) == nil {
			return &ExecutionError{Node: m.Data.NodeID, Message: errorMessage(m)}
		}
	}
	return &ExecutionError{Node: "?", Message: "see ComfyUI log"}
}

// waitHistory polls until the prompt appears in history. It also covers a
// dropped websocket mid-render and reattached prompts.
func (c *Client) waitHistory(ctx context.Context, promptID string) (HistoryEntry, error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for n := 1; ; n++ {
		h, ok, err := c.History(ctx, promptID)
		if err != nil && ctx.Err() == nil {
			return HistoryEntry{}, err
		}
		if ok {
			return finished(h)
		}
		if n%5 == 0 {
			if known, err := c.QueueContains(ctx, promptID); err == nil && !known {
				if h, ok, err := c.History(ctx, promptID); err == nil && ok {
					return finished(h)
				}
				return HistoryEntry{}, ErrPromptLost
			}
		}
		select {
		case <-ctx.Done():
			return HistoryEntry{}, c.stopPrompt(ctx, promptID)
		case <-tick.C:
		}
	}
}

// stopPrompt interrupts promptID if it is running and drops it from the queue
// if it is still pending, then returns the error Run reports.
func (c *Client) stopPrompt(ctx context.Context, promptID string) error {
	if errors.Is(context.Cause(ctx), ErrDetach) {
		return context.Cause(ctx)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := errors.Join(c.Interrupt(sctx, promptID), c.DeleteQueued(sctx, promptID))
	if err != nil {
		return fmt.Errorf("%w (%w); also failed to stop ComfyUI: %w", ErrInterrupted, context.Cause(ctx), err)
	}
	return fmt.Errorf("%w: %w", ErrInterrupted, context.Cause(ctx))
}
