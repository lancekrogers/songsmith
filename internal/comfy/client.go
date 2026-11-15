// Package comfy talks to a local ComfyUI server over its HTTP and websocket APIs.
package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
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
		RAMTotal       int64    `json:"ram_total"`
	} `json:"system"`
	Devices []Device `json:"devices"`
}

// Memory is what the server renders in, in bytes: the GPU's own memory on a
// device that has it, otherwise system memory, which Apple Silicon shares
// with its GPU. It is 0 when the server does not say.
func (s SystemStats) Memory() int64 {
	if len(s.Devices) > 0 {
		if d := s.Devices[0]; d.Type != "mps" && d.Type != "cpu" && d.VRAMTotal > 0 {
			return d.VRAMTotal
		}
	}
	return s.System.RAMTotal
}

func (c *Client) SystemStats(ctx context.Context) (SystemStats, error) {
	var s SystemStats
	err := c.getJSON(ctx, "/system_stats", &s)
	return s, err
}

// Free asks the server to unload its models and, with freeMemory, to drop
// its cached node outputs, which is what releases a model it keeps on the
// GPU. The server applies it once the prompt running now finishes, or at
// once when it is idle.
func (c *Client) Free(ctx context.Context, unloadModels, freeMemory bool) error {
	return c.postJSON(ctx, "/free", map[string]any{"unload_models": unloadModels, "free_memory": freeMemory})
}

// OutputDir is the folder a server on this machine saves outputs to, or ""
// when it cannot tell.
func (c *Client) OutputDir(ctx context.Context) (string, error) {
	stats, err := c.SystemStats(ctx)
	if err != nil {
		return "", err
	}
	argv := stats.System.Argv
	for i, arg := range argv {
		dir, ok := strings.CutPrefix(arg, "--output-directory=")
		if arg == "--output-directory" && i+1 < len(argv) {
			dir, ok = argv[i+1], true
		}
		if ok {
			if !filepath.IsAbs(dir) {
				return "", nil
			}
			return filepath.Clean(dir), nil
		}
	}
	paths, err := c.FolderPaths(ctx)
	if err != nil {
		return "", err
	}
	nodes := paths["custom_nodes"]
	if len(nodes) == 0 || !filepath.IsAbs(nodes[0]) {
		return "", nil
	}
	return filepath.Join(filepath.Dir(filepath.Clean(nodes[0])), "output"), nil
}

// Local reports whether the server runs on this machine.
func (c *Client) Local() bool {
	u, err := url.Parse(c.base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// FolderPaths returns the folders the server reads each kind of file from,
// such as "custom_nodes" or "vae". ComfyUI serves it for its own frontend.
func (c *Client) FolderPaths(ctx context.Context) (map[string][]string, error) {
	var p map[string][]string
	err := c.getJSON(ctx, "/internal/folder_paths", &p)
	return p, err
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
// model files a loader node can see, or nil when it cannot read them.
func (n NodeInfo) ComboOptions(name string) []string {
	opts, _ := n.ComboList(name)
	return opts
}

// ComboList returns the allowed values for a combo input, and false when
// the input is missing, is not a combo, or does not list its values. It
// reads both the [[values], {...}] shape and the newer
// ["COMBO", {"options": [values]}].
func (n NodeInfo) ComboList(name string) ([]string, bool) {
	raw, ok := n.Input.Required[name]
	if !ok {
		raw, ok = n.Input.Optional[name]
	}
	if !ok {
		return nil, false
	}
	var spec []json.RawMessage
	if json.Unmarshal(raw, &spec) != nil || len(spec) == 0 {
		return nil, false
	}
	var opts []string
	if json.Unmarshal(spec[0], &opts) == nil && opts != nil {
		return opts, true
	}
	var typ string
	if json.Unmarshal(spec[0], &typ) != nil || typ != "COMBO" || len(spec) < 2 {
		return nil, false
	}
	var extra struct {
		Options []string `json:"options"`
	}
	if json.Unmarshal(spec[1], &extra) != nil || extra.Options == nil {
		return nil, false
	}
	return extra.Options, true
}

// ErrUnreachable means the server did not answer at all: it is stopped,
// asleep, or the network to it is down.
var ErrUnreachable = errors.New("comfyui server did not answer")

// unreachableError keeps err's message and also matches ErrUnreachable.
type unreachableError struct{ err error }

func (e *unreachableError) Error() string   { return e.err.Error() }
func (e *unreachableError) Unwrap() []error { return []error{e.err, ErrUnreachable} }

// unreachable marks a request that got no answer from the server, unless
// it failed because ctx ended.
func unreachable(ctx context.Context, err error) error {
	var ue *url.Error
	if ctx.Err() != nil || !errors.As(err, &ue) {
		return err
	}
	return &unreachableError{err: err}
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
		return "", fmt.Errorf("queue prompt: %w", unreachable(ctx, err))
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
		Audio   []OutputFile `json:"audio"`
		Latents []OutputFile `json:"latents"`
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

// LatentFiles lists every latent a SaveLatent node wrote.
func (h HistoryEntry) LatentFiles() []OutputFile {
	var files []OutputFile
	for _, o := range h.Outputs {
		files = append(files, o.Latents...)
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
		return fmt.Errorf("download %s: %w", f.Filename, unreachable(ctx, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", f.Filename, resp.StatusCode)
	}
	body := &bodyReader{r: resp.Body}
	if _, err := io.Copy(w, body); err != nil {
		return fmt.Errorf("download %s: %w", f.Filename, body.cut(ctx, err))
	}
	return nil
}

// bodyReader records the error reading a response body, so a connection
// that drops midway can be told apart from a file that cannot be written.
type bodyReader struct {
	r   io.Reader
	err error
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		b.err = err
	}
	return n, err
}

// cut marks err as unreachable when the body stopped arriving, unless ctx
// ended.
func (b *bodyReader) cut(ctx context.Context, err error) error {
	if b.err != nil && ctx.Err() == nil {
		return &unreachableError{err: err}
	}
	return err
}

// Interrupt stops promptID if it is the prompt currently executing.
func (c *Client) Interrupt(ctx context.Context, promptID string) error {
	return c.postJSON(ctx, "/interrupt", map[string]any{"prompt_id": promptID})
}

// DeleteQueued removes pending prompts from the queue.
func (c *Client) DeleteQueued(ctx context.Context, promptIDs ...string) error {
	return c.postJSON(ctx, "/queue", map[string]any{"delete": promptIDs})
}

// QueueContains reports whether promptID is running or pending.
func (c *Client) QueueContains(ctx context.Context, promptID string) (bool, error) {
	var q struct {
		Running [][]json.RawMessage `json:"queue_running"`
		Pending [][]json.RawMessage `json:"queue_pending"`
	}
	if err := c.getJSON(ctx, "/queue", &q); err != nil {
		return false, err
	}
	for _, item := range append(q.Running, q.Pending...) {
		var id string
		if len(item) > 1 && json.Unmarshal(item[1], &id) == nil && id == promptID {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) postJSON(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, unreachable(ctx, err))
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: HTTP %d", path, resp.StatusCode)
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
		return fmt.Errorf("GET %s: %w", path, unreachable(ctx, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, bytes.TrimSpace(body))
	}
	body := &bodyReader{r: resp.Body}
	if err := json.NewDecoder(body).Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, body.cut(ctx, err))
	}
	return nil
}
