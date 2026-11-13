package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	models  config.ComfyUI
	render  config.Render
	now     func() time.Time
}

func NewComfyMiniMax(client *comfy.Client, starter Starter, models config.ComfyUI, render config.Render) *ComfyMiniMax {
	return &ComfyMiniMax{client: client, starter: starter, models: models, render: render, now: time.Now}
}

func (b *ComfyMiniMax) Name() string { return "minimax-music3 (comfyui)" }

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
	g := comfy.MiniMaxGraph(comfy.MiniMaxParams{
		Caption:        req.Caption,
		Lyrics:         req.Lyrics,
		Seed:           req.Seed,
		MaxSeconds:     req.MaxSeconds,
		CFGScale:       b.render.CFGScale,
		TopK:           b.render.TopK,
		Steps:          b.render.Steps,
		DiffusionModel: b.models.DiffusionModel,
		TextEncoder:    b.models.TextEncoder,
		VAE:            b.models.VAE,
		FilenamePrefix: "songsmith/" + req.ID,
	})

	timings := map[Stage]time.Duration{}
	var cur Stage
	var since time.Time
	enter := func(s Stage) {
		if s == cur {
			return
		}
		now := b.now()
		if cur != "" {
			timings[cur] += now.Sub(since)
		}
		cur, since = s, now
	}

	h, err := b.client.Run(ctx, g, comfy.RunOptions{ResumeID: req.ResumeID, OnQueued: req.OnStarted, OnEvent: func(e comfy.Event) {
		stage, ok := nodeStage[e.Node]
		if !ok {
			return
		}
		switch e.Type {
		case comfy.EventExecuting:
			enter(stage)
			progress(Progress{Stage: stage})
		case comfy.EventProgress:
			enter(stage)
			progress(Progress{Stage: stage, Value: e.Value, Max: e.Max})
		}
	}})
	enter(StageSaving)
	if err != nil {
		return Result{Timings: timings}, err
	}

	files := h.AudioFiles()
	if len(files) == 0 {
		return Result{Timings: timings}, errors.New("comfyui finished without an audio output")
	}
	if err := b.download(ctx, files[0], req.OutPath); err != nil {
		return Result{Timings: timings}, err
	}
	enter("")
	return Result{AudioPath: req.OutPath, Timings: timings}, nil
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
