package machines

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
)

func TestCheckReportsWhatTheMachineHas(t *testing.T) {
	models := config.Default().ComfyUI
	srv := comfytest.New(t, "mbp")
	srv.SetMemory(32 * comfytest.GiB)
	srv.SetModels(models.DiffusionModel, models.TextEncoder, models.VAE)
	st := Check(t.Context(), srv.URL, models)
	if st.State != Ready || st.Memory != 32*comfytest.GiB || st.Device != "mps" || !strings.Contains(st.Detail, "32 GB") {
		t.Fatalf("ready: %+v", st)
	}

	srv.SetModels(models.DiffusionModel, models.TextEncoder)
	if st := Check(t.Context(), srv.URL, models); st.State != ModelsMissing || !strings.Contains(st.Detail, models.VAE) {
		t.Fatalf("missing vae: %+v", st)
	}
	other := models
	other.VAE = "other_vae.safetensors"
	srv.SetModels(models.DiffusionModel, models.TextEncoder, other.VAE)
	if st := Check(t.Context(), srv.URL, other); st.State != Ready {
		t.Fatalf("the machine's own file names: %+v", st)
	}

	srv.SetMissingNode("VAEDecodeAudioTiled")
	if st := Check(t.Context(), srv.URL, other); st.State != TooOld || !strings.Contains(st.Detail, "VAEDecodeAudioTiled") {
		t.Fatalf("old comfyui: %+v", st)
	}

	srv.SetDown(true)
	if st := Check(t.Context(), srv.URL, models); st.State != Offline || !strings.Contains(st.Detail, "not answering at "+srv.URL) {
		t.Fatalf("down: %+v", st)
	}
	srv.Close()
	if st := Check(t.Context(), srv.URL, models); st.State != Offline || !strings.Contains(st.Detail, "awake") {
		t.Fatalf("closed: %+v", st)
	}
	if err := Ping(t.Context(), srv.URL); !errors.Is(err, comfy.ErrUnreachable) {
		t.Fatalf("ping a closed server: %v", err)
	}
}

func TestBackendRendersWithTheMachinesOwnModelFiles(t *testing.T) {
	srv := comfytest.New(t, "mbp")
	cfg := config.Default()
	cfg.Machines = []config.Machine{{Name: "MacBook Pro", URL: srv.URL, Enabled: true, VAE: "mbp_vae.safetensors"}}
	b := Backend("MacBook Pro", srv.URL, func() config.Config { return cfg })
	out := filepath.Join(t.TempDir(), "song.flac")
	if _, err := b.Generate(t.Context(), backend.Request{ID: "s", Caption: "pop", Lyrics: "[Verse]\nla", Seed: 1, MaxSeconds: 30, OutPath: out}, nil); err != nil {
		t.Fatal(err)
	}
	g := srv.Graph(srv.Prompts()[0])
	if g[comfy.NodeVAE].Inputs["vae_name"] != "mbp_vae.safetensors" || g[comfy.NodeDiffusion].Inputs["unet_name"] != cfg.ComfyUI.DiffusionModel {
		t.Fatalf("loaders %v %v", g[comfy.NodeVAE].Inputs, g[comfy.NodeDiffusion].Inputs)
	}
	if data, _ := os.ReadFile(out); string(data) != "audio from mbp" {
		t.Fatalf("audio %q", data)
	}
}

func TestSpeedsAreKeptPerMachineAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	s := NewSpeeds(dir)
	if _, ok := s.Speed("MacBook Pro"); ok {
		t.Fatal("a machine with no renders has a speed")
	}
	if err := s.Record("MacBook Pro", 300*time.Second, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("MacBook Pro", 100*time.Second, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(config.ThisComputer, 90*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	again := NewSpeeds(dir)
	if sp, ok := again.Speed("MacBook Pro"); !ok || sp != 2 {
		t.Fatalf("speed %v %v", sp, ok)
	}
	if _, ok := again.Speed(config.ThisComputer); ok {
		t.Fatal("a render with no audio length was measured")
	}
	if all := again.All(); all["MacBook Pro"].Renders != 2 {
		t.Fatalf("all %+v", all)
	}
}
