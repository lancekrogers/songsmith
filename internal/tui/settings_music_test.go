package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
)

// The Comfy-Org/MiniMax-Music-3 tree as its API listed it on 2026-11-12.
var musicRepoTree = map[string]int64{
	"diffusion_models/minimax_music3_dit_fp16.safetensors":                      4914197682,
	"diffusion_models/minimax_music3_dit_fp32.safetensors":                      9828345396,
	"diffusion_models/minimax_music3_dit_int8_convrot.safetensors":              2502161682,
	"text_encoders/minimax_music3_text_encoder_bf16.safetensors":                18472478038,
	"text_encoders/minimax_music3_text_encoder_pruned_bf16.safetensors":         16706629398,
	"text_encoders/minimax_music3_text_encoder_pruned_int8_convrot.safetensors": 9196611886,
	"vae/minimax_music3_dav.safetensors":                                        216696128,
}

func TestBuiltinVariantsMatchTheModelRepository(t *testing.T) {
	got := map[string]int64{}
	for slot, vs := range builtinVariants {
		for _, v := range vs {
			got[slot+"/"+v.File] = v.Size
		}
	}
	if len(got) != len(musicRepoTree) {
		t.Fatalf("%d variants, the repository has %d files", len(got), len(musicRepoTree))
	}
	for path, size := range musicRepoTree {
		if got[path] != size {
			t.Errorf("%s: size %d, repository %d", path, got[path], size)
		}
	}
	d := config.Default().ComfyUI
	for slot, file := range map[string]string{SlotDiffusion: d.DiffusionModel, SlotTextEncoder: d.TextEncoder, SlotVAE: d.VAE} {
		if _, ok := musicRepoTree[slot+"/"+file]; !ok {
			t.Errorf("default %s is not in the repository", file)
		}
	}
}

// sparseFile makes a file of size bytes without writing them.
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestLocalVariantsAreInstalledOnlyAtFullSize(t *testing.T) {
	cfg := config.Default()
	cfg.ComfyUI.Dir = t.TempDir()
	sparseFile(t, filepath.Join(cfg.ComfyUI.Dir, "models", "vae", "minimax_music3_dav.safetensors"), 216696128)
	sparseFile(t, filepath.Join(cfg.ComfyUI.Dir, "models", "diffusion_models", "minimax_music3_dit_int8_convrot.safetensors"), 1000)
	if vae := localVariants(cfg, SlotVAE); !vae[0].Installed {
		t.Fatal("full-size VAE not installed")
	}
	for _, v := range localVariants(cfg, SlotDiffusion) {
		if v.Installed {
			t.Fatalf("%s installed (missing or partial)", v.File)
		}
	}
}

// someInstalled marks the pruned text encoders and the fp16 diffusion model
// as on disk.
func someInstalled(_ config.Config, slot string) []ModelVariant {
	vs := slices.Clone(builtinVariants[slot])
	for i := range vs {
		vs[i].Installed = strings.Contains(vs[i].File, "pruned") || strings.Contains(vs[i].File, "fp16") || slot == SlotVAE
	}
	return vs
}

func TestMusicModelListsVariantsAndSavesOnlyInstalledOnes(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a.d.Variants = someInstalled
	a = openSettingsOn(t, a, sectionMusicModel)
	view := plain(a)
	for _, s := range []string{
		"Text encoder",
		"▸ ✓ pruned bf16 · 16.7 GB · installed · recommended",
		"pruned int8 · 9.2 GB · installed · for NVIDIA 16 GB",
		"full bf16 · 18.5 GB · not on disk",
		"Diffusion model",
		"✓ fp16 · 4.9 GB · installed · recommended",
		"fp32 · 9.8 GB · not on disk",
		"VAE           dav · 0.2 GB · installed",
	} {
		if !strings.Contains(view, s) {
			t.Fatalf("Music model missing %q:\n%s", s, view)
		}
	}
	saves := recordSaves(&a)
	a = focus(t, a, "Diffusion model", 1)
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || a.cfg().ComfyUI.DiffusionModel != config.Default().ComfyUI.DiffusionModel ||
		!strings.Contains(plain(a), "minimax_music3_dit_fp32.safetensors is not on disk (9.8 GB)") {
		t.Fatalf("dim variant: saves %v\n%s", *saves, plain(a))
	}
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 24})
	a = focus(t, a, "Text encoder", 1)
	status := plain(a)
	for _, s := range []string{"NVIDIA cards with 16 GB", "untested on NVIDIA", "On Apple Silicon it ran on the CPU", "about 50x slower"} {
		if !strings.Contains(strings.ReplaceAll(status, "\n  ", " "), s) {
			t.Errorf("int8 note missing %q:\n%s", s, status)
		}
	}
	a = pressSettings(t, a, "enter")
	want := savedKV{"comfyui.text_encoder", "minimax_music3_text_encoder_pruned_int8_convrot.safetensors"}
	if len(*saves) != 1 || (*saves)[0] != want || a.cfg().ComfyUI.TextEncoder != want.value {
		t.Fatalf("saves %v", *saves)
	}
	if !strings.Contains(plain(a), "Applies to the next render · Text encoder:") || !strings.Contains(plain(a), "✓ pruned int8") {
		t.Fatalf("after choosing:\n%s", plain(a))
	}
}

func TestMusicModelShowsACustomFileAsCurrent(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	if err := a.d.Config.Set("comfyui.diffusion_model", "my_finetune.safetensors"); err != nil {
		t.Fatal(err)
	}
	a = openSettingsOn(t, a, sectionMusicModel)
	if !strings.Contains(plain(a), "✓ my_finetune.safetensors · custom") {
		t.Fatalf("custom file:\n%s", plain(a))
	}
}

// objectInfoServer is a ComfyUI that answers object_info for KSampler.
func objectInfoServer(t *testing.T) func(context.Context, string) (comfy.NodeInfo, bool, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object_info/KSampler" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"KSampler":{"input":{"required":{
			"steps":["INT",{"default":20,"min":1,"max":10000}],
			"sampler_name":[["euler","euler_ancestral","dpmpp_2m"]],
			"scheduler":[["simple","karras","normal"]]}}}}`))
	}))
	t.Cleanup(srv.Close)
	return comfy.NewClient(srv.URL).ObjectInfo
}

func TestRenderListsSamplersFromTheRunningServer(t *testing.T) {
	a, srv := newSettingsApp(t, readyChecks())
	srv.objectInfo = objectInfoServer(t)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 100, Height: 40})
	a = openSettingsOn(t, a, sectionRender)
	view := plain(a)
	for _, s := range []string{"Sampler\n", "✓ euler", "euler_ancestral", "dpmpp_2m", "Scheduler", "✓ simple", "karras"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Render missing %q:\n%s", s, view)
		}
	}
	saves := recordSaves(&a)
	a = focus(t, a, "Scheduler", 1)
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"render.scheduler", "karras"}) {
		t.Fatalf("saves %v", *saves)
	}

	if err := a.d.Config.Set("render.sampler", "not_here"); err != nil {
		t.Fatal(err)
	}
	a = focus(t, a, "Sampler", 3)
	if row, _, _ := a.focusedSetting(); row.options[3] != "not_here · not on this server" || !row.muted[3] || row.current != 3 {
		t.Fatalf("sampler row %+v", row)
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 {
		t.Fatalf("choosing an unknown sampler saved %v", *saves)
	}
}

func TestRenderWithoutAServerSaysToStartIt(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 140, Height: 30})
	a, cmd := send(t, a, switchScreenMsg(screenSettings))
	a, load := send(t, a, settingsKey("4"))
	if !strings.Contains(plain(a), "asking the ComfyUI server…") {
		t.Fatalf("no loading line:\n%s", plain(a))
	}
	a = settle(t, a, tea.Batch(cmd, load))
	view := plain(a)
	if !strings.Contains(view, "Sampler, scheduler  euler, simple  start the server in Settings → Music engine to list the choices") || strings.Contains(view, "Scheduler") {
		t.Fatalf("no-server line:\n%s", view)
	}
	a, _ = send(t, a, samplersLoadedMsg{seq: a.settings.samplers.seq - 1, samplers: []string{"stale"}})
	if strings.Contains(plain(a), "stale") {
		t.Fatal("stale sampler list shown")
	}
}

func TestRenderSlidersClampAtTheirBounds(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	saves := recordSaves(&a)
	press := func(name, k string, n int) {
		a = focus(t, a, name, -1)
		for range n {
			a = pressSettings(t, a, k)
		}
	}
	press("Steps", "+", 40)
	press("CFG scale", "-", 20)
	press("Top-k", "+", 50)
	r := a.cfg().Render
	if r.Steps != 60 || r.CFGScale != 1.0 || r.TopK != 200 {
		t.Fatalf("upper/lower bounds: %+v", r)
	}
	press("Steps", "-", 60)
	press("CFG scale", "+", 40)
	press("Top-k", "-", 1)
	r = a.cfg().Render
	if r.Steps != 10 || r.CFGScale != 4.0 || r.TopK != 195 {
		t.Fatalf("other bounds: %+v", r)
	}
	for _, s := range *saves {
		if !slices.Contains([]string{"render.steps", "render.cfg_scale", "render.top_k"}, s.key) {
			t.Fatalf("saved %v", s)
		}
	}
	data, _ := os.ReadFile(a.d.Config.Path())
	for _, s := range []string{"steps: 10\n", "cfg_scale: 4\n", "top_k: 195\n"} {
		if !strings.Contains(string(data), s) {
			t.Fatalf("config file lacks %q:\n%s", s, data)
		}
	}
}
