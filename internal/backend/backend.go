// Package backend defines how songsmith renders a song, independent of the
// model or server that does the work.
package backend

import (
	"context"
	"time"
)

type Stage string

const (
	StageQueued    Stage = "queued"
	StageLoading   Stage = "loading models"
	StageComposing Stage = "composing"
	StageRendering Stage = "rendering audio"
	StageDecoding  Stage = "decoding"
	StageSaving    Stage = "saving"
)

// Progress reports where a render is. Value and Max are zero when the stage
// has no step count.
type Progress struct {
	Stage Stage
	Value int
	Max   int
}

type Request struct {
	ID         string
	Caption    string
	Lyrics     string
	Seed       int64
	MaxSeconds float64
	// OutPath is where the finished audio file is written.
	OutPath string
	// ResumeID reattaches to a render started earlier, if the backend still has it.
	ResumeID string
	// OnStarted receives the backend's job ID as soon as the render is submitted,
	// so it can be resumed after a crash.
	OnStarted func(jobID string)
}

type Result struct {
	AudioPath string
	Timings   map[Stage]time.Duration
}

type Backend interface {
	Name() string
	Generate(ctx context.Context, req Request, progress func(Progress)) (Result, error)
}
