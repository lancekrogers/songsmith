package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// promptRecorder is a ComfyUI that records the graph it is sent and rejects
// it, so nothing renders.
type promptRecorder struct {
	mu    sync.Mutex
	graph comfy.Graph
}

func (p *promptRecorder) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
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
		p.mu.Lock()
		p.graph = body.Prompt
		p.mu.Unlock()
		http.Error(w, "recorded", http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type recordingChat struct {
	mu   sync.Mutex
	reqs []ollama.ChatRequest
}

func (c *recordingChat) Chat(_ context.Context, req ollama.ChatRequest) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	return "{}", nil
}

// liveWriter drafts with the settings current when each draft starts, as the
// app's drafter does.
type liveWriter struct {
	live *config.Live
	chat *recordingChat
}

func (d liveWriter) Write(ctx context.Context, idea string, opts writer.Options) (writer.Draft, error) {
	cfg := d.live.Get()
	return writer.New(d.chat, cfg.Ollama.Model).WithTemperature(cfg.Ollama.Temperature).Write(ctx, idea, opts)
}

func (d liveWriter) Revise(ctx context.Context, current writer.Draft, idea, instruction string, opts writer.Options) (writer.Draft, error) {
	cfg := d.live.Get()
	return writer.New(d.chat, cfg.Ollama.Model).Revise(ctx, current, idea, instruction, opts)
}

func TestSettingsChangesReachTheNextRenderAndDraft(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	rec := &promptRecorder{}
	comfyURL := rec.server(t).URL
	render := backend.NewComfyMiniMax(comfy.NewClient(comfyURL), nil, a.d.Config.Get)
	chat := &recordingChat{}
	a.d.Drafter = liveWriter{a.d.Config, chat}

	a = openSettingsOn(t, a, sectionRender)
	a = focus(t, a, "Steps", -1)
	a = pressSettings(t, a, "+", "+")
	a = pressSettings(t, a, "2")
	a = focus(t, a, "Model", 2)
	a = pressSettings(t, a, "enter")
	if a.cfg().Render.Steps != 32 || a.cfg().Ollama.Model != "zeta:3b" {
		t.Fatalf("running config: steps %d, model %q", a.cfg().Render.Steps, a.cfg().Ollama.Model)
	}

	_, err := render.Generate(t.Context(), backend.Request{ID: "song", Lyrics: "[Verse]\nla", Caption: "pop",
		MaxSeconds: 30, OutPath: filepath.Join(t.TempDir(), "song.mp3")}, nil)
	if err == nil {
		t.Fatal("the recorder accepted a render")
	}
	rec.mu.Lock()
	sampler := rec.graph[comfy.NodeSampler]
	rec.mu.Unlock()
	if got := sampler.Inputs["steps"]; got != float64(32) {
		t.Fatalf("next render's steps = %v, want 32", got)
	}

	_, _ = a.d.Drafter.Write(t.Context(), "a song about socks", writer.Options{})
	if len(chat.reqs) != 1 || chat.reqs[0].Model != "zeta:3b" {
		t.Fatalf("next draft requests %+v", chat.reqs)
	}
}
