package ollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatAndModels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Stream || req.Model != "m" {
			http.Error(w, "bad", 400)
			return
		}
		io.WriteString(w, `{"message":{"role":"assistant","content":"{\"ok\":true}"}}`)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"name":"m"},{"name":"qwen3.8:latest"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewClient(srv.URL)

	got, err := c.Chat(t.Context(), ChatRequest{Model: "m", Stream: true})
	if err != nil || got != `{"ok":true}` {
		t.Fatalf("chat = %q, %v", got, err)
	}
	names, err := c.Models(t.Context())
	if err != nil || len(names) != 2 || names[1] != "qwen3.8:latest" {
		t.Fatalf("models = %v, %v", names, err)
	}
	if _, err := c.Chat(t.Context(), ChatRequest{Model: "other"}); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
}
