package setup

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/ollama"
)

// OllamaMissing explains that songsmith does not install Ollama itself.
func OllamaMissing(url string, err error) error {
	return &MissingError{
		What: fmt.Sprintf("Ollama is not reachable at %s (%v)", url, err),
		How: "songsmith does not install Ollama. Get it from https://ollama.com/download, start it, then run setup again. " +
			"Lyrics and style can still be written by hand without it.",
	}
}

// ModelTag adds Ollama's implicit ":latest" to an untagged model name.
func ModelTag(model string) string {
	if model != "" && !strings.Contains(model, ":") {
		return model + ":latest"
	}
	return model
}

// WriterModels lists the models installed in Ollama, with a short timeout
// so a stopped Ollama does not hold up a status check.
func WriterModels(ctx context.Context, url string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	names, err := ollama.NewClient(url).Models(ctx)
	if err != nil {
		return nil, OllamaMissing(url, err)
	}
	return names, nil
}

// PullWriter pulls model into the Ollama at url.
func (in *Installer) PullWriter(ctx context.Context, url, model string, progress func(Progress)) error {
	emit := throttle(progress, 250*time.Millisecond)
	names, err := WriterModels(ctx, url)
	if err != nil {
		return err
	}
	if slices.Contains(names, ModelTag(model)) {
		emit(newProgress(model, PhaseSkipped, 0, 0, "already in Ollama"))
		return nil
	}
	emit(newProgress(model, PhaseStart, 0, 0, "pulling through Ollama"))
	err = ollama.NewClient(url).Pull(ctx, model, func(status string, done, total int64) {
		phase := PhaseDownload
		if total == 0 || strings.HasPrefix(status, "verifying") || strings.HasPrefix(status, "writing") {
			phase = PhaseInstall
		}
		emit(newProgress(model, phase, done, total, status))
	})
	if err != nil {
		return err
	}
	emit(newProgress(model, PhaseDone, 0, 0, "ready"))
	return nil
}
