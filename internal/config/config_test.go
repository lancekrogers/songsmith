package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("missing file gives defaults", func(t *testing.T) {
		cfg, err := Load(filepath.Join(dir, "nope.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg != Default() {
			t.Fatalf("got %+v, want defaults", cfg)
		}
	})

	t.Run("partial override keeps other defaults", func(t *testing.T) {
		p := write("partial.yaml", "ollama:\n  model: gemma4:26b\ncomfyui:\n  port: 9000\nlibrary_dir: ~/songs\n")
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Ollama.Model != "gemma4:26b" || cfg.ComfyUI.Port != 9000 {
			t.Fatalf("overrides not applied: %+v", cfg)
		}
		if cfg.Ollama.URL != Default().Ollama.URL || cfg.ComfyUI.TextEncoder != Default().ComfyUI.TextEncoder {
			t.Fatalf("defaults lost: %+v", cfg)
		}
		home, _ := os.UserHomeDir()
		if cfg.LibraryDir != filepath.Join(home, "songs") {
			t.Fatalf("home not expanded: %s", cfg.LibraryDir)
		}
	})

	t.Run("invalid yaml is an error", func(t *testing.T) {
		p := write("bad.yaml", "comfyui: [unclosed\n")
		if _, err := Load(p); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("env var picks the path", func(t *testing.T) {
		p := write("env.yaml", "render:\n  steps: 12\n")
		t.Setenv("SONGSMITH_CONFIG", p)
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Render.Steps != 12 {
			t.Fatalf("steps = %d", cfg.Render.Steps)
		}
	})
}
