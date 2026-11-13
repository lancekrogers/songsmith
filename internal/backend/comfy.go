package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
)

// Errors the queue reacts to, shared with the ComfyUI client.
var (
	// ErrDetach as a cancellation cause leaves the render running so it can be resumed.
	ErrDetach = comfy.ErrDetach
	// ErrJobLost means the backend no longer knows a resumed job.
	ErrJobLost = comfy.ErrPromptLost
)

// Starter makes sure a ComfyUI server is reachable before a render.
type Starter interface {
	Start(ctx context.Context) error
}

// ComfyMiniMax renders songs with MiniMax Music 3 through ComfyUI.
type ComfyMiniMax struct {
	client  *comfy.Client
	starter Starter
	// settings returns the current model files and render settings; each
	// render reads them once when it starts.
	settings func() config.Config
	now      func() time.Time
	// settle is how long a low-memory render waits after asking the server
	// to free memory before it queues the next prompt.
	settle time.Duration
}

func NewComfyMiniMax(client *comfy.Client, starter Starter, settings func() config.Config) *ComfyMiniMax {
	return &ComfyMiniMax{client: client, starter: starter, settings: settings, now: time.Now, settle: time.Second}
}

func (b *ComfyMiniMax) Name() string { return "minimax-music3 (comfyui)" }

func (b *ComfyMiniMax) Abandon(ctx context.Context, jobID string) error {
	if job, ok := parseJob(jobID); ok {
		return b.abandonStaged(ctx, job)
	}
	return errors.Join(b.client.Interrupt(ctx, jobID), b.client.DeleteQueued(ctx, jobID))
}

var nodeStage = map[string]Stage{
	comfy.NodeTextEncoder: StageLoading,
	comfy.NodeDiffusion:   StageLoading,
	comfy.NodeVAE:         StageLoading,
	comfy.NodeEncode:      StageComposing,
	comfy.NodeNegative:    StageRendering,
	comfy.NodeLatent:      StageRendering,
	comfy.NodeSampler:     StageRendering,
	comfy.NodeDecode:      StageDecoding,
	comfy.NodeSave:        StageSaving,
}

func (b *ComfyMiniMax) Generate(ctx context.Context, req Request, progress func(Progress)) (Result, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if b.starter != nil {
		if err := b.starter.Start(ctx); err != nil {
			return Result{}, fmt.Errorf("start comfyui: %w", err)
		}
	}
	cfg := b.settings()
	p := comfy.MiniMaxParams{
		Caption:        req.Caption,
		Lyrics:         req.Lyrics,
		Seed:           req.Seed,
		MaxSeconds:     req.MaxSeconds,
		CFGScale:       cfg.Render.CFGScale,
		TopK:           cfg.Render.TopK,
		Steps:          cfg.Render.Steps,
		Sampler:        cfg.Render.Sampler,
		Scheduler:      cfg.Render.Scheduler,
		DiffusionModel: cfg.ComfyUI.DiffusionModel,
		TextEncoder:    cfg.ComfyUI.TextEncoder,
		VAE:            cfg.ComfyUI.VAE,
		FilenamePrefix: "songsmith/" + req.ID,
	}
	clk := &stageClock{now: b.now, timings: map[Stage]time.Duration{}}

	// The mode is decided once, when the render starts. A resumed render
	// keeps the mode it started with, unless the server has forgotten it.
	if job, ok := parseJob(req.ResumeID); ok {
		return b.staged(ctx, req, p, job, progress, clk)
	}
	if req.ResumeID != "" && cfg.Render.LowMemory != config.LowMemoryOff && !b.knows(ctx, req.ResumeID) {
		req.ResumeID = ""
	}
	why := ""
	if req.ResumeID == "" {
		var on bool
		if on, why = b.lowMemory(ctx, cfg.Render.LowMemory); on {
			return b.staged(ctx, req, p, stagedJob{stage: 1}, progress, clk)
		}
	}

	h, err := b.client.Run(ctx, comfy.MiniMaxGraph(p), comfy.RunOptions{ResumeID: req.ResumeID, OnQueued: req.OnStarted, OnEvent: func(e comfy.Event) {
		clk.event(e, nodeStage, progress)
	}})
	clk.enter(StageSaving)
	if err != nil {
		if comfy.OutOfMemory(err, comfy.NodeDecode) {
			err = &DecodeOOMError{Err: err, LowMemory: why}
		}
		return Result{Timings: clk.timings}, err
	}

	files := h.AudioFiles()
	if len(files) == 0 {
		return Result{Timings: clk.timings}, errors.New("comfyui finished without an audio output")
	}
	if err := b.download(ctx, files[0], req.OutPath); err != nil {
		return Result{Timings: clk.timings}, err
	}
	clk.enter("")
	return Result{AudioPath: req.OutPath, Timings: clk.timings}, nil
}

// DecodeOOMError is a one-prompt render that ran out of memory decoding. The
// sampled song is lost with the prompt, so it is not re-run: a long render
// would most likely fail the same way.
type DecodeOOMError struct {
	Err error
	// LowMemory is how low-memory rendering was resolved for the render.
	LowMemory string
}

func (e *DecodeOOMError) Error() string {
	msg := strings.TrimRight(e.Err.Error(), ". \n") + ". The song was rendered but ran out of memory while decoding; turn on low-memory rendering " +
		"(Settings → Render → Low-memory rendering, or render.low_memory: on in the config) and render it again"
	if e.LowMemory != "" {
		msg += " (low-memory rendering was " + e.LowMemory + ")"
	}
	return msg
}

func (e *DecodeOOMError) Unwrap() error { return e.Err }

// knows reports whether the server still has promptID, queued or finished.
// When it cannot tell, it assumes it does.
func (b *ComfyMiniMax) knows(ctx context.Context, promptID string) bool {
	if _, ok, err := b.client.History(ctx, promptID); err != nil || ok {
		return true
	}
	known, err := b.client.QueueContains(ctx, promptID)
	return err != nil || known
}

// stageClock adds up the time spent in each stage of a render.
type stageClock struct {
	now     func() time.Time
	timings map[Stage]time.Duration
	cur     Stage
	since   time.Time
}

func (c *stageClock) enter(s Stage) {
	if s == c.cur {
		return
	}
	now := c.now()
	if c.cur != "" {
		c.timings[c.cur] += now.Sub(c.since)
	}
	c.cur, c.since = s, now
}

// event moves the clock to the stage of e's node and reports progress.
func (c *stageClock) event(e comfy.Event, stages map[string]Stage, progress func(Progress)) {
	stage, ok := stages[e.Node]
	if !ok {
		return
	}
	switch e.Type {
	case comfy.EventExecuting:
		c.enter(stage)
		progress(Progress{Stage: stage})
	case comfy.EventProgress:
		c.enter(stage)
		progress(Progress{Stage: stage, Value: e.Value, Max: e.Max})
	}
}

func (b *ComfyMiniMax) download(ctx context.Context, f comfy.OutputFile, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".audio-*")
	if err != nil {
		return fmt.Errorf("create temp audio file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := b.client.Download(ctx, f, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write audio: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("save audio: %w", err)
	}
	return nil
}
