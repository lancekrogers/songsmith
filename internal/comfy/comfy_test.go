package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeComfy struct {
	t          *testing.T
	mu         sync.Mutex
	promptResp string
	promptCode int
	events     []string
	history    string
	interrupts []string
	deletes    []string
	queueJSON  string
	histCalls  int
	queued     chan struct{}
	hold       chan struct{}
}

func newFake(t *testing.T) (*fakeComfy, *Client) {
	f := &fakeComfy{t: t, promptCode: 200, promptResp: `{"prompt_id":"p1","node_errors":{}}`, queued: make(chan struct{}, 1),
		queueJSON: `{"queue_running":[],"queue_pending":[]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		<-f.queued
		for _, e := range f.events {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(e)); err != nil {
				return
			}
		}
		if f.hold != nil {
			select {
			case <-f.hold:
			case <-r.Context().Done():
			}
		}
	})
	mux.HandleFunc("POST /prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt   Graph  `json:"prompt"`
			ClientID string `json:"client_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ClientID == "" || len(body.Prompt) == 0 {
			http.Error(w, "bad request", 400)
			return
		}
		w.WriteHeader(f.promptCode)
		io.WriteString(w, f.promptResp)
		f.queued <- struct{}{}
	})
	mux.HandleFunc("GET /history/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.histCalls++
		h := f.history
		f.mu.Unlock()
		if h == "" {
			h = "{}"
		}
		io.WriteString(w, h)
	})
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		io.WriteString(w, f.queueJSON)
	})
	mux.HandleFunc("POST /queue", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Delete []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.deletes = append(f.deletes, body.Delete...)
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /interrupt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PromptID string `json:"prompt_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.interrupts = append(f.interrupts, body.PromptID)
		f.mu.Unlock()
	})
	mux.HandleFunc("GET /view", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filename") != "song.flac" || r.URL.Query().Get("subfolder") != "songsmith" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, "audio-bytes")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, NewClient(srv.URL)
}

func ev(typ, data string) string { return fmt.Sprintf(`{"type":%q,"data":%s}`, typ, data) }

const doneHistory = `{"p1":{"status":{"status_str":"success","completed":true},"outputs":{"save":{"audio":[{"filename":"song.flac","subfolder":"songsmith","type":"output"}]}}}}`

func TestRunSuccess(t *testing.T) {
	f, c := newFake(t)
	f.events = []string{
		ev("status", `{"status":{}}`),
		ev("progress", `{"prompt_id":"other","node":"encode","value":1,"max":9}`),
		ev("progress", `{"prompt_id":"p1","node":"encode","value":5,"max":751}`),
		ev("progress", `{"prompt_id":"p1","node":"sampler","value":30,"max":30}`),
		ev("execution_success", `{"prompt_id":"p1"}`),
	}
	f.history = doneHistory

	var got []Event
	var queuedID string
	h, err := c.Run(t.Context(), MiniMaxGraph(MiniMaxParams{}), RunOptions{OnEvent: func(e Event) { got = append(got, e) }, OnQueued: func(id string) { queuedID = id }})
	if err != nil {
		t.Fatal(err)
	}
	if queuedID != "p1" {
		t.Fatalf("queued id = %q", queuedID)
	}
	if len(got) != 3 || got[0].Node != NodeEncode || got[0].Max != 751 || got[1].Node != NodeSampler {
		t.Fatalf("events = %+v", got)
	}
	files := h.AudioFiles()
	if len(files) != 1 || files[0].Filename != "song.flac" {
		t.Fatalf("files = %+v", files)
	}
	var sb strings.Builder
	if err := c.Download(t.Context(), files[0], &sb); err != nil || sb.String() != "audio-bytes" {
		t.Fatalf("download = %q, %v", sb.String(), err)
	}
}

func TestRunExecutionError(t *testing.T) {
	f, c := newFake(t)
	f.events = []string{ev("execution_error", `{"prompt_id":"p1","node_id":"encode","exception_type":"RuntimeError","exception_message":"MPS out of memory"}`)}
	_, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{})
	var ee *ExecutionError
	if !errors.As(err, &ee) || ee.Node != NodeEncode || !strings.Contains(ee.Message, "MPS out of memory") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectedPrompt(t *testing.T) {
	f, c := newFake(t)
	f.promptResp = `{"error":{"type":"prompt_outputs_failed_validation"},"node_errors":{"encode":{"errors":["bad"]}}}`
	f.promptCode = 400
	_, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{})
	var pe *PromptError
	if !errors.As(err, &pe) || pe.Status != 400 || !strings.Contains(pe.Body, "encode") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunCancelInterrupts(t *testing.T) {
	f, c := newFake(t)
	f.hold = make(chan struct{})
	defer close(f.hold)
	f.events = []string{ev("progress", `{"prompt_id":"p1","node":"encode","value":1,"max":751}`)}
	ctx, cancel := context.WithCancel(t.Context())
	_, err := c.Run(ctx, Graph{"x": {}}, RunOptions{OnEvent: func(Event) { cancel() }})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.interrupts) != 1 || f.interrupts[0] != "p1" || len(f.deletes) != 1 || f.deletes[0] != "p1" {
		t.Fatalf("interrupts = %v deletes = %v", f.interrupts, f.deletes)
	}
}

func TestRunFallsBackToHistoryWhenSocketDrops(t *testing.T) {
	f, c := newFake(t)
	f.events = nil
	f.history = doneHistory
	h, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{})
	if err != nil || len(h.AudioFiles()) != 1 {
		t.Fatalf("h = %+v, err = %v", h, err)
	}
}

func TestMiniMaxGraphMatchesRequiredNodes(t *testing.T) {
	g := MiniMaxGraph(MiniMaxParams{Caption: "c", Lyrics: "[Verse]\nl", Seed: 7, MaxSeconds: 60, CFGScale: 1.7, TopK: 50, Steps: 30,
		DiffusionModel: "dit", TextEncoder: "te", VAE: "vae", FilenamePrefix: "songsmith/x"})
	for id, n := range g {
		req, ok := RequiredNodes[n.ClassType]
		if !ok {
			t.Errorf("node %s uses unchecked class %s", id, n.ClassType)
			continue
		}
		for _, in := range req {
			if _, ok := n.Inputs[in]; !ok {
				t.Errorf("node %s (%s) missing input %s", id, n.ClassType, in)
			}
		}
		for in, v := range n.Inputs {
			if l, ok := v.([]any); ok {
				if _, exists := g[l[0].(string)]; !exists {
					t.Errorf("node %s input %s links to missing node %v", id, in, l[0])
				}
			}
		}
	}
	if g[NodeLatent].Inputs["seconds"].([]any)[1] != 1 {
		t.Error("latent length must come from the encoder's seconds output")
	}
	if g[NodeTextEncoder].Inputs["type"] != "minimax" {
		t.Error("CLIPLoader type must be minimax")
	}
}

func TestComboOptions(t *testing.T) {
	var info NodeInfo
	if err := json.Unmarshal([]byte(`{"input":{"required":{"unet_name":[["a.safetensors","b.safetensors"],{}],"weight_dtype":[["default"]]}}}`), &info); err != nil {
		t.Fatal(err)
	}
	if got := info.ComboOptions("unet_name"); len(got) != 2 || got[1] != "b.safetensors" {
		t.Fatalf("got %v", got)
	}
	if !info.HasInput("weight_dtype") || info.HasInput("nope") {
		t.Fatal("HasInput wrong")
	}
}

func TestRunResumeFinishedPrompt(t *testing.T) {
	f, c := newFake(t)
	f.history = doneHistory
	h, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{ResumeID: "p1"})
	if err != nil || len(h.AudioFiles()) != 1 {
		t.Fatalf("h = %+v err = %v", h, err)
	}
	if len(f.queued) != 0 {
		t.Fatal("resuming a finished prompt must not queue a new one")
	}
}

func TestRunResumeQueuedPromptWaitsForIt(t *testing.T) {
	f, c := newFake(t)
	f.queueJSON = `{"queue_running":[[1,"old",{},{},[]]],"queue_pending":[]}`
	go func() {
		time.Sleep(1500 * time.Millisecond)
		f.mu.Lock()
		f.history = `{"old":{"status":{"status_str":"success","completed":true},"outputs":{"save":{"audio":[{"filename":"a.flac","subfolder":"songsmith","type":"output"}]}}}}`
		f.mu.Unlock()
	}()
	var queuedID string
	h, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{ResumeID: "old", OnQueued: func(id string) { queuedID = id }})
	if err != nil || queuedID != "old" || h.AudioFiles()[0].Filename != "a.flac" {
		t.Fatalf("h = %+v err = %v id = %q", h, err, queuedID)
	}
}

func TestRunUnknownResumeQueuesFresh(t *testing.T) {
	f, c := newFake(t)
	f.events = []string{ev("execution_success", `{"prompt_id":"p1"}`)}
	f.history = doneHistory
	var queuedID string
	if _, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{ResumeID: "gone", OnQueued: func(id string) { queuedID = id }}); err != nil {
		t.Fatal(err)
	}
	if queuedID != "p1" {
		t.Fatalf("queued = %q", queuedID)
	}
}

func TestWaitHistoryDetectsLostPrompt(t *testing.T) {
	_, c := newFake(t)
	start := time.Now()
	_, err := c.waitHistory(t.Context(), "vanished")
	if !errors.Is(err, ErrPromptLost) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("took too long to notice")
	}
}

func TestMiniMaxGraphSamplerSettings(t *testing.T) {
	in := MiniMaxGraph(MiniMaxParams{}).sampler()
	if in["sampler_name"] != "euler" || in["scheduler"] != "simple" {
		t.Fatalf("defaults = %v / %v", in["sampler_name"], in["scheduler"])
	}
	in = MiniMaxGraph(MiniMaxParams{Sampler: "dpmpp_2m", Scheduler: "karras"}).sampler()
	if in["sampler_name"] != "dpmpp_2m" || in["scheduler"] != "karras" {
		t.Fatalf("configured = %v / %v", in["sampler_name"], in["scheduler"])
	}
}

func (g Graph) sampler() map[string]any { return g[NodeSampler].Inputs }

func TestStagedGraphsMatchRequiredNodes(t *testing.T) {
	p := MiniMaxParams{Caption: "c", Lyrics: "l", Seed: 7, MaxSeconds: 60, CFGScale: 1.7, TopK: 50, Steps: 30,
		DiffusionModel: "dit", TextEncoder: "te", VAE: "vae", FilenamePrefix: "songsmith/x"}
	sample := MiniMaxSampleGraph(p, "songsmith/latents/x")
	decode := MiniMaxDecodeGraph("vae", "songsmith/latents/x_00001_.latent [output]", "songsmith/x")
	for name, g := range map[string]Graph{"sample": sample, "decode": decode} {
		for id, n := range g {
			req, ok := RequiredNodes[n.ClassType]
			if !ok {
				t.Errorf("%s: node %s uses unchecked class %s", name, id, n.ClassType)
			}
			for _, in := range req {
				if _, ok := n.Inputs[in]; !ok {
					t.Errorf("%s: node %s missing input %s", name, id, in)
				}
			}
			for in, v := range n.Inputs {
				if l, ok := v.([]any); ok {
					if _, exists := g[l[0].(string)]; !exists {
						t.Errorf("%s: node %s input %s links to missing node %v", name, id, in, l[0])
					}
				}
			}
		}
	}
	full := MiniMaxGraph(p)
	for _, id := range []string{NodeTextEncoder, NodeDiffusion, NodeEncode, NodeNegative, NodeLatent, NodeSampler} {
		a, _ := json.Marshal(sample[id])
		b, _ := json.Marshal(full[id])
		if string(a) != string(b) {
			t.Errorf("sample graph changed %s: %s, want %s", id, a, b)
		}
	}
	for _, id := range []string{NodeVAE, NodeDecode, NodeSave} {
		if _, ok := sample[id]; ok {
			t.Errorf("sample graph still has %s", id)
		}
	}
	if len(decode) != 4 || decode[NodeDecode].Inputs["samples"].([]any)[0] != NodeLoadLatent {
		t.Errorf("decode graph = %+v", decode)
	}
}

func TestLatentRefRoundTrips(t *testing.T) {
	f := OutputFile{Filename: "song_00001_.latent", Subfolder: "songsmith/latents", Type: "output"}
	ref := LatentRef(f)
	if ref != "songsmith/latents/song_00001_.latent [output]" {
		t.Fatalf("ref = %q", ref)
	}
	if got, ok := ParseLatentRef(ref); !ok || got != f {
		t.Fatalf("parsed %+v %v", got, ok)
	}
	if _, ok := ParseLatentRef("no-tag.latent"); ok {
		t.Fatal("parsed a ref without a folder tag")
	}
}

func TestOutOfMemory(t *testing.T) {
	for _, c := range []struct {
		err  error
		node string
		want bool
	}{
		{&ExecutionError{Node: NodeDecode, Message: "RuntimeError: MPS backend out of memory (MPS allocated: 40.95 GiB)"}, NodeDecode, true},
		{&ExecutionError{Node: NodeDecode, Message: "OutOfMemoryError: CUDA out of memory. Tried to allocate 4.75 GiB"}, NodeDecode, true},
		{&ExecutionError{Node: NodeDecode, Message: "torch.OutOfMemoryError: Allocation failed"}, NodeDecode, true},
		{fmt.Errorf("wrapped: %w", &ExecutionError{Node: NodeDecode, Message: "out of memory"}), NodeDecode, true},
		{&ExecutionError{Node: NodeSampler, Message: "MPS backend out of memory"}, NodeDecode, false},
		{&ExecutionError{Node: NodeDecode, Message: "RuntimeError: shape mismatch"}, NodeDecode, false},
		{errors.New("out of memory"), NodeDecode, false},
	} {
		if got := OutOfMemory(c.err, c.node); got != c.want {
			t.Errorf("%v at %s = %v", c.err, c.node, got)
		}
	}
}

func TestFinishedPromptReportsItsError(t *testing.T) {
	f, c := newFake(t)
	f.history = `{"p1":{"status":{"status_str":"error","completed":false,"messages":[["execution_start",{"prompt_id":"p1"}],` +
		`["execution_error",{"prompt_id":"p1","node_id":"decode","exception_type":"RuntimeError","exception_message":"MPS backend out of memory"}]]},"outputs":{}}}`
	_, err := c.Run(t.Context(), Graph{"x": {}}, RunOptions{ResumeID: "p1"})
	if !OutOfMemory(err, NodeDecode) {
		t.Fatalf("err = %v", err)
	}
}

func TestFreeAndSystemStats(t *testing.T) {
	var free map[string]bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /free", func(w http.ResponseWriter, r *http.Request) { json.NewDecoder(r.Body).Decode(&free) })
	mux.HandleFunc("GET /system_stats", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"system":{"ram_total":34359738368,"argv":["main.py","--output-directory","/srv/comfy-out"]},"devices":[{"type":"mps","vram_total":34359738368}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	if err := c.Free(t.Context(), true, true); err != nil || !free["unload_models"] || !free["free_memory"] {
		t.Fatalf("free = %v, %v", free, err)
	}
	s, err := c.SystemStats(t.Context())
	if err != nil || s.Memory() != 32<<30 {
		t.Fatalf("memory = %d, %v", s.Memory(), err)
	}
	if dir, err := c.OutputDir(t.Context()); err != nil || dir != "/srv/comfy-out" {
		t.Fatalf("output dir = %q, %v", dir, err)
	}
	if !c.Local() || NewClient("http://10.0.0.5:8188").Local() || !NewClient("http://[::1]:8188").Local() {
		t.Fatal("Local is wrong")
	}
}

func TestMemoryPrefersADedicatedGPU(t *testing.T) {
	var s SystemStats
	s.System.RAMTotal = 128 << 30
	s.Devices = []Device{{Type: "cuda", VRAMTotal: 24 << 30}}
	if s.Memory() != 24<<30 {
		t.Fatalf("cuda memory = %d", s.Memory())
	}
	s.Devices = []Device{{Type: "mps", VRAMTotal: 128 << 30}}
	if s.Memory() != 128<<30 {
		t.Fatalf("mps memory = %d", s.Memory())
	}
}

func TestEveryRenderGraphDecodesInTiles(t *testing.T) {
	graphs := map[string]Graph{
		"single prompt": MiniMaxGraph(MiniMaxParams{}),
		"decode stage":  MiniMaxDecodeGraph("vae.safetensors", "songsmith/latents/x_00001_.latent [output]", "songsmith/x"),
	}
	for name, g := range graphs {
		n := g[NodeDecode]
		if n.ClassType != "VAEDecodeAudioTiled" {
			t.Errorf("%s decodes with %s; a full-length decode corrupts songs over about 3:07 on MPS", name, n.ClassType)
		}
		if n.Inputs["tile_size"] != 512 || n.Inputs["overlap"] != 64 {
			t.Errorf("%s tile settings = %v / %v, want 512 / 64", name, n.Inputs["tile_size"], n.Inputs["overlap"])
		}
	}
	if _, ok := MiniMaxSampleGraph(MiniMaxParams{}, "songsmith/latents/x")[NodeDecode]; ok {
		t.Error("the sampling stage must not decode")
	}
}
