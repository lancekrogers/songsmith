// Package player plays one audio file at a time with the platform's player.
package player

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
)

type Player struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	current string
	// Done receives the path of a file when its playback ends on its own or is stopped.
	Done chan string
}

func New() *Player {
	return &Player{Done: make(chan string, 4)}
}

func command(path string) (*exec.Cmd, error) {
	if runtime.GOOS == "darwin" {
		return exec.Command("afplay", path), nil
	}
	for _, name := range []string{"ffplay", "paplay", "aplay"} {
		if p, err := exec.LookPath(name); err == nil {
			if name == "ffplay" {
				return exec.Command(p, "-nodisp", "-autoexit", "-loglevel", "quiet", path), nil
			}
			return exec.Command(p, path), nil
		}
	}
	return nil, errors.New("no audio player found; install ffmpeg (ffplay)")
}

// Play stops anything playing and starts path.
func (p *Player) Play(path string) error {
	p.Stop()
	cmd, err := command(path)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("play %s: %w", path, err)
	}
	p.mu.Lock()
	p.cmd, p.current = cmd, path
	p.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd, p.current = nil, ""
		}
		p.mu.Unlock()
		select {
		case p.Done <- path:
		default:
		}
	}()
	return nil
}

// Stop ends playback, if any.
func (p *Player) Stop() {
	p.mu.Lock()
	cmd := p.cmd
	p.cmd, p.current = nil, ""
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Current returns the file being played, or "".
func (p *Player) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}
