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

// LowMemoryBelow is the server memory under which auto renders in two
// prompts. MiniMax Music 3 keeps about 21 GB of models loaded through
// sampling, and decoding a long song needs several GB more.
const LowMemoryBelow int64 = 48 << 30

// ResolveLowMemory decides whether a render uses the two-prompt path, and
// says why. memory is the server's, from comfy.SystemStats.Memory; 0 means
// unknown, which leaves auto off.
func ResolveLowMemory(mode config.LowMemoryMode, memory int64) (on bool, why string) {
	switch mode {
	case config.LowMemoryOn:
		return true, "on"
	case config.LowMemoryOff:
		return false, "off"
	}
	if memory <= 0 {
		return false, "auto · off, the server's memory is unknown"
	}
	if memory < LowMemoryBelow {
		return true, "auto · on for this machine, " + GB(memory)
	}
	return false, "auto · off for this machine, " + GB(memory)
}

// GB writes a memory size the way machines are sold: 34359738368 is "32 GB".
func GB(bytes int64) string {
	return fmt.Sprintf("%.0f GB", float64(bytes)/(1<<30))
}

// lowMemory resolves the mode in cfg against the server's memory.
func (b *ComfyMiniMax) lowMemory(ctx context.Context, mode config.LowMemoryMode) (bool, string) {
	if mode == config.LowMemoryOn || mode == config.LowMemoryOff {
		return ResolveLowMemory(mode, 0)
	}
	stats, err := b.client.SystemStats(ctx)
	if err != nil {
		return false, "auto · off, could not read the server's memory"
	}
	return ResolveLowMemory(mode, stats.Memory())
}

// A two-prompt render's job ID records how far it got, so a resumed render
// never samples again once the latent is saved:
//
//	lowmem1:<prompt>                    sampling
//	lowmem2:<prompt>:<latent ref>      decoding; <prompt> is "" until queued
const (
	stage1Prefix = "lowmem1:"
	stage2Prefix = "lowmem2:"
)

type stagedJob struct {
	stage  int
	prompt string
	latent string
}

func (j stagedJob) id() string {
	if j.stage == 2 {
		return stage2Prefix + j.prompt + ":" + j.latent
	}
	return stage1Prefix + j.prompt
}

func parseJob(id string) (stagedJob, bool) {
	if p, ok := strings.CutPrefix(id, stage1Prefix); ok {
		return stagedJob{stage: 1, prompt: p}, true
	}
	if rest, ok := strings.CutPrefix(id, stage2Prefix); ok {
		prompt, latent, ok := strings.Cut(rest, ":")
		if ok && latent != "" {
			return stagedJob{stage: 2, prompt: prompt, latent: latent}, true
		}
		return stagedJob{stage: 1}, true
	}
	return stagedJob{}, false
}

// Stages of the second prompt: reloading the VAE is part of decoding, so a
// render never goes back to "loading models".
var decodeStage = map[string]Stage{
	comfy.NodeVAE:        StageDecoding,
	comfy.NodeLoadLatent: StageDecoding,
	comfy.NodeDecode:     StageDecoding,
	comfy.NodeSave:       StageSaving,
}

var sampleStage = map[string]Stage{
	comfy.NodeTextEncoder: StageLoading,
	comfy.NodeDiffusion:   StageLoading,
	comfy.NodeEncode:      StageComposing,
	comfy.NodeNegative:    StageRendering,
	comfy.NodeLatent:      StageRendering,
	comfy.NodeSampler:     StageRendering,
	comfy.NodeSaveLatent:  StageDecoding,
}

// StageError names the prompt of a two-prompt render that failed.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return "low-memory render, " + e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

const (
	stageSample = "stage 1 of 2 (compose and render)"
	stageDecode = "stage 2 of 2 (decode)"
)

// staged renders in two prompts: sample and save the latent, free every
// model, then load only the VAE to decode it.
func (b *ComfyMiniMax) staged(ctx context.Context, req Request, p comfy.MiniMaxParams, job stagedJob, progress func(Progress), clk *stageClock) (Result, error) {
	started := func(j stagedJob) {
		if req.OnStarted != nil {
			req.OnStarted(j.id())
		}
	}
	latent := job.latent
	resumedLatent := latent != ""
	defer func() {
		// A detached render resumes from the latent; any other outcome ends
		// the job, and nothing would read the file again.
		if latent != "" && !errors.Is(context.Cause(ctx), ErrDetach) {
			_ = b.removeLatent(context.WithoutCancel(ctx), latent)
		}
	}()

	for {
		if latent == "" {
			ref, err := b.sample(ctx, req, p, job, progress, clk, started)
			if err != nil {
				clk.enter(StageSaving)
				return Result{Timings: clk.timings}, &StageError{Stage: stageSample, Err: err}
			}
			latent = ref
			job = stagedJob{stage: 2, latent: latent}
			started(job)
			clk.enter(StageDecoding)
			progress(Progress{Stage: StageDecoding})
			if err := b.freeModels(ctx); err != nil {
				clk.enter(StageSaving)
				return Result{Timings: clk.timings}, &StageError{Stage: stageDecode, Err: err}
			}
		}
		h, err := b.decode(ctx, req, p.VAE, job, progress, clk, started)
		var pe *comfy.PromptError
		if resumedLatent && errors.As(err, &pe) && strings.Contains(pe.Body, comfy.NodeLoadLatent) {
			// The latent a detached render saved is gone: sample again.
			latent, job, resumedLatent = "", stagedJob{stage: 1}, false
			continue
		}
		clk.enter(StageSaving)
		if err != nil {
			return Result{Timings: clk.timings}, &StageError{Stage: stageDecode, Err: err}
		}
		files := h.AudioFiles()
		if len(files) == 0 {
			return Result{Timings: clk.timings}, &StageError{Stage: stageDecode, Err: errors.New("comfyui finished without an audio output")}
		}
		if err := b.download(ctx, files[0], req.OutPath); err != nil {
			return Result{Timings: clk.timings}, err
		}
		clk.enter("")
		return Result{AudioPath: req.OutPath, Timings: clk.timings}, nil
	}
}

// sample runs (or reattaches to) the first prompt and returns the saved latent.
func (b *ComfyMiniMax) sample(ctx context.Context, req Request, p comfy.MiniMaxParams, job stagedJob, progress func(Progress), clk *stageClock, started func(stagedJob)) (string, error) {
	g := comfy.MiniMaxSampleGraph(p, latentPrefix(req.ID))
	var running string
	freed := false
	h, err := b.client.Run(ctx, g, comfy.RunOptions{
		ResumeID: job.prompt,
		OnQueued: func(id string) {
			running = id
			started(stagedJob{stage: 1, prompt: id})
		},
		OnEvent: func(e comfy.Event) {
			if !freed {
				// Asked while the prompt runs, the server frees the models
				// as soon as it ends and before it takes the next prompt.
				freed = true
				_ = b.client.Free(ctx, true, true)
			}
			clk.event(e, sampleStage, progress)
		},
	})
	if err != nil {
		return "", err
	}
	files := h.LatentFiles()
	if len(files) == 0 {
		return "", fmt.Errorf("comfyui finished prompt %s without saving the latent", running)
	}
	return comfy.LatentRef(files[0]), nil
}

// decode runs (or reattaches to) the second prompt, and once more after
// freeing memory again if it runs out.
func (b *ComfyMiniMax) decode(ctx context.Context, req Request, vae string, job stagedJob, progress func(Progress), clk *stageClock, started func(stagedJob)) (comfy.HistoryEntry, error) {
	g := comfy.MiniMaxDecodeGraph(vae, job.latent, "songsmith/"+req.ID)
	for attempt := 0; ; attempt++ {
		h, err := b.client.Run(ctx, g, comfy.RunOptions{
			ResumeID: job.prompt,
			OnQueued: func(id string) {
				job.prompt = id
				started(job)
			},
			OnEvent: func(e comfy.Event) { clk.event(e, decodeStage, progress) },
		})
		if err == nil || attempt > 0 || ctx.Err() != nil || !comfy.OutOfMemory(err, comfy.NodeDecode) {
			return h, err
		}
		job.prompt = ""
		if ferr := b.freeModels(ctx); ferr != nil {
			return h, errors.Join(err, ferr)
		}
	}
}

// freeModels asks the server to free its models and gives it a moment to do
// so before the next prompt is queued. It asks twice: a request that lands
// while the server is tidying up after a prompt is only seen once it next
// wakes, and the second one wakes it. Only cancelling fails it: a server
// that does not answer fails the next prompt anyway.
func (b *ComfyMiniMax) freeModels(ctx context.Context) error {
	for range 2 {
		_ = b.client.Free(ctx, true, true)
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), ErrDetach) {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: %w", comfy.ErrInterrupted, context.Cause(ctx))
		case <-time.After(b.settle):
		}
	}
	return nil
}

// latentPrefix is where the first prompt saves its latent: one folder for
// all of them, one name per song.
func latentPrefix(songID string) string { return "songsmith/latents/" + songID }

// removeLatent deletes a saved latent. ComfyUI has no API to delete an
// output, so it is removed only from a server on this machine, and only
// from songsmith's latent folder in that server's output folder.
func (b *ComfyMiniMax) removeLatent(ctx context.Context, ref string) error {
	f, ok := comfy.ParseLatentRef(ref)
	if !ok || f.Type != "output" || !strings.HasPrefix(f.Subfolder+"/", "songsmith/latents/") ||
		!strings.HasSuffix(f.Filename, ".latent") || strings.ContainsAny(f.Filename, `/\`) || !b.client.Local() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := b.client.OutputDir(ctx)
	if err != nil || out == "" {
		return err
	}
	p := filepath.Join(out, filepath.FromSlash(f.Subfolder), f.Filename)
	if rel, err := filepath.Rel(filepath.Join(out, "songsmith", "latents"), p); err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove saved latent: %w", err)
	}
	return nil
}

// abandonStaged stops a detached two-prompt render and deletes its latent.
func (b *ComfyMiniMax) abandonStaged(ctx context.Context, job stagedJob) error {
	var errs []error
	if job.prompt != "" {
		errs = append(errs, b.client.Interrupt(ctx, job.prompt), b.client.DeleteQueued(ctx, job.prompt))
	}
	latent := job.latent
	if job.stage == 1 && job.prompt != "" {
		if h, ok, err := b.client.History(ctx, job.prompt); err == nil && ok {
			if files := h.LatentFiles(); len(files) > 0 {
				latent = comfy.LatentRef(files[0])
			}
		}
	}
	if latent != "" {
		errs = append(errs, b.removeLatent(ctx, latent))
	}
	return errors.Join(errs...)
}
