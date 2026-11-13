package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
)

// TestHardwareRender renders a short song on the ComfyUI install named by the
// config ($SONGSMITH_CONFIG, else ~/.config/songsmith/config.yaml).
// Run with SONGSMITH_HARDWARE=1 (just test hardware).
func TestHardwareRender(t *testing.T) {
	if os.Getenv("SONGSMITH_HARDWARE") == "" {
		t.Skip("set SONGSMITH_HARDWARE=1 to render on the real ComfyUI install")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ComfyUI.Dir, "main.py")); err != nil {
		t.Fatalf("no ComfyUI in %s: set comfyui.dir in %s (or point SONGSMITH_CONFIG at a config that does)", cfg.ComfyUI.Dir, config.Path(""))
	}
	m := server.New(cfg.ComfyUI, cfg.StateDir)
	b := NewComfyMiniMax(m.Client(), starterFunc(m.Ensure), func() config.Config { return cfg })

	out := filepath.Join(t.TempDir(), "smoke.flac")
	last := map[Stage]int{}
	start := time.Now()
	res, err := b.Generate(t.Context(), Request{
		ID:         "hardware-smoke",
		Caption:    "Global Metadata: Upbeat synth pop, 118 BPM, A minor, bright and punchy.\nVocal Details: Confident female lead.\nArrangement: Analog synth bass, gated drums, shimmering pads.",
		Lyrics:     "[Verse]\nNeon on the water\nCity in my chest\n[Chorus]\nWe light it up tonight",
		Seed:       42,
		MaxSeconds: 20,
		OutPath:    out,
	}, func(p Progress) {
		if p.Max > 0 && p.Value-last[p.Stage] < p.Max/5 && p.Value != p.Max {
			return
		}
		last[p.Stage] = p.Value
		t.Logf("%6.1fs %-16s %d/%d", time.Since(start).Seconds(), p.Stage, p.Value, p.Max)
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(res.AudioPath)
	if err != nil || fi.Size() < 100_000 {
		t.Fatalf("audio file %s: %v (size %d)", res.AudioPath, err, fi.Size())
	}
	for s, d := range res.Timings {
		t.Logf("timing %-16s %s", s, d.Round(time.Second))
	}
	t.Logf("total %s, %d bytes", time.Since(start).Round(time.Second), fi.Size())
}
