// Command setupfake stands in for Hugging Face, Ollama and ComfyUI while the
// demo tapes record, so a recording downloads nothing real and reaches no
// real service. It serves small generated model files with a catalog of
// their sizes and SHA-256 sums, and an Ollama that pulls a model in a few
// seconds; with -library-addr, an Ollama with a library of made-up models
// that writes a canned draft, and with -comfy-addr, a ComfyUI that lists its
// nodes and samplers and renders nothing. It is for demos and tests only and
// listens on loopback.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fixture struct {
	Dir         string `json:"dir"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Label       string `json:"label"`
	Tag         string `json:"tag,omitempty"`
	Note        string `json:"note"`
	Recommended bool   `json:"recommended,omitempty"`
	seed        byte
}

// fixtures mirror the real repository's file names at a small size.
var fixtures = []fixture{
	{Dir: "diffusion_models", Name: "minimax_music3_dit_fp16.safetensors", Size: 48 << 20, Label: "DiT fp16", Note: "Recommended diffusion model.", Recommended: true},
	{Dir: "diffusion_models", Name: "minimax_music3_dit_fp32.safetensors", Size: 96 << 20, Label: "DiT fp32", Note: "Full precision."},
	{Dir: "text_encoders", Name: "minimax_music3_text_encoder_pruned_bf16.safetensors", Size: 120 << 20, Label: "Text encoder bf16 (pruned)", Note: "Recommended text encoder.", Recommended: true},
	{Dir: "vae", Name: "minimax_music3_dav.safetensors", Size: 12 << 20, Label: "VAE", Note: "The only VAE; required.", Recommended: true},
}

// content is fixture f's byte at offset i: a fixed pattern, so the sum is
// known without storing the file.
func (f fixture) content(off int64, p []byte) {
	for i := range p {
		n := off + int64(i)
		p[i] = byte(n*31+n>>9) ^ f.seed
	}
}

func (f *fixture) sum() {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for off := int64(0); off < f.Size; off += int64(len(buf)) {
		n := min(int64(len(buf)), f.Size-off)
		f.content(off, buf[:n])
		h.Write(buf[:n])
	}
	f.SHA256 = hex.EncodeToString(h.Sum(nil))
}

type server struct {
	rate  int64
	model string

	mu     sync.Mutex
	pulled bool
}

func (s *server) catalog(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fixtures)
}

// file answers like huggingface.co/<repo>/resolve/main/<dir>/<name>: a HEAD
// with the size and sum, and a throttled GET that honours Range.
func (s *server) file(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/hf/")
	i := slices.IndexFunc(fixtures, func(f fixture) bool { return f.Dir+"/"+f.Name == path })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	f := fixtures[i]
	w.Header().Set("X-Linked-Size", strconv.FormatInt(f.Size, 10))
	w.Header().Set("X-Linked-Etag", `"`+f.SHA256+`"`)
	if r.Method == http.MethodHead {
		return
	}
	start := int64(0)
	if v, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
		from, _, _ := strings.Cut(v, "-")
		if n, err := strconv.ParseInt(from, 10, 64); err == nil && n < f.Size {
			start = n
		}
	}
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size-start, 10))
	if start > 0 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, f.Size-1, f.Size))
		w.WriteHeader(http.StatusPartialContent)
	}
	chunk := s.rate / 20
	buf := make([]byte, chunk)
	for off := start; off < f.Size; off += chunk {
		n := min(chunk, f.Size-off)
		f.content(off, buf[:n])
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *server) tags(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	pulled := s.pulled
	s.mu.Unlock()
	models := []map[string]string{}
	if pulled {
		models = append(models, map[string]string{"name": s.model})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
}

// pull streams Ollama's /api/pull lines for one layer.
func (s *server) pull(w http.ResponseWriter, r *http.Request) {
	enc := json.NewEncoder(w)
	line := func(v map[string]any) {
		_ = enc.Encode(v)
		w.(http.Flusher).Flush()
	}
	line(map[string]any{"status": "pulling manifest"})
	const total = 2_400_000_000
	for done := int64(0); done <= total; done += total / 40 {
		line(map[string]any{"status": "pulling layer", "digest": "sha256:demo", "total": total, "completed": done})
		select {
		case <-r.Context().Done():
			return
		case <-time.After(75 * time.Millisecond):
		}
	}
	line(map[string]any{"status": "verifying sha256 digest"})
	line(map[string]any{"status": "writing manifest"})
	s.mu.Lock()
	s.pulled = true
	s.mu.Unlock()
	line(map[string]any{"status": "success"})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18490", "loopback address to listen on")
	rate := flag.Int64("rate", 24<<20, "download speed in bytes per second")
	model := flag.String("model", "songwriter:demo", "the model the fake Ollama pulls")
	libraryAddr := flag.String("library-addr", "", "also serve an Ollama with a library of made-up models here")
	comfyAddr := flag.String("comfy-addr", "", "also serve a stand-in running ComfyUI here")
	flag.Parse()
	for _, a := range []string{*addr, *libraryAddr, *comfyAddr} {
		if a == "" {
			continue
		}
		host, _, err := net.SplitHostPort(a)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			log.Fatalf("listen on a loopback address, not %q", a)
		}
	}
	if *libraryAddr != "" {
		go serveLibrary(*libraryAddr)
	}
	if *comfyAddr != "" {
		go serveComfy(*comfyAddr)
	}
	for i := range fixtures {
		fixtures[i].seed = byte(i * 53)
		fixtures[i].sum()
	}
	s := &server{rate: max(*rate, 20), model: *model}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /catalog.json", s.catalog)
	mux.HandleFunc("/hf/", s.file)
	mux.HandleFunc("GET /api/tags", s.tags)
	mux.HandleFunc("POST /api/pull", s.pull)
	log.Printf("setupfake on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
