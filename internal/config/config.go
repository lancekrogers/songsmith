// Package config loads songsmith settings from YAML, falling back to defaults
// that match a ComfyUI install with the MiniMax Music 3 model files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type ComfyUI struct {
	Dir            string `yaml:"dir"`
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	DiffusionModel string `yaml:"diffusion_model"`
	TextEncoder    string `yaml:"text_encoder"`
	VAE            string `yaml:"vae"`
}

func (c ComfyUI) BaseURL() string {
	return fmt.Sprintf("http://%s:%d", c.Host, c.Port)
}

type Ollama struct {
	URL   string `yaml:"url"`
	Model string `yaml:"model"`
}

type Render struct {
	Steps    int     `yaml:"steps"`
	CFGScale float64 `yaml:"cfg_scale"`
	TopK     int     `yaml:"top_k"`
}

type Config struct {
	ComfyUI    ComfyUI `yaml:"comfyui"`
	Ollama     Ollama  `yaml:"ollama"`
	Render     Render  `yaml:"render"`
	LibraryDir string  `yaml:"library_dir"`
	StateDir   string  `yaml:"state_dir"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		ComfyUI: ComfyUI{
			Dir:            "/Volumes/fast/ComfyUI",
			Host:           "127.0.0.1",
			Port:           8188,
			DiffusionModel: "minimax_music3_dit_fp16.safetensors",
			TextEncoder:    "minimax_music3_text_encoder_pruned_bf16.safetensors",
			VAE:            "minimax_music3_dav.safetensors",
		},
		Ollama: Ollama{
			URL:   "http://127.0.0.1:11434",
			Model: "qwen3.8:latest",
		},
		Render: Render{
			Steps:    30,
			CFGScale: 1.7,
			TopK:     50,
		},
		LibraryDir: filepath.Join(home, "Music", "Songsmith"),
		StateDir:   filepath.Join(home, ".local", "state", "songsmith"),
	}
}

// DefaultPath returns $SONGSMITH_CONFIG or ~/.config/songsmith/config.yaml.
func DefaultPath() string {
	if p := os.Getenv("SONGSMITH_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "songsmith", "config.yaml")
}

// Load reads the config at path (DefaultPath when empty). A missing file
// yields the defaults; keys present in the file override them.
func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.LibraryDir = expandHome(cfg.LibraryDir)
	cfg.StateDir = expandHome(cfg.StateDir)
	cfg.ComfyUI.Dir = expandHome(cfg.ComfyUI.Dir)
	return cfg, nil
}

func expandHome(p string) string {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, rest)
}
