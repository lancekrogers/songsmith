package machines

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Speed is what a machine's finished renders took.
type Speed struct {
	RenderSeconds float64 `json:"render_seconds"`
	AudioSeconds  float64 `json:"audio_seconds"`
	Renders       int     `json:"renders"`
}

// PerAudioSecond is the seconds the machine took per second of audio.
func (s Speed) PerAudioSecond() float64 {
	if s.AudioSeconds <= 0 {
		return 0
	}
	return s.RenderSeconds / s.AudioSeconds
}

// Speeds keeps each machine's measured render speed in machines.json in
// the state folder.
type Speeds struct {
	path string

	mu     sync.Mutex
	loaded bool
	byName map[string]Speed
}

func NewSpeeds(stateDir string) *Speeds {
	return &Speeds{path: filepath.Join(stateDir, "machines.json")}
}

// load reads the file once. Call with s.mu held.
func (s *Speeds) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byName = map[string]Speed{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file struct {
		Speeds map[string]Speed `json:"speeds"`
	}
	if json.Unmarshal(data, &file) == nil && file.Speeds != nil {
		s.byName = file.Speeds
	}
}

// Speed returns the seconds machine took per second of audio, and false
// before it has finished a render.
func (s *Speeds) Speed(machine string) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	sp, ok := s.byName[machine]
	if !ok || sp.PerAudioSecond() <= 0 {
		return 0, false
	}
	return sp.PerAudioSecond(), true
}

// All returns every machine's speed.
func (s *Speeds) All() map[string]Speed {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return maps.Clone(s.byName)
}

// Record adds a finished render: how long it took and how much audio it made.
func (s *Speeds) Record(machine string, took time.Duration, audioSeconds float64) error {
	if took <= 0 || audioSeconds <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	sp := s.byName[machine]
	sp.RenderSeconds += took.Seconds()
	sp.AudioSeconds += audioSeconds
	sp.Renders++
	s.byName[machine] = sp
	return s.save()
}

func (s *Speeds) save() error {
	data, err := json.MarshalIndent(map[string]any{"speeds": s.byName}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode machine speeds: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("save machine speeds: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".machines-*.json")
	if err != nil {
		return fmt.Errorf("save machine speeds: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("save machine speeds: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save machine speeds: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("save machine speeds: %w", err)
	}
	return nil
}
