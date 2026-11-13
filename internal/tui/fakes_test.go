package tui

import (
	"context"
	"os"
	"sync"

	"github.com/lancekrogers/songsmith/internal/master"
)

// fakePlayer records what would play; no test plays audio.
type fakePlayer struct {
	mu      sync.Mutex
	current string
	played  []string
	done    chan string
}

func newFakePlayer() *fakePlayer { return &fakePlayer{done: make(chan string, 64)} }

func (p *fakePlayer) Play(path string) error {
	p.Stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = path
	p.played = append(p.played, path)
	return nil
}

func (p *fakePlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != "" {
		select {
		case p.done <- p.current:
		default:
		}
	}
	p.current = ""
}

func (p *fakePlayer) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func (p *fakePlayer) Finished() <-chan string { return p.done }

func (p *fakePlayer) plays() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.played...)
}

type previewCall struct {
	out      string
	settings master.Settings
	start    float64
}

// fakeMaster stands in for ffmpeg: previews and masters are small files.
type fakeMaster struct {
	mu          sync.Mutex
	unavailable error
	measure     master.Measure
	previews    []previewCall
	// block, when set, holds each Render until it is closed or the render
	// is cancelled; renderErr fails it.
	block     chan struct{}
	renderErr error
	renders   int
}

func (f *fakeMaster) Check() error { return f.unavailable }

func (f *fakeMaster) Analyze(context.Context, string) (master.Measure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.measure
	if m.Duration == 0 {
		m = master.Measure{Loudness: -17.2, TruePeak: -2.1, Range: 6.4, Hiss: -61, DC: 0.012, NoiseFloor: -72, Duration: 150,
			Info: master.Info{SampleRate: 44100, Channels: 2, Duration: 150}}
	}
	return m, nil
}

func (f *fakeMaster) Preview(ctx context.Context, in, out string, s master.Settings, start, seconds float64) (master.Measure, error) {
	f.mu.Lock()
	f.previews = append(f.previews, previewCall{out: out, settings: s, start: start})
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return master.Measure{}, err
	}
	if err := os.WriteFile(out, []byte("excerpt"), 0o644); err != nil {
		return master.Measure{}, err
	}
	m, _ := f.Analyze(ctx, in)
	m.Duration = seconds
	if s.Loudness != 0 {
		m.Loudness = s.Loudness
	}
	return m, nil
}

func (f *fakeMaster) Render(ctx context.Context, in, out string, s master.Settings, progress func(float64)) (master.Measure, error) {
	f.mu.Lock()
	f.renders++
	block, fail := f.block, f.renderErr
	f.mu.Unlock()
	progress(0.4)
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return master.Measure{}, ctx.Err()
		}
	}
	if fail != nil {
		return master.Measure{}, fail
	}
	progress(1)
	m, _ := f.Analyze(ctx, in)
	m.Loudness = s.Loudness
	return m, os.WriteFile(out, []byte("master"), 0o644)
}

func (f *fakeMaster) calls() []previewCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]previewCall(nil), f.previews...)
}
