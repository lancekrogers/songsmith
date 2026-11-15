// Package comfytest is a stand-in ComfyUI server for tests: it queues
// prompts, holds them until a test finishes them, and can stop answering
// and come back at the same address.
package comfytest

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/lancekrogers/songsmith/internal/comfy"
)

// GiB is a gibibyte, for Server.Memory.
const GiB = int64(1) << 30

// Server answers the parts of the ComfyUI API songsmith uses.
type Server struct {
	URL string

	srv    *httptest.Server
	prefix string

	mu sync.Mutex
	// Hold keeps each prompt running until Finish; otherwise it finishes
	// as soon as it is queued.
	hold bool
	down bool
	// silent is open while the server says nothing at all.
	silent  chan struct{}
	memory  int64
	models  []string
	missing string
	cuts    int
	refuses int
	hcuts   int
	n       int
	state   map[string]string
	graphs  map[string]comfy.Graph
	order   []string
	stopped []string
	clients map[string]chan string
	conns   map[*websocket.Conn]bool
	queued  chan string
}

// New starts a server whose prompt IDs start with name, so IDs from two
// servers never collide. It is closed when the test ends.
func New(t testing.TB, name string) *Server {
	s := newServer(name)
	s.srv = httptest.NewServer(s.handler())
	s.URL = s.srv.URL
	t.Cleanup(s.Close)
	return s
}

// Listen starts a server on addr, for stand-ins outside tests such as the
// demo recordings. Close stops it.
func Listen(addr, name string) (*Server, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := newServer(name)
	s.srv = httptest.NewUnstartedServer(s.handler())
	s.srv.Listener.Close()
	s.srv.Listener = l
	s.srv.Start()
	s.URL = s.srv.URL
	return s, nil
}

func newServer(name string) *Server {
	return &Server{prefix: name, memory: 64 * GiB, state: map[string]string{}, graphs: map[string]comfy.Graph{},
		clients: map[string]chan string{}, conns: map[*websocket.Conn]bool{}, queued: make(chan string, 64)}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /system_stats", s.stats)
	mux.HandleFunc("GET /object_info/{class}", s.objectInfo)
	mux.HandleFunc("GET /ws", s.ws)
	mux.HandleFunc("POST /prompt", s.prompt)
	mux.HandleFunc("GET /history/{id}", s.history)
	mux.HandleFunc("GET /queue", s.queue)
	mux.HandleFunc("POST /queue", s.deleteQueued)
	mux.HandleFunc("POST /interrupt", s.interrupt)
	mux.HandleFunc("POST /free", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /view", s.view)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if quiet := s.quiet(); quiet != nil {
			select {
			case <-quiet:
			case <-r.Context().Done():
			}
			drop(w)
			return
		}
		if s.isDown() {
			drop(w)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Close stops the server for good; its address then refuses connections.
func (s *Server) Close() {
	s.SetDown(true)
	s.SetSilent(false)
	s.srv.Close()
}

// SetSilent makes the server stop answering without closing anything, as
// a computer that went to sleep or lost its network does: requests hang and
// the websocket goes quiet. Prompts keep their state. When it answers again,
// the requests that hung are dropped.
func (s *Server) SetSilent(silent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case silent && s.silent == nil:
		s.silent = make(chan struct{})
	case !silent && s.silent != nil:
		close(s.silent)
		s.silent = nil
	}
}

func (s *Server) quiet() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.silent
}

// SetHold makes new prompts wait for Finish.
func (s *Server) SetHold(hold bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hold = hold
}

// SetMemory sets the memory the server reports.
func (s *Server) SetMemory(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memory = bytes
}

// SetModels sets the model files the loaders list; nil lists none, so the
// files cannot be checked.
func (s *Server) SetModels(files ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = files
}

// SetMissingNode makes the server not know a node class, as an older
// ComfyUI does.
func (s *Server) SetMissingNode(class string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.missing = class
}

// CutDownloads makes the next n downloads drop the connection halfway
// through the file.
func (s *Server) CutDownloads(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cuts = n
}

// RefuseDownloads makes the next n downloads answer with a server error.
func (s *Server) RefuseDownloads(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuses = n
}

// CutHistory makes the next n history answers drop the connection halfway
// through.
func (s *Server) CutHistory(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hcuts = n
}

// SetDown makes the server drop every connection, as a machine that went to
// sleep or left the network does, or answer again. Prompts keep their state.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	s.down = down
	var conns []*websocket.Conn
	if down {
		conns = slices.Collect(maps.Keys(s.conns))
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.CloseNow()
	}
}

func (s *Server) isDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down
}

// Queued delivers the ID of each prompt as it is queued.
func (s *Server) Queued() <-chan string { return s.queued }

// Prompts lists the IDs of every prompt queued, in order.
func (s *Server) Prompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

// Graph returns the graph queued as id.
func (s *Server) Graph(id string) comfy.Graph {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.graphs[id]
}

// Stopped lists the prompts interrupted or deleted from the queue.
func (s *Server) Stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.stopped)
}

// Running reports whether id is still rendering.
func (s *Server) Running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state[id] == "running"
}

// Progress tells every client that prompt id has done value of max sampler steps.
func (s *Server) Progress(id string, value, max int) {
	s.broadcast(fmt.Sprintf(`{"type":"progress","data":{"prompt_id":%q,"node":%q,"value":%d,"max":%d}}`, id, comfy.NodeSampler, value, max))
}

func (s *Server) broadcast(msg string) {
	s.mu.Lock()
	chans := slices.Collect(maps.Values(s.clients))
	s.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- msg:
		default:
		}
	}
}

// Finish ends a held prompt with an audio output.
func (s *Server) Finish(id string) {
	s.mu.Lock()
	s.state[id] = "done"
	s.mu.Unlock()
	s.broadcast(fmt.Sprintf(`{"type":"execution_success","data":{"prompt_id":%q}}`, id))
}

func drop(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if conn, _, err := hj.Hijack(); err == nil {
		conn.Close()
	}
}

func (s *Server) view(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	refuse := s.refuses > 0
	if refuse {
		s.refuses--
	}
	s.mu.Unlock()
	if refuse {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	s.answer(w, &s.cuts, "audio from "+s.prefix)
}

// answer writes body, or while *cuts is above zero counts it down and drops
// the connection halfway through body.
func (s *Server) answer(w http.ResponseWriter, cuts *int, body string) {
	s.mu.Lock()
	cut := *cuts > 0
	if cut {
		*cuts--
	}
	s.mu.Unlock()
	if !cut {
		io.WriteString(w, body)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	io.WriteString(w, body[:len(body)/2])
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}

func (s *Server) stats(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(w, `{"system":{"comfyui_version":"0.3.99","ram_total":%d,"argv":["main.py","--gpu-only"]},"devices":[{"name":"mps","type":"mps","vram_total":%d}]}`, s.memory, s.memory)
}

func (s *Server) objectInfo(w http.ResponseWriter, r *http.Request) {
	class := r.PathValue("class")
	s.mu.Lock()
	inputs, ok := comfy.RequiredNodes[class]
	if class == s.missing {
		ok = false
	}
	models := s.models
	s.mu.Unlock()
	if !ok {
		io.WriteString(w, "{}")
		return
	}
	req := map[string]any{}
	for _, in := range inputs {
		req[in] = []any{"STRING"}
		switch in {
		case "unet_name", "clip_name", "vae_name":
			if models != nil {
				req[in] = []any{models}
			}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{class: map[string]any{"input": map[string]any{"required": req}}})
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	ch := make(chan string, 64)
	id := r.URL.Query().Get("clientId")
	s.mu.Lock()
	s.clients[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, id)
		s.mu.Unlock()
	}()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	s.mu.Lock()
	s.conns[conn] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	for {
		select {
		case msg := <-ch:
			if s.quiet() != nil {
				continue
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(msg)) != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt   comfy.Graph `json:"prompt"`
		ClientID string      `json:"client_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.n++
	id := fmt.Sprintf("%s-%d", s.prefix, s.n)
	s.order = append(s.order, id)
	s.graphs[id] = body.Prompt
	s.state[id] = "running"
	hold := s.hold
	ch := s.clients[body.ClientID]
	s.mu.Unlock()
	fmt.Fprintf(w, `{"prompt_id":%q,"node_errors":{}}`, id)
	if ch != nil {
		ch <- fmt.Sprintf(`{"type":"execution_start","data":{"prompt_id":%q}}`, id)
		ch <- fmt.Sprintf(`{"type":"progress","data":{"prompt_id":%q,"node":%q,"value":1,"max":30}}`, id, comfy.NodeSampler)
	}
	select {
	case s.queued <- id:
	default:
	}
	if !hold {
		s.Finish(id)
	}
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	st := s.state[id]
	sample := s.graphs[id][comfy.NodeSaveLatent].ClassType != ""
	s.mu.Unlock()
	var body string
	switch {
	case st == "done" && sample:
		body = fmt.Sprintf(`{%q:{"status":{"status_str":"success","completed":true},"outputs":{"save_latent":{"latents":[{"filename":%q,"subfolder":"songsmith/latents","type":"output"}]}}}}`, id, id+".latent")
	case st == "done":
		body = fmt.Sprintf(`{%q:{"status":{"status_str":"success","completed":true},"outputs":{"save":{"audio":[{"filename":"song.flac","subfolder":"songsmith","type":"output"}]}}}}`, id)
	case st == "interrupted":
		body = fmt.Sprintf(`{%q:{"status":{"status_str":"error","completed":false,"messages":[["execution_interrupted",{"prompt_id":%q}]]},"outputs":{}}}`, id, id)
	default:
		body = "{}"
	}
	s.answer(w, &s.hcuts, body)
}

func (s *Server) queue(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []string
	for id, st := range s.state {
		if st == "running" {
			items = append(items, fmt.Sprintf(`[0,%q,{},{},[]]`, id))
		}
	}
	fmt.Fprintf(w, `{"queue_running":[%s],"queue_pending":[]}`, strings.Join(items, ","))
}

func (s *Server) deleteQueued(_ http.ResponseWriter, r *http.Request) {
	var body struct{ Delete []string }
	json.NewDecoder(r.Body).Decode(&body)
	s.stop(body.Delete...)
}

func (s *Server) interrupt(_ http.ResponseWriter, r *http.Request) {
	var body struct {
		PromptID string `json:"prompt_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.stop(body.PromptID)
}

func (s *Server) stop(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if s.state[id] == "running" {
			s.state[id] = "interrupted"
		}
		if id != "" && !slices.Contains(s.stopped, id) {
			s.stopped = append(s.stopped, id)
		}
	}
}
