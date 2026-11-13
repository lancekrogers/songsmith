package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/writer"
)

func TestUnloadWriterAsksOllamaOnlyWhenOn(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			hits.Add(1)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":""},"done":true}`))
	}))
	defer srv.Close()
	o := config.Default().Ollama
	o.URL, o.Model = srv.URL, "writer:1b"
	unloadWriter(t.Context(), o)
	if hits.Load() != 1 {
		t.Fatalf("on: %d requests", hits.Load())
	}
	o.UnloadBeforeRender = false
	unloadWriter(t.Context(), o)
	if hits.Load() != 1 {
		t.Fatalf("off: %d requests", hits.Load())
	}
	o.UnloadBeforeRender, o.URL = true, "http://127.0.0.1:1"
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	unloadWriter(ctx, o)
	if time.Since(start) > time.Second {
		t.Fatal("an unreachable Ollama held up the render")
	}
}

func TestDraftsSendTheConfiguredContextAndKeepAlive(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{}"}}`))
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Ollama.URL, cfg.Ollama.Context, cfg.Ollama.KeepAlive = srv.URL, 32768, "2m"
	w, _ := newWriter(cfg, "")
	_, _ = w.Write(t.Context(), "an idea", writer.Options{})
	if body["keep_alive"] != "2m" || body["options"].(map[string]any)["num_ctx"] != float64(32768) {
		t.Fatalf("request %v", body)
	}
	cfg.Ollama.Context = 0
	w, _ = newWriter(cfg, "")
	_, _ = w.Write(t.Context(), "an idea", writer.Options{})
	if _, sent := body["options"].(map[string]any)["num_ctx"]; sent {
		t.Fatalf("a context of 0 was sent: %v", body)
	}
}
