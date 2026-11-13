// Package config loads songsmith settings from YAML, falling back to defaults
// that match a ComfyUI install with the MiniMax Music 3 model files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

// BaseURL is the server's address; an IPv6 host is bracketed.
func (c ComfyUI) BaseURL() string {
	return "http://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

type Ollama struct {
	URL         string  `yaml:"url"`
	Model       string  `yaml:"model"`
	Temperature float64 `yaml:"temperature"`
	// InstructionsFile replaces the built-in system prompt when set. Load
	// resolves a relative path against the config file's folder.
	InstructionsFile string `yaml:"instructions_file"`
}

type Render struct {
	Steps    int     `yaml:"steps"`
	CFGScale float64 `yaml:"cfg_scale"`
	TopK     int     `yaml:"top_k"`
	// DefaultSeconds is the length a new song starts with; Load turns a
	// non-positive value into the default.
	DefaultSeconds int    `yaml:"default_seconds"`
	Sampler        string `yaml:"sampler"`
	Scheduler      string `yaml:"scheduler"`
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
			Dir:            filepath.Join(home, ".local", "share", "songsmith", "ComfyUI"),
			Host:           "127.0.0.1",
			Port:           8188,
			DiffusionModel: "minimax_music3_dit_fp16.safetensors",
			TextEncoder:    "minimax_music3_text_encoder_pruned_bf16.safetensors",
			VAE:            "minimax_music3_dav.safetensors",
		},
		Ollama: Ollama{
			URL:         "http://127.0.0.1:11434",
			Model:       "qwen3.8:latest",
			Temperature: 0.8,
		},
		Render: Render{
			Steps:          30,
			CFGScale:       1.7,
			TopK:           50,
			DefaultSeconds: 120,
			Sampler:        "euler",
			Scheduler:      "simple",
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

// Path returns explicit, or DefaultPath when it is empty: the file a process
// started with --config, $SONGSMITH_CONFIG or neither reads and writes.
func Path(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return DefaultPath()
}

// Load reads the config at path (DefaultPath when empty). A missing file
// yields the defaults; keys present in the file override them.
func Load(path string) (Config, error) {
	path = Path(path)
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
	cfg.LibraryDir = ExpandHome(cfg.LibraryDir)
	cfg.StateDir = ExpandHome(cfg.StateDir)
	cfg.ComfyUI.Dir = ExpandHome(cfg.ComfyUI.Dir)
	cfg.Ollama.InstructionsFile = ExpandHome(cfg.Ollama.InstructionsFile)
	if f := cfg.Ollama.InstructionsFile; f != "" && !filepath.IsAbs(f) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return Config{}, fmt.Errorf("resolve config path %s: %w", path, err)
		}
		cfg.Ollama.InstructionsFile = filepath.Join(filepath.Dir(abs), f)
	}
	if cfg.Render.DefaultSeconds <= 0 {
		cfg.Render.DefaultSeconds = Default().Render.DefaultSeconds
	}
	return cfg, nil
}

// ExpandHome turns ~ and a path starting with ~/ into one under the home
// folder; any other path is returned as it is.
func ExpandHome(p string) string {
	rest, ok := strings.CutPrefix(p, "~/")
	if p == "~" {
		rest, ok = "", true
	}
	if !ok {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, rest)
}
