package config

import "sync"

// Live holds the settings a running process uses and the file they came
// from. Renders and drafts call Get when they start, so a change applies to
// the next one without a restart.
//
// The folders, host and port are fixed for the process lifetime: the library,
// its render lock and the ComfyUI client are built from them once.
type Live struct {
	path string

	mu    sync.RWMutex
	cfg   Config
	fixed Config
}

func NewLive(path string, cfg Config) *Live {
	return &Live{path: Path(path), cfg: cfg, fixed: cfg}
}

// Path is the config file this process reads and writes.
func (l *Live) Path() string { return l.path }

func (l *Live) Get() Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg
}

// Replace swaps in cfg, keeping the fixed settings from the start.
func (l *Live) Replace(cfg Config) {
	cfg.LibraryDir = l.fixed.LibraryDir
	cfg.StateDir = l.fixed.StateDir
	cfg.ComfyUI.Dir = l.fixed.ComfyUI.Dir
	cfg.ComfyUI.Host = l.fixed.ComfyUI.Host
	cfg.ComfyUI.Port = l.fixed.ComfyUI.Port
	l.mu.Lock()
	l.cfg = cfg
	l.mu.Unlock()
}

// Set writes key to the config file and, only if that worked, reloads the
// file into the running settings. A fixed setting is saved for the next start.
func (l *Live) Set(key string, value any) error {
	if err := Set(l.path, key, value); err != nil {
		return err
	}
	cfg, err := Load(l.path)
	if err != nil {
		return err
	}
	l.Replace(cfg)
	return nil
}
