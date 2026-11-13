package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// fakeServices answers as a running ComfyUI and an Ollama, recording the
// render graph and the chat requests; the render is rejected, so nothing runs.
type fakeServices struct {
	mu     sync.Mutex
	graphs []comfy.Graph
	chats  []ollama.ChatRequest
}

func (f *fakeServices) start(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /system_stats", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"system":{"argv":["main.py","--gpu-only"]},"devices":[{"type":"mps"}]}`))
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt comfy.Graph `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.graphs = append(f.graphs, body.Prompt)
		f.mu.Unlock()
		http.Error(w, "recorded", http.StatusBadRequest)
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req ollama.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.chats = append(f.chats, req)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{}"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The TUI's drafter and renderer read the running settings at the start of
// each draft and render, so a saved change applies without a restart.
func TestDraftsAndRendersUseTheSettingsCurrentWhenTheyStart(t *testing.T) {
	f := &fakeServices{}
	srv := f.start(t)
	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	portNum, _ := strconv.Atoi(port)
	for _, kv := range []struct {
		key   string
		value any
	}{
		{"library_dir", filepath.Join(dir, "library")}, {"state_dir", filepath.Join(dir, "state")},
		{"comfyui.dir", filepath.Join(dir, "ComfyUI")}, {"comfyui.host", host}, {"comfyui.port", portNum},
		{"ollama.url", srv.URL}, {"ollama.model", "first:1b"},
	} {
		if err := config.Set(path, kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(path, cfg)

	r := newRenderer(live)
	drafter := liveDrafter{live}
	draftAndRender := func() {
		_, _ = drafter.Write(t.Context(), "an idea", writer.Options{})
		_, err := r.backend.Generate(t.Context(), backend.Request{ID: "s", Lyrics: "[Verse]\nla", Caption: "pop",
			MaxSeconds: 30, OutPath: filepath.Join(dir, "s.mp3")}, nil)
		if err == nil {
			t.Fatal("the fake accepted a render")
		}
	}
	draftAndRender()
	for _, kv := range []struct {
		key   string
		value any
	}{{"render.steps", 42}, {"render.sampler", "dpmpp_2m"}, {"ollama.model", "second:2b"}, {"ollama.temperature", 0.3}} {
		if err := live.Set(kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	draftAndRender()

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.graphs) != 2 || len(f.chats) != 2 {
		t.Fatalf("%d renders, %d drafts", len(f.graphs), len(f.chats))
	}
	before, after := f.graphs[0][comfy.NodeSampler].Inputs, f.graphs[1][comfy.NodeSampler].Inputs
	if before["steps"] != float64(30) || after["steps"] != float64(42) || after["sampler_name"] != "dpmpp_2m" {
		t.Fatalf("sampler inputs before %v, after %v", before, after)
	}
	if f.chats[0].Model != "first:1b" || f.chats[1].Model != "second:2b" || f.chats[1].Options["temperature"] != 0.3 {
		t.Fatalf("draft requests %+v", f.chats)
	}
}
