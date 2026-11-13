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
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/songsmith/internal/master"
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
	// Context is the num_ctx drafts ask for; 0 leaves it to Ollama.
	Context int `yaml:"context"`
	// KeepAlive is how long Ollama keeps the writer loaded after a draft,
	// in Ollama's duration syntax ("5m", "0", "-1").
	KeepAlive string `yaml:"keep_alive"`
	// UnloadBeforeRender asks Ollama to unload the writer when a song
	// starts rendering, so the music model has the memory.
	UnloadBeforeRender bool `yaml:"unload_before_render"`
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
	// LowMemory renders in two prompts, freeing the models between them.
	LowMemory LowMemoryMode `yaml:"low_memory"`
}

// LowMemoryMode is auto, on or off; auto decides at the start of each
// render from the ComfyUI server's memory.
type LowMemoryMode string

const (
	LowMemoryAuto LowMemoryMode = "auto"
	LowMemoryOn   LowMemoryMode = "on"
	LowMemoryOff  LowMemoryMode = "off"
)

// LowMemoryModes are the values render.low_memory takes.
var LowMemoryModes = []LowMemoryMode{LowMemoryAuto, LowMemoryOn, LowMemoryOff}

func (m *LowMemoryMode) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if !slices.Contains(LowMemoryModes, LowMemoryMode(s)) {
		return fmt.Errorf("render.low_memory is %q; use auto, on or off", s)
	}
	*m = LowMemoryMode(s)
	return nil
}

// Master is the mastering chain the user saved and whether new renders
// are mastered with it.
type Master struct {
	// Auto masters each new render with Default, keeps the raw render as
	// its own take and makes the master current.
	Auto    bool            `yaml:"auto"`
	Default master.Settings `yaml:"default"`
}

type Config struct {
	ComfyUI    ComfyUI `yaml:"comfyui"`
	Ollama     Ollama  `yaml:"ollama"`
	Render     Render  `yaml:"render"`
	Master     Master  `yaml:"master"`
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
			URL:                "http://127.0.0.1:11434",
			Model:              "qwen3.8:latest",
			Temperature:        0.8,
			KeepAlive:          "5m",
			UnloadBeforeRender: true,
		},
		Render: Render{
			Steps:          30,
			CFGScale:       1.7,
			TopK:           50,
			DefaultSeconds: 120,
			Sampler:        "euler",
			Scheduler:      "simple",
			LowMemory:      LowMemoryAuto,
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
