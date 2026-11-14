package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
)

// fakeComfy is a ComfyUI API that runs each queued prompt from a script, so
// no test reaches a real server.
type fakeComfy struct {
	t    *testing.T
	root string

	mu       sync.Mutex
	clients  map[string]chan string
	n        int
	log      []string
	graphs   map[string]comfy.Graph
	history  map[string]string
	running  map[string]bool
	ram      int64
	statsErr bool
	// script decides what the prompt does: the events it sends after its
	// start, and whether it then succeeds, fails or keeps running.
	script func(id, kind string, attempt int) outcome
	tries  map[string]int
	// freedBefore counts the frees asked for before a prompt's history was
	// first read, which a render reads only once the prompt has finished.
	freedBefore map[string]int
}

type outcome struct {
	events []string
	result string // "ok", "oom", "hold"
}

const gib = int64(1) << 30

func newFakeComfy(t *testing.T) (*fakeComfy, *comfy.Client) {
	f := &fakeComfy{t: t, root: t.TempDir(), clients: map[string]chan string{}, graphs: map[string]comfy.Graph{},
		history: map[string]string{}, running: map[string]bool{}, tries: map[string]int{}, freedBefore: map[string]int{}, ram: 32 * gib}
	f.script = func(string, string, int) outcome { return outcome{result: "ok"} }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		ch := make(chan string, 64)
		f.mu.Lock()
		f.clients[r.URL.Query().Get("clientId")] = ch
		f.mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			select {
			case msg := <-ch:
				if conn.Write(r.Context(), websocket.MessageText, []byte(msg)) != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt   comfy.Graph `json:"prompt"`
			ClientID string      `json:"client_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		kind := graphKind(body.Prompt)
		if kind == "decode" {
			ref, _ := body.Prompt[comfy.NodeLoadLatent].Inputs["latent"].(string)
			if lf, ok := comfy.ParseLatentRef(ref); !ok || !fileExists(f.outputPath(lf)) {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":{"type":"prompt_outputs_failed_validation"},"node_errors":{%q:{"errors":["Invalid latent file"]}}}`, comfy.NodeLoadLatent)
				return
			}
		}
		f.mu.Lock()
		f.n++
		id := fmt.Sprintf("p%d", f.n)
		f.log = append(f.log, "prompt:"+kind)
		f.graphs[id] = body.Prompt
		attempt := f.tries[kind]
		f.tries[kind]++
		ch := f.clients[body.ClientID]
		f.mu.Unlock()
		out := f.script(id, kind, attempt)
		fmt.Fprintf(w, `{"prompt_id":%q,"node_errors":{}}`, id)
		send := func(typ, data string) { ch <- fmt.Sprintf(`{"type":%q,"data":%s}`, typ, data) }
		send("execution_start", fmt.Sprintf(`{"prompt_id":%q}`, id))
		for _, e := range out.events {
			ch <- strings.ReplaceAll(e, "$ID", id)
		}
		switch out.result {
		case "ok":
			f.finish(id, kind)
			send("execution_success", fmt.Sprintf(`{"prompt_id":%q}`, id))
		case "oom":
			f.fail(id)
			send("execution_error", fmt.Sprintf(`{"prompt_id":%q,"node_id":"decode","exception_type":"RuntimeError","exception_message":"MPS backend out of memory (MPS allocated: 40.95 GiB). Tried to allocate 4.75 GiB on shared pool."}`, id))
		case "hold":
			f.mu.Lock()
			f.running[id] = true
			f.mu.Unlock()
		}
	})
	mux.HandleFunc("GET /history/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		h, ok := f.history[r.PathValue("id")]
		if _, seen := f.freedBefore[r.PathValue("id")]; !seen {
			f.freedBefore[r.PathValue("id")] = count(f.log, "free")
		}
		f.mu.Unlock()
		if !ok {
			io.WriteString(w, "{}")
			return
		}
		fmt.Fprintf(w, `{%q:%s}`, r.PathValue("id"), h)
	})
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var items []string
		for id := range f.running {
			items = append(items, fmt.Sprintf(`[0,%q,{},{},[]]`, id))
		}
		fmt.Fprintf(w, `{"queue_running":[%s],"queue_pending":[]}`, strings.Join(items, ","))
	})
	mux.HandleFunc("POST /queue", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Delete []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.record("delete:" + strings.Join(body.Delete, ","))
	})
	mux.HandleFunc("POST /interrupt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PromptID string `json:"prompt_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.record("interrupt:" + body.PromptID)
	})
	mux.HandleFunc("POST /free", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]bool
		json.NewDecoder(r.Body).Decode(&body)
		if !body["unload_models"] || !body["free_memory"] {
			t.Errorf("free body = %v", body)
		}
		f.record("free")
	})
	mux.HandleFunc("GET /system_stats", func(w http.ResponseWriter, r *http.Request) {
		f.record("stats")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.statsErr {
			http.Error(w, "boom", 500)
			return
		}
		fmt.Fprintf(w, `{"system":{"ram_total":%d,"argv":["main.py","--gpu-only"]},"devices":[{"type":"mps","vram_total":%d}]}`, f.ram, f.ram)
	})
	mux.HandleFunc("GET /internal/folder_paths", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string][]string{"custom_nodes": {filepath.Join(f.root, "custom_nodes")}})
	})
	mux.HandleFunc("GET /view", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "audio-bytes")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, comfy.NewClient(srv.URL)
}

func graphKind(g comfy.Graph) string {
	switch {
	case g[comfy.NodeSaveLatent].ClassType != "":
		return "sample"
	case g[comfy.NodeLoadLatent].ClassType != "":
		return "decode"
	}
	return "single"
}

func (f *fakeComfy) record(s string) {
	f.mu.Lock()
	f.log = append(f.log, s)
	f.mu.Unlock()
}

func (f *fakeComfy) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func (f *fakeComfy) outputPath(o comfy.OutputFile) string {
	return filepath.Join(f.root, "output", filepath.FromSlash(o.Subfolder), o.Filename)
}

// latentFile is where the sample prompt id saves its latent.
func (f *fakeComfy) latentFile(id string) comfy.OutputFile {
	return comfy.OutputFile{Filename: "song_" + id + "_.latent", Subfolder: "songsmith/latents", Type: "output"}
}

// finish records a prompt as finished, writing the latent a sample prompt saves.
func (f *fakeComfy) finish(id, kind string) {
	out := `{"save":{"audio":[{"filename":"song.flac","subfolder":"songsmith","type":"output"}]}}`
	if kind == "sample" {
		lf := f.latentFile(id)
		p := f.outputPath(lf)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Error(err)
		}
		if err := os.WriteFile(p, []byte("latent"), 0o644); err != nil {
			f.t.Error(err)
		}
		data, _ := json.Marshal(lf)
		out = fmt.Sprintf(`{"save_latent":{"latents":[%s]}}`, data)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, id)
	f.history[id] = fmt.Sprintf(`{"status":{"status_str":"success","completed":true},"outputs":%s}`, out)
}

func (f *fakeComfy) fail(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, id)
	f.history[id] = `{"status":{"status_str":"error","completed":false,"messages":[["execution_start",{"prompt_id":"` + id +
		`"}],["execution_error",{"prompt_id":"` + id + `","node_id":"decode","exception_type":"RuntimeError","exception_message":"MPS backend out of memory"}]]},"outputs":{}}`
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func progressEvent(node string, value, maxValue int) string {
	return fmt.Sprintf(`{"type":"progress","data":{"prompt_id":"$ID","node":%q,"value":%d,"max":%d}}`, node, value, maxValue)
}

func executing(node string) string {
	return fmt.Sprintf(`{"type":"executing","data":{"prompt_id":"$ID","node":%q}}`, node)
}

// sampleEvents and decodeEvents walk every node of each prompt.
var (
	sampleEvents = []string{executing(comfy.NodeTextEncoder), executing(comfy.NodeDiffusion), executing(comfy.NodeEncode),
		progressEvent(comfy.NodeEncode, 5, 10), executing(comfy.NodeSampler), progressEvent(comfy.NodeSampler, 30, 30), executing(comfy.NodeSaveLatent)}
	decodeEvents = []string{executing(comfy.NodeVAE), executing(comfy.NodeLoadLatent), executing(comfy.NodeDecode),
		progressEvent(comfy.NodeDecode, 1, 2), executing(comfy.NodeSave)}
)

func testConfig(mode config.LowMemoryMode) config.Config {
	cfg := config.Default()
	cfg.Render.LowMemory = mode
	return cfg
}

// newTestBackend renders on f with mode, on a clock that ticks a second per read.
func newTestBackend(c *comfy.Client, mode config.LowMemoryMode) (*ComfyMiniMax, *[]time.Time) {
	b := NewComfyMiniMax(c, nil, func() config.Config { return testConfig(mode) })
	b.settle = time.Millisecond
	var reads []time.Time
	at := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	b.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(time.Second)
		reads = append(reads, at)
		return at
	}
	return b, &reads
}

func testRequest(t *testing.T) Request {
	return Request{ID: "song", Caption: "pop", Lyrics: "[Verse]\nla", Seed: 7, MaxSeconds: 150, OutPath: filepath.Join(t.TempDir(), "song.flac")}
}

func indexOf(calls []string, s string, from int) int {
	for i := from; i < len(calls); i++ {
		if calls[i] == s {
			return i
		}
	}
	return -1
}

func count(calls []string, s string) int {
	n := 0
	for _, c := range calls {
		if c == s {
			n++
		}
	}
	return n
}

func TestStagedRenderRunsSampleFreeDecode(t *testing.T) {
	f, c := newFakeComfy(t)
	f.script = func(_, kind string, _ int) outcome {
		if kind == "sample" {
			return outcome{events: sampleEvents, result: "ok"}
		}
		return outcome{events: decodeEvents, result: "ok"}
	}
	b, reads := newTestBackend(c, config.LowMemoryOn)
	req := testRequest(t)
	var ids []string
	req.OnStarted = func(id string) { ids = append(ids, id) }
	var stages []Stage
	res, err := b.Generate(t.Context(), req, func(p Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(res.AudioPath); string(data) != "audio-bytes" {
		t.Fatalf("audio = %q", data)
	}

	calls := f.calls()
	sample := indexOf(calls, "prompt:sample", 0)
	free := indexOf(calls, "free", sample)
	decode := indexOf(calls, "prompt:decode", free)
	if sample < 0 || free < 0 || decode < 0 || slices.Contains(calls, "prompt:single") || slices.Contains(calls[:sample], "stats") {
		t.Fatalf("calls = %v", calls)
	}
	if n := count(calls[sample:decode], "free"); n < 3 {
		t.Fatalf("freed %d times between the prompts: %v", n, calls)
	}
	f.mu.Lock()
	early := f.freedBefore["p1"]
	f.mu.Unlock()
	if early < 1 {
		t.Fatal("no free was asked for while the first prompt ran")
	}

	p := comfy.MiniMaxParams{Caption: req.Caption, Lyrics: req.Lyrics, Seed: req.Seed, MaxSeconds: req.MaxSeconds}
	cfg := testConfig(config.LowMemoryOn)
	p.CFGScale, p.TopK, p.Steps, p.Sampler, p.Scheduler = cfg.Render.CFGScale, cfg.Render.TopK, cfg.Render.Steps, cfg.Render.Sampler, cfg.Render.Scheduler
	p.DiffusionModel, p.TextEncoder, p.VAE, p.FilenamePrefix = cfg.ComfyUI.DiffusionModel, cfg.ComfyUI.TextEncoder, cfg.ComfyUI.VAE, "songsmith/song"
	ref := comfy.LatentRef(f.latentFile("p1"))
	assertGraph(t, f.graphs["p1"], comfy.MiniMaxSampleGraph(p, "songsmith/latents/song"))
	assertGraph(t, f.graphs["p2"], comfy.MiniMaxDecodeGraph(cfg.ComfyUI.VAE, ref, "songsmith/song"))

	want := []Stage{StageLoading, StageComposing, StageRendering, StageDecoding, StageSaving}
	if !slices.Equal(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
	var total time.Duration
	for _, s := range want {
		if res.Timings[s] <= 0 {
			t.Errorf("no time in %s: %v", s, res.Timings)
		}
		total += res.Timings[s]
	}
	if span := (*reads)[len(*reads)-1].Sub((*reads)[0]); total != span || len(res.Timings) != len(want) {
		t.Fatalf("timings %v add up to %s, the render took %s", res.Timings, total, span)
	}
	wantIDs := []string{"lowmem1:p1", "lowmem2::" + ref, "lowmem2:p2:" + ref}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("job ids = %q, want %q", ids, wantIDs)
	}
	if fileExists(f.outputPath(f.latentFile("p1"))) {
		t.Fatal("the latent was left behind after the render")
	}
}

func assertGraph(t *testing.T, got, want comfy.Graph) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("graph\n got %s\nwant %s", g, w)
	}
}

func TestOffRendersTheSingleGraph(t *testing.T) {
	f, c := newFakeComfy(t)
	b, _ := newTestBackend(c, config.LowMemoryOff)
	req := testRequest(t)
	if _, err := b.Generate(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(); !slices.Equal(calls, []string{"prompt:single"}) {
		t.Fatalf("calls = %v", calls)
	}
	cfg := testConfig(config.LowMemoryOff)
	assertGraph(t, f.graphs["p1"], comfy.MiniMaxGraph(comfy.MiniMaxParams{
		Caption: req.Caption, Lyrics: req.Lyrics, Seed: req.Seed, MaxSeconds: req.MaxSeconds,
		CFGScale: cfg.Render.CFGScale, TopK: cfg.Render.TopK, Steps: cfg.Render.Steps, Sampler: cfg.Render.Sampler, Scheduler: cfg.Render.Scheduler,
		DiffusionModel: cfg.ComfyUI.DiffusionModel, TextEncoder: cfg.ComfyUI.TextEncoder, VAE: cfg.ComfyUI.VAE, FilenamePrefix: "songsmith/song",
	}))
}

func TestAutoDecidesFromTheServersMemory(t *testing.T) {
	for _, c := range []struct {
		ram      int64
		statsErr bool
		want     string
	}{
		{ram: 32 * gib, want: "prompt:sample"},
		{ram: 48 * gib, want: "prompt:single"},
		{ram: 128 * gib, want: "prompt:single"},
		{statsErr: true, want: "prompt:single"},
	} {
		f, client := newFakeComfy(t)
		f.ram, f.statsErr = c.ram, c.statsErr
		b, _ := newTestBackend(client, config.LowMemoryAuto)
		if _, err := b.Generate(t.Context(), testRequest(t), nil); err != nil {
			t.Fatalf("%d GB: %v", c.ram/gib, err)
		}
		if calls := f.calls(); calls[0] != "stats" || calls[1] != c.want {
			t.Fatalf("%d GB (stats error %v): calls = %v, want %s", c.ram/gib, c.statsErr, calls, c.want)
		}
	}
}

func TestResolveLowMemory(t *testing.T) {
	for _, c := range []struct {
		mode   config.LowMemoryMode
		memory int64
		on     bool
		why    string
	}{
		{config.LowMemoryAuto, 32 * gib, true, "auto · on for this machine, 32 GB"},
		{config.LowMemoryAuto, 48 * gib, false, "auto · off for this machine, 48 GB"},
		{config.LowMemoryAuto, 128 * gib, false, "auto · off for this machine, 128 GB"},
		{config.LowMemoryAuto, 0, false, "auto · off, the server's memory is unknown"},
		{"", 32 * gib, true, "auto · on for this machine, 32 GB"},
		{config.LowMemoryOn, 128 * gib, true, "on"},
		{config.LowMemoryOff, 16 * gib, false, "off"},
	} {
		if on, why := ResolveLowMemory(c.mode, c.memory); on != c.on || why != c.why {
			t.Errorf("%q at %d GB = %v %q, want %v %q", c.mode, c.memory/gib, on, why, c.on, c.why)
		}
	}
}

func TestSingleRenderDecodeOOMSaysToTurnOnLowMemory(t *testing.T) {
	f, c := newFakeComfy(t)
	f.statsErr = true
	f.script = func(string, string, int) outcome { return outcome{result: "oom"} }
	b, _ := newTestBackend(c, config.LowMemoryAuto)
	_, err := b.Generate(t.Context(), testRequest(t), nil)
	var oom *DecodeOOMError
	if !errors.As(err, &oom) || !strings.Contains(err.Error(), "turn on low-memory rendering") ||
		!strings.Contains(err.Error(), "could not read the server's memory") || !strings.Contains(err.Error(), "MPS backend out of memory") {
		t.Fatalf("err = %v", err)
	}
	if n := count(f.calls(), "prompt:single"); n != 1 {
		t.Fatalf("queued %d renders; a failed decode must not re-run the song", n)
	}
}

func TestStagedDecodeOOMRetriesOnce(t *testing.T) {
	for _, fails := range []int{1, 2} {
		f, c := newFakeComfy(t)
		f.script = func(_, kind string, attempt int) outcome {
			if kind == "decode" && attempt < fails {
				return outcome{result: "oom"}
			}
			return outcome{result: "ok"}
		}
		b, _ := newTestBackend(c, config.LowMemoryOn)
		_, err := b.Generate(t.Context(), testRequest(t), nil)
		calls := f.calls()
		if n := count(calls, "prompt:decode"); n != 2 || count(calls, "prompt:sample") != 1 {
			t.Fatalf("%d failures: calls = %v", fails, calls)
		}
		first := indexOf(calls, "prompt:decode", 0)
		if indexOf(calls, "free", first) > indexOf(calls, "prompt:decode", first+1) {
			t.Fatalf("no free before the retry: %v", calls)
		}
		if fails == 1 && err != nil {
			t.Fatalf("retry failed: %v", err)
		}
		var se *StageError
		if fails == 2 && (!errors.As(err, &se) || se.Stage != stageDecode || !comfy.OutOfMemory(err, comfy.NodeDecode) || !strings.Contains(err.Error(), "stage 2 of 2 (decode)")) {
			t.Fatalf("err = %v", err)
		}
		if fileExists(f.outputPath(f.latentFile("p1"))) {
			t.Fatal("the latent outlived the render")
		}
	}
}

func TestStagedCancelInEachStage(t *testing.T) {
	for _, stage := range []string{"sample", "decode"} {
		f, c := newFakeComfy(t)
		f.script = func(_, kind string, _ int) outcome {
			if kind == stage {
				return outcome{events: []string{progressEvent(comfy.NodeSampler, 1, 30), progressEvent(comfy.NodeDecode, 1, 2)}, result: "hold"}
			}
			return outcome{result: "ok"}
		}
		b, _ := newTestBackend(c, config.LowMemoryOn)
		ctx, cancel := context.WithCancel(t.Context())
		_, err := b.Generate(ctx, testRequest(t), func(p Progress) {
			if p.Max > 0 {
				cancel()
			}
		})
		cancel()
		var se *StageError
		if !errors.Is(err, comfy.ErrInterrupted) || !errors.As(err, &se) {
			t.Fatalf("%s: err = %v", stage, err)
		}
		calls := f.calls()
		running := map[string]string{"sample": "p1", "decode": "p2"}[stage]
		if !slices.Contains(calls, "interrupt:"+running) || !slices.Contains(calls, "delete:"+running) {
			t.Fatalf("%s: calls = %v", stage, calls)
		}
		if stage == "sample" && (se.Stage != stageSample || slices.Contains(calls, "prompt:decode")) {
			t.Fatalf("cancel while sampling: %v, calls %v", err, calls)
		}
		if stage == "decode" && (se.Stage != stageDecode || fileExists(f.outputPath(f.latentFile("p1")))) {
			t.Fatalf("cancel while decoding: %v, latent kept %v", err, fileExists(f.outputPath(f.latentFile("p1"))))
		}
	}
}

func TestStagedResumeAfterDetachInSampling(t *testing.T) {
	f, c := newFakeComfy(t)
	f.script = func(_, kind string, _ int) outcome {
		if kind == "sample" {
			return outcome{events: []string{progressEvent(comfy.NodeSampler, 1, 30)}, result: "hold"}
		}
		return outcome{result: "ok"}
	}
	b, _ := newTestBackend(c, config.LowMemoryOn)
	ctx, cancel := context.WithCancelCause(t.Context())
	req := testRequest(t)
	var job string
	req.OnStarted = func(id string) { job = id }
	_, err := b.Generate(ctx, req, func(p Progress) {
		if p.Max > 0 {
			cancel(ErrDetach)
		}
	})
	if !errors.Is(err, ErrDetach) || job != "lowmem1:p1" || slices.ContainsFunc(f.calls(), func(s string) bool { return strings.HasPrefix(s, "interrupt") }) {
		t.Fatalf("detach: err %v, job %q, calls %v", err, job, f.calls())
	}

	f.finish("p1", "sample")
	req.ResumeID = job
	if _, err := b.Generate(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(); count(calls, "prompt:sample") != 1 || count(calls, "prompt:decode") != 1 {
		t.Fatalf("resume sampled again or did not decode: %v", calls)
	}
	if want := "lowmem2:p2:" + comfy.LatentRef(f.latentFile("p1")); job != want {
		t.Fatalf("job = %q, want %q", job, want)
	}
}

func TestStagedResumeInDecoding(t *testing.T) {
	f, c := newFakeComfy(t)
	b, _ := newTestBackend(c, config.LowMemoryOff)
	f.finish("s1", "sample")
	ref := comfy.LatentRef(f.latentFile("s1"))

	// Detached after the latent was saved, before the decode was queued.
	req := testRequest(t)
	req.ResumeID = "lowmem2::" + ref
	if _, err := b.Generate(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(); count(calls, "prompt:decode") != 1 || count(calls, "prompt:sample") != 0 {
		t.Fatalf("calls = %v", calls)
	}

	// Detached while decoding, which then finished.
	f.finish("s1", "sample")
	f.finish("d9", "decode")
	req.ResumeID = "lowmem2:d9:" + ref
	before := len(f.calls())
	if _, err := b.Generate(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls()[before:]; slices.ContainsFunc(calls, func(s string) bool { return strings.HasPrefix(s, "prompt:") }) {
		t.Fatalf("a finished decode was queued again: %v", calls)
	}
}

func TestStagedResumeSamplesAgainWhenTheLatentIsGone(t *testing.T) {
	f, c := newFakeComfy(t)
	b, _ := newTestBackend(c, config.LowMemoryOn)
	req := testRequest(t)
	req.ResumeID = "lowmem2::" + comfy.LatentRef(f.latentFile("s0"))
	if _, err := b.Generate(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(); count(calls, "prompt:sample") != 1 || count(calls, "prompt:decode") != 1 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestAbandonStagedJob(t *testing.T) {
	f, c := newFakeComfy(t)
	b, _ := newTestBackend(c, config.LowMemoryOn)
	f.finish("s1", "sample")
	latent := f.outputPath(f.latentFile("s1"))
	if err := b.Abandon(t.Context(), "lowmem2:p2:"+comfy.LatentRef(f.latentFile("s1"))); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(); !slices.Equal(calls[:2], []string{"interrupt:p2", "delete:p2"}) || fileExists(latent) {
		t.Fatalf("calls = %v, latent kept %v", calls, fileExists(latent))
	}

	f.finish("s3", "sample")
	latent = f.outputPath(f.latentFile("s3"))
	if err := b.Abandon(t.Context(), "lowmem1:s3"); err != nil || fileExists(latent) {
		t.Fatalf("stage 1: err %v, latent kept %v", err, fileExists(latent))
	}
}

func TestRemoveLatentStaysInItsFolder(t *testing.T) {
	f, c := newFakeComfy(t)
	b, _ := newTestBackend(c, config.LowMemoryOn)
	outside := filepath.Join(f.root, "output", "songsmith", "song_00001_.flac")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{
		"songsmith/song_00001_.flac [output]",
		"songsmith/latents/../song_00001_.flac [output]",
		"songsmith/latents/x.latent [input]",
		"other/x.latent [output]",
	} {
		if err := b.removeLatent(t.Context(), ref); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	if !fileExists(outside) {
		t.Fatal("removed a file outside songsmith's latent folder")
	}
}

func TestParseJob(t *testing.T) {
	for id, want := range map[string]stagedJob{
		"lowmem1:p1": {stage: 1, prompt: "p1"},
		"lowmem2::songsmith/latents/a.latent [output]": {stage: 2, latent: "songsmith/latents/a.latent [output]"},
		"lowmem2:p2:a:b.latent [output]":               {stage: 2, prompt: "p2", latent: "a:b.latent [output]"},
	} {
		got, ok := parseJob(id)
		if !ok || !reflect.DeepEqual(got, want) || got.id() != id {
			t.Errorf("%q = %+v (%v), id %q", id, got, ok, got.id())
		}
	}
	if _, ok := parseJob("3f2a-uuid"); ok {
		t.Error("a plain prompt ID parsed as a low-memory job")
	}
}
