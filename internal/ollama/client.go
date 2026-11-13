// Package ollama is a minimal client for a local Ollama server.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base string
	http *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 10 * time.Minute}}
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	// Format is "json" or a JSON schema that constrains the reply.
	Format  any            `json:"format,omitempty"`
	Think   *bool          `json:"think,omitempty"`
	Options map[string]any `json:"options,omitempty"`
	Stream  bool           `json:"stream"`
	// KeepAlive is how long the model stays loaded after the request: a
	// duration string such as "5m", or seconds. nil leaves it to Ollama.
	KeepAlive any `json:"keep_alive,omitempty"`
}

type chatResponse struct {
	Message Message `json:"message"`
	Error   string  `json:"error"`
}

// Chat sends a non-streaming chat request and returns the reply content.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (string, error) {
	req.Stream = false
	var resp chatResponse
	if err := c.post(ctx, "/api/chat", req, &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("ollama: %s", resp.Error)
	}
	return resp.Message.Content, nil
}

// KeepAlive turns a keep_alive setting into what the API takes: a number of
// seconds for a bare number, the duration string otherwise. Blank, or a value
// Ollama would reject, is nil, which leaves it to Ollama.
func KeepAlive(s string) any {
	s = strings.TrimSpace(s)
	if s == "" || !ValidKeepAlive(s) {
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

// ValidKeepAlive reports whether s is a keep_alive Ollama accepts: a whole
// number of seconds or a Go duration such as "5m" or "-1m".
func ValidKeepAlive(s string) bool {
	s = strings.TrimSpace(s)
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	_, err := time.ParseDuration(s)
	return err == nil
}

// Unload asks Ollama to drop model from memory now: a chat request with no
// messages and keep_alive 0.
func (c *Client) Unload(ctx context.Context, model string) error {
	var resp chatResponse
	if err := c.post(ctx, "/api/chat", map[string]any{"model": model, "messages": []Message{}, "keep_alive": 0, "stream": false}, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("ollama: %s", resp.Error)
	}
	return nil
}

// Models lists the installed model names.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	infos, err := c.ModelInfos(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, m := range infos {
		names = append(names, m.Name)
	}
	return names, nil
}

// ModelInfo is one installed model as /api/tags describes it. Older servers
// leave Capabilities out; nil means unknown.
type ModelInfo struct {
	Name          string
	Family        string
	Families      []string
	ParameterSize string
	Quantization  string
	Capabilities  []string
}

// ModelInfos lists the installed models with what /api/tags says of them.
func (c *Client) ModelInfos(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/tags: HTTP %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Details struct {
				Family            string   `json:"family"`
				Families          []string `json:"families"`
				ParameterSize     string   `json:"parameter_size"`
				QuantizationLevel string   `json:"quantization_level"`
			} `json:"details"`
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("decode ollama tags: %w", err)
	}
	out := make([]ModelInfo, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, ModelInfo{Name: m.Name, Family: m.Details.Family, Families: m.Details.Families,
			ParameterSize: m.Details.ParameterSize, Quantization: m.Details.QuantizationLevel, Capabilities: m.Capabilities})
	}
	return out, nil
}

// Capabilities asks /api/show what model can do, such as "completion" or
// "embedding". A server that does not say returns nil.
func (c *Client) Capabilities(ctx context.Context, model string) ([]string, error) {
	var resp struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := c.post(ctx, "/api/show", map[string]string{"model": model}, &resp); err != nil {
		return nil, err
	}
	return resp.Capabilities, nil
}

// CanChat reports whether m can write text. Capabilities decide when known:
// a model without "completion" cannot. Without them, an embedding family or
// name marks a model that cannot; anything else is assumed to.
func (m ModelInfo) CanChat() bool {
	if m.Capabilities != nil {
		return slices.Contains(m.Capabilities, "completion")
	}
	if strings.Contains(strings.ToLower(m.Name), "embed") {
		return false
	}
	for _, f := range append([]string{m.Family}, m.Families...) {
		f = strings.ToLower(f)
		if strings.Contains(f, "embed") || strings.Contains(f, "bert") {
			return false
		}
	}
	return true
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read ollama response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s: HTTP %d: %s", path, resp.StatusCode, bytes.TrimSpace(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode ollama response: %w", err)
	}
	return nil
}

type pullLine struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// Pull downloads model through /api/pull. progress receives each status
// line with completed and total summed over the layers seen so far, since
// Ollama reports bytes per layer. Pull returns nil only after Ollama reports
// success.
func (c *Client) Pull(ctx context.Context, model string, progress func(status string, completed, total int64)) error {
	data, err := json.Marshal(map[string]any{"model": model, "stream": true})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/pull", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// No overall timeout: a large pull runs far longer than a chat request.
	hc := &http.Client{Transport: c.http.Transport}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("reach ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var line pullLine
		if json.Unmarshal(raw, &line) == nil && line.Error != "" {
			return fmt.Errorf("ollama pull %s: %s", model, line.Error)
		}
		return fmt.Errorf("ollama pull %s: HTTP %d: %s", model, resp.StatusCode, bytes.TrimSpace(raw))
	}
	type layer struct{ done, total int64 }
	layers := map[string]layer{}
	dec := json.NewDecoder(resp.Body)
	for {
		var line pullLine
		if err := dec.Decode(&line); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return fmt.Errorf("ollama pull %s: stream ended before success", model)
			}
			return fmt.Errorf("read ollama pull stream: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("ollama pull %s: %s", model, line.Error)
		}
		if line.Digest != "" && line.Total > 0 {
			layers[line.Digest] = layer{line.Completed, line.Total}
		}
		var done, total int64
		for _, l := range layers {
			done += l.done
			total += l.total
		}
		if progress != nil {
			progress(line.Status, done, total)
		}
		if line.Status == "success" {
			return nil
		}
	}
}
