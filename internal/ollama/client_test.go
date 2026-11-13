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

func TestATrailingSlashOnTheBaseURLIsIgnored(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		io.WriteString(w, `{"models":[{"name":"m"}]}`)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL + "/").Models(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/api/tags" {
		t.Fatalf("paths %q", paths)
	}
}

func TestModelInfosReadDetailsAndOptionalCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[
			{"name":"talker:8b","details":{"family":"llama","families":["llama"],"parameter_size":"8.0B","quantization_level":"Q4_K_M"},"capabilities":["completion","tools"]},
			{"name":"old-server:1b","details":{"family":"qwen2"}},
			{"name":"vectors:v1","details":{"family":"nomic-bert","families":["nomic-bert"]}},
			{"name":"finder:1b","details":{"family":"gemma3"},"capabilities":["embedding"]}]}`)
	}))
	defer srv.Close()
	infos, err := NewClient(srv.URL).ModelInfos(t.Context())
	if err != nil || len(infos) != 4 {
		t.Fatalf("infos %+v, %v", infos, err)
	}
	if m := infos[0]; m.ParameterSize != "8.0B" || m.Quantization != "Q4_K_M" || m.Family != "llama" || !m.CanChat() {
		t.Fatalf("talker %+v", m)
	}
	if m := infos[1]; m.Capabilities != nil || !m.CanChat() {
		t.Fatalf("a model without capabilities must be kept: %+v", m)
	}
	if infos[2].CanChat() || infos[3].CanChat() {
		t.Fatalf("embedding models can chat: %+v %+v", infos[2], infos[3])
	}
	if (ModelInfo{Name: "text-embed-small:latest"}).CanChat() {
		t.Fatal("an embed name can chat")
	}
}

func TestCapabilitiesAsksShow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/api/show" || body["model"] != "m:1b" {
			http.Error(w, "no", 404)
			return
		}
		io.WriteString(w, `{"capabilities":["completion","vision"],"details":{"family":"x"}}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if caps, err := c.Capabilities(t.Context(), "m:1b"); err != nil || len(caps) != 2 || caps[0] != "completion" {
		t.Fatalf("caps %v, %v", caps, err)
	}
	if _, err := c.Capabilities(t.Context(), "other"); err == nil {
		t.Fatal("a failed show returned no error")
	}
}

func TestChatSendsKeepAliveAndUnloadAsksForZero(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		io.WriteString(w, `{"message":{"role":"assistant","content":""},"done_reason":"unload","done":true}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if _, err := c.Chat(t.Context(), ChatRequest{Model: "m", KeepAlive: KeepAlive("5m"), Options: map[string]any{"num_ctx": 8192}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Unload(t.Context(), "m"); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["keep_alive"] != "5m" || bodies[0]["options"].(map[string]any)["num_ctx"] != float64(8192) {
		t.Fatalf("chat body %v", bodies[0])
	}
	if u := bodies[1]; u["model"] != "m" || u["keep_alive"] != float64(0) || len(u["messages"].([]any)) != 0 {
		t.Fatalf("unload body %v", u)
	}
	if !ValidKeepAlive("10m") || !ValidKeepAlive("0") || !ValidKeepAlive("-1") || ValidKeepAlive("soon") || KeepAlive("soon") != nil || KeepAlive("300") != 300 {
		t.Fatal("keep_alive validation")
	}
}
