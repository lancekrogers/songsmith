// Package comfy talks to a local ComfyUI server over its HTTP and websocket APIs.
package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Node is one entry of a ComfyUI API-format prompt graph. Inputs hold literal
// values or links of the form []any{"<node id>", <output index>}.
type Node struct {
	ClassType string         `json:"class_type"`
	Inputs    map[string]any `json:"inputs"`
}

type Graph map[string]Node

func Link(nodeID string, output int) []any { return []any{nodeID, output} }

type Client struct {
	base string
	http *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{base: baseURL, http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) BaseURL() string { return c.base }

type Device struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	VRAMTotal int64  `json:"vram_total"`
}

type SystemStats struct {
	System struct {
		ComfyUIVersion string   `json:"comfyui_version"`
		PythonVersion  string   `json:"python_version"`
		PyTorchVersion string   `json:"pytorch_version"`
		Argv           []string `json:"argv"`
	} `json:"system"`
	Devices []Device `json:"devices"`
}

func (c *Client) SystemStats(ctx context.Context) (SystemStats, error) {
	var s SystemStats
	err := c.getJSON(ctx, "/system_stats", &s)
	return s, err
}

// NodeInfo is the subset of /object_info that songsmith checks.
type NodeInfo struct {
	Input struct {
		Required map[string]json.RawMessage `json:"required"`
		Optional map[string]json.RawMessage `json:"optional"`
	} `json:"input"`
}

func (n NodeInfo) HasInput(name string) bool {
	_, req := n.Input.Required[name]
	_, opt := n.Input.Optional[name]
	return req || opt
}

// ObjectInfo returns the definition of one node class, or ok=false when the
// server does not know the class.
func (c *Client) ObjectInfo(ctx context.Context, class string) (NodeInfo, bool, error) {
	var resp map[string]NodeInfo
	if err := c.getJSON(ctx, "/object_info/"+url.PathEscape(class), &resp); err != nil {
		return NodeInfo{}, false, err
	}
	info, ok := resp[class]
	return info, ok, nil
}

// ComboOptions returns the allowed values for a combo input, such as the
// model files a loader node can see.
func (n NodeInfo) ComboOptions(name string) []string {
	raw, ok := n.Input.Required[name]
	if !ok {
		raw, ok = n.Input.Optional[name]
	}
	if !ok {
		return nil
	}
	var spec []json.RawMessage
	if json.Unmarshal(raw, &spec) != nil || len(spec) == 0 {
		return nil
	}
	var opts []string
	if json.Unmarshal(spec[0], &opts) == nil {
		return opts
	}
	return nil
}

// PromptError is returned when ComfyUI rejects a graph.
type PromptError struct {
	Status int
	Body   string
}

func (e *PromptError) Error() string {
	return fmt.Sprintf("comfyui rejected prompt (HTTP %d): %s", e.Status, e.Body)
}

func (c *Client) QueuePrompt(ctx context.Context, g Graph, clientID string) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": g, "client_id": clientID})
	if err != nil {
		return "", fmt.Errorf("encode prompt: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/prompt", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("queue prompt: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read prompt response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &PromptError{Status: resp.StatusCode, Body: string(data)}
	}
	var out struct {
		PromptID   string          `json:"prompt_id"`
		NodeErrors json.RawMessage `json:"node_errors"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decode prompt response: %w", err)
	}
	if len(out.NodeErrors) > 0 && string(out.NodeErrors) != "{}" && string(out.NodeErrors) != "null" {
		return "", &PromptError{Status: resp.StatusCode, Body: string(out.NodeErrors)}
	}
	if out.PromptID == "" {
		return "", fmt.Errorf("comfyui returned no prompt id: %s", data)
	}
	return out.PromptID, nil
}

type OutputFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

type HistoryEntry struct {
	Status struct {
		StatusStr string            `json:"status_str"`
		Completed bool              `json:"completed"`
		Messages  []json.RawMessage `json:"messages"`
	} `json:"status"`
	Outputs map[string]struct {
		Audio []OutputFile `json:"audio"`
	} `json:"outputs"`
}

// AudioFiles lists every audio output in the entry.
func (h HistoryEntry) AudioFiles() []OutputFile {
	var files []OutputFile
	for _, o := range h.Outputs {
		files = append(files, o.Audio...)
	}
	return files
}

// History returns the finished entry for promptID, or ok=false while it is
// still queued or running.
func (c *Client) History(ctx context.Context, promptID string) (HistoryEntry, bool, error) {
	var resp map[string]HistoryEntry
	if err := c.getJSON(ctx, "/history/"+url.PathEscape(promptID), &resp); err != nil {
		return HistoryEntry{}, false, err
	}
	h, ok := resp[promptID]
	return h, ok, nil
}

// Download streams an output file into w.
func (c *Client) Download(ctx context.Context, f OutputFile, w io.Writer) error {
	q := url.Values{"filename": {f.Filename}, "subfolder": {f.Subfolder}, "type": {f.Type}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/view?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", f.Filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", f.Filename, resp.StatusCode)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", f.Filename, err)
	}
	return nil
}

// Interrupt stops the prompt that is currently executing.
func (c *Client) Interrupt(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/interrupt", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("interrupt: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("interrupt: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, bytes.TrimSpace(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
