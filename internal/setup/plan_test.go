package setup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/config"
)

func planConfig(t *testing.T, comfyDir, ollamaURL string) config.Config {
	return config.Config{
		ComfyUI: config.ComfyUI{
			Dir:            comfyDir,
			Host:           "127.0.0.1",
			Port:           18188,
			DiffusionModel: "minimax_music3_dit_fp16.safetensors",
			TextEncoder:    "minimax_music3_text_encoder_pruned_bf16.safetensors",
			VAE:            "minimax_music3_dav.safetensors",
		},
		Ollama:     config.Ollama{URL: ollamaURL, Model: "writer-model"},
		LibraryDir: filepath.Join(t.TempDir(), "library"),
		StateDir:   filepath.Join(t.TempDir(), "state"),
	}
}

func fakeOllama(t *testing.T, tags string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, tags)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func byID(p Plan) map[ActionID]Action {
	m := map[ActionID]Action{}
	for _, a := range p.Actions {
		m[a.ID] = a
	}
	return m
}

func TestPlanNothingInstalled(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	in := fs.installer("darwin")
	dir := filepath.Join(t.TempDir(), "share", "songsmith", "ComfyUI")
	cfg := planConfig(t, dir, fakeOllama(t, `{"models":[{"name":"other:7b"}]}`))

	p := in.Plan(t.Context(), cfg)
	ids := []ActionID{}
	for _, a := range p.Actions {
		ids = append(ids, a.ID)
	}
	if len(ids) != 5 || ids[0] != ActionComfyUI || ids[4] != ActionWriter {
		t.Fatalf("order = %v", ids)
	}
	a := byID(p)
	if a[ActionComfyUI].Status != StatusMissing || !strings.Contains(a[ActionComfyUI].Detail, "uv") {
		t.Fatalf("comfyui = %+v", a[ActionComfyUI])
	}
	if a[ActionVAE].Status != StatusMissing || a[ActionVAE].DownloadBytes != 216696128 || !strings.Contains(a[ActionVAE].Detail, "after ComfyUI") {
		t.Fatalf("vae = %+v", a[ActionVAE])
	}
	w := a[ActionWriter]
	if w.Status != StatusMissing || w.Required || !strings.Contains(w.Detail, "1 other models") || !strings.Contains(w.Detail, "Settings → Writer") {
		t.Fatalf("writer = %+v", w)
	}
	if len(p.MissingRequired()) != 4 || p.Ready() {
		t.Fatalf("missing required = %d", len(p.MissingRequired()))
	}
	want := in.ComfyDiskEstimate() + 4914197682 + 16706629398 + 216696128
	if p.DownloadBytes() != want {
		t.Fatalf("download = %d, want %d", p.DownloadBytes(), want)
	}
}

func TestPlanPartialAndInstalled(t *testing.T) {
	fs := newFakeSystem(t)
	in := fs.installer("darwin")
	dir := t.TempDir()
	for _, p := range []string{"main.py", ".venv/bin/python", ".venv/lib/python3.12/site-packages/torch/x"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), nil, 0o644)
	}
	vae, _ := Lookup(DirVAE, "minimax_music3_dav.safetensors")
	os.MkdirAll(filepath.Join(dir, "models", "vae"), 0o755)
	os.WriteFile(PartPath(dir, vae), make([]byte, vae.Size/4), 0o644)
	cfg := planConfig(t, dir, fakeOllama(t, `{"models":[{"name":"writer-model:latest"}]}`))
	cfg.ComfyUI.DiffusionModel = "my_own_dit.safetensors"

	a := byID(in.Plan(t.Context(), cfg))
	if a[ActionComfyUI].Status != StatusDone {
		t.Fatalf("comfyui = %+v", a[ActionComfyUI])
	}
	v := a[ActionVAE]
	if v.Status != StatusPartial || v.Percent < 24.9 || v.Percent > 25.1 || v.DownloadBytes != vae.Size-vae.Size/4 {
		t.Fatalf("vae = %+v", v)
	}
	if d := a[ActionDiffusion]; d.Status != StatusUnavailable || !strings.Contains(d.Detail, "not a file in "+Repo) {
		t.Fatalf("diffusion = %+v", d)
	}
	if w := a[ActionWriter]; w.Status != StatusDone {
		t.Fatalf("untagged model should match :latest: %+v", w)
	}
}

func TestPlanBlockers(t *testing.T) {
	fs := newFakeSystem(t, "uv")
	in := fs.installer("linux")
	cfg := planConfig(t, filepath.Join(t.TempDir(), "ComfyUI"), "http://127.0.0.1:1")

	a := byID(in.Plan(t.Context(), cfg))
	if c := a[ActionComfyUI]; c.Status != StatusUnavailable || !strings.Contains(c.Detail, "git is not installed") {
		t.Fatalf("comfyui = %+v", c)
	}
	if v := a[ActionVAE]; v.Status != StatusUnavailable || !strings.Contains(v.Detail, "needs ComfyUI") {
		t.Fatalf("vae = %+v", v)
	}
	w := a[ActionWriter]
	if w.Status != StatusUnavailable || !strings.Contains(w.Detail, "ollama.com/download") || !strings.Contains(w.Detail, "written by hand") {
		t.Fatalf("writer = %+v", w)
	}
}

func TestPlanDiskSpace(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	in := fs.installer("darwin")
	in.FreeSpace = func(string) (uint64, error) { return 10e9, nil }
	cfg := planConfig(t, filepath.Join(t.TempDir(), "ComfyUI"), fakeOllama(t, `{"models":[]}`))

	a := byID(in.Plan(t.Context(), cfg))
	// ComfyUI (about 3 GB) and the 4.9 GB DiT fit in 10 GB; the 16.7 GB text encoder does not.
	if a[ActionComfyUI].Status != StatusMissing || a[ActionDiffusion].Status != StatusMissing {
		t.Fatalf("comfyui %v, diffusion %v", a[ActionComfyUI].Status, a[ActionDiffusion].Status)
	}
	if te := a[ActionTextEncoder]; te.Status != StatusUnavailable || !strings.Contains(te.Detail, "not enough disk space") {
		t.Fatalf("text encoder = %+v", te)
	}
}

func TestPlanLinuxMarkedUntested(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	cfg := planConfig(t, filepath.Join(t.TempDir(), "ComfyUI"), fakeOllama(t, `{"models":[]}`))
	a := byID(fs.installer("linux").Plan(t.Context(), cfg))
	if !strings.Contains(a[ActionComfyUI].Detail, "untested") {
		t.Fatalf("comfyui = %+v", a[ActionComfyUI])
	}
	if a[ActionComfyUI].DownloadBytes <= fs.installer("darwin").ComfyDiskEstimate() {
		t.Fatal("linux estimate should cover CUDA torch")
	}
}

func TestPlanOffline(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	fs.reachOK = false
	cfg := planConfig(t, filepath.Join(t.TempDir(), "ComfyUI"), fakeOllama(t, `{"models":[]}`))
	a := byID(fs.installer("darwin").Plan(t.Context(), cfg))
	if c := a[ActionComfyUI]; c.Status != StatusUnavailable || !strings.Contains(c.Detail, "cannot reach github.com") {
		t.Fatalf("comfyui = %+v", c)
	}
	if v := a[ActionVAE]; v.Status != StatusUnavailable || !strings.Contains(v.Detail, "cannot reach huggingface.co") {
		t.Fatalf("vae = %+v", v)
	}
}

func TestPlanInstalledNeverProbes(t *testing.T) {
	fs := newFakeSystem(t)
	in := fs.installer("darwin")
	in.Reach = func(context.Context, string) error { t.Error("probed the network"); return nil }
	dir := t.TempDir()
	for _, p := range []string{"main.py", ".venv/bin/python", ".venv/lib/python3.12/site-packages/torch/x"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), nil, 0o644)
	}
	cfg := planConfig(t, dir, fakeOllama(t, `{"models":[]}`))
	for _, s := range ModelSlots(cfg.ComfyUI) {
		f, _ := Lookup(s.Dir, s.Name)
		os.MkdirAll(filepath.Join(dir, "models", s.Dir), 0o755)
		fh, _ := os.Create(filePath(dir, f))
		fh.Truncate(f.Size)
		fh.Close()
	}
	if p := in.Plan(t.Context(), cfg); !p.Ready() {
		t.Fatalf("not ready: %+v", p.MissingRequired())
	}
}
