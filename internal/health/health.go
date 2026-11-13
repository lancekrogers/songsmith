// Package health checks whether this machine can draft and render songs:
// the ComfyUI install, the MiniMax Music 3 files, the server and Ollama.
package health

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/writer"
)

type Result int

const (
	OK Result = iota
	Warn
	Fail
)

func (r Result) String() string {
	switch r {
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	default:
		return "ok"
	}
}

// Stable check IDs.
const (
	IDComfyUI          = "comfyui-install"
	IDDiffusionModel   = "model-diffusion"
	IDTextEncoder      = "model-text-encoder"
	IDVAE              = "model-vae"
	IDServer           = "comfyui-server"
	IDNodes            = "comfyui-nodes"
	IDServerModelFiles = "comfyui-model-list"
	IDWriter           = "ollama"
	IDFFprobe          = "ffprobe"
	IDInstructions     = "writer-instructions"
)

// Install actions that can fix a failing check.
const (
	FixComfyUI     = "install-comfyui"
	FixMusicModel  = "install-music-model"
	FixWriterModel = "pull-writer-model"
)

type Check struct {
	ID     string
	Name   string
	Result Result
	Detail string
	// Blocking is true when this check failing stops songs from rendering.
	Blocking bool
	// Fix names the install action that would fix the check, or "".
	Fix string
	// Missing names what is absent when the check is not OK, such as
	// "ComfyUI" or "Ollama"; "" when nothing is missing.
	Missing string
}

// Blocker returns the first failing check that stops rendering.
func Blocker(checks []Check) (Check, bool) {
	for _, c := range checks {
		if c.Result == Fail && c.Blocking {
			return c, true
		}
	}
	return Check{}, false
}

// Failed reports whether any check failed.
func Failed(checks []Check) bool {
	return slices.ContainsFunc(checks, func(c Check) bool { return c.Result == Fail })
}

// Missing names, once each and in check order, what is absent: the pieces
// songsmith can install and the ones the user has to get elsewhere.
func Missing(checks []Check) (installable, elsewhere []string) {
	for _, c := range checks {
		if c.Result == OK || c.Missing == "" {
			continue
		}
		list := &elsewhere
		if c.Fix != "" {
			list = &installable
		}
		if !slices.Contains(*list, c.Missing) {
			*list = append(*list, c.Missing)
		}
	}
	return installable, elsewhere
}

func Find(checks []Check, id string) (Check, bool) {
	i := slices.IndexFunc(checks, func(c Check) bool { return c.ID == id })
	if i < 0 {
		return Check{}, false
	}
	return checks[i], true
}

// Run runs every check. The server and Ollama probes use ctx for their timeouts.
func Run(ctx context.Context, cfg config.Config) []Check {
	m := server.New(cfg.ComfyUI, cfg.StateDir)
	_, mainErr := os.Stat(filepath.Join(cfg.ComfyUI.Dir, "main.py"))
	_, pyErr := os.Stat(m.Python())
	out := []Check{installCheck(cfg.ComfyUI, m.Python(), mainErr, pyErr)}
	out = append(out, modelChecks(cfg.ComfyUI)...)

	st, _ := m.Status(ctx)
	out = append(out, serverCheck(cfg.ComfyUI, st))
	if st.Running {
		out = append(out, nodeChecks(ctx, m.Client(), cfg.ComfyUI)...)
	}

	names, err := ollama.NewClient(cfg.Ollama.URL).Models(ctx)
	out = append(out, writerCheck(cfg.Ollama, names, err))
	if ch, ok := instructionsCheck(cfg.Ollama.InstructionsFile); ok {
		out = append(out, ch)
	}
	_, err = exec.LookPath("ffprobe")
	return append(out, ffprobeCheck(err))
}

func installCheck(c config.ComfyUI, python string, mainErr, pyErr error) Check {
	ch := Check{ID: IDComfyUI, Name: "ComfyUI install", Blocking: true, Fix: FixComfyUI, Missing: "ComfyUI"}
	switch {
	case mainErr != nil:
		ch.Result = Fail
		ch.Detail = fmt.Sprintf("ComfyUI is not installed in %s. songsmith can install it there: Finish setup. "+
			"To use a ComfyUI you already have, set Settings → Music engine → Install folder (config key comfyui.dir)", c.Dir)
	case pyErr != nil:
		ch.Result = Fail
		ch.Detail = fmt.Sprintf("no Python environment at %s. songsmith can create it: Finish setup (shell: `uv venv --python 3.12 %s`)",
			python, filepath.Join(c.Dir, ".venv"))
	default:
		ch.Result, ch.Detail = OK, c.Dir
	}
	return ch
}

func modelChecks(c config.ComfyUI) []Check {
	files := []struct{ id, dir, file string }{
		{IDDiffusionModel, "diffusion_models", c.DiffusionModel},
		{IDTextEncoder, "text_encoders", c.TextEncoder},
		{IDVAE, "vae", c.VAE},
	}
	out := make([]Check, 0, len(files))
	for _, f := range files {
		ch := Check{ID: f.id, Name: "Model " + f.dir, Blocking: true, Fix: FixMusicModel, Missing: "music model files"}
		p := filepath.Join(c.Dir, "models", f.dir, f.file)
		if fi, err := os.Stat(p); err != nil {
			ch.Result = Fail
			ch.Detail = fmt.Sprintf("missing %s. songsmith can download it from Comfy-Org/MiniMax-Music-3: Finish setup "+
				"(shell: `hf download Comfy-Org/MiniMax-Music-3 %s/%s --local-dir %s`)", f.file, f.dir, f.file, filepath.Join(c.Dir, "models"))
		} else {
			ch.Result, ch.Detail = OK, fmt.Sprintf("%s (%.1f GB)", f.file, float64(fi.Size())/1e9)
		}
		out = append(out, ch)
	}
	return out
}

// serverCheck never blocks: songsmith starts a stopped server on the first
// render, and a CPU-mode server renders, only far slower.
func serverCheck(c config.ComfyUI, st server.Status) Check {
	ch := Check{ID: IDServer, Name: "ComfyUI server"}
	switch {
	case !st.Running:
		ch.Result, ch.Detail = Warn, fmt.Sprintf("not running at %s; songsmith starts it on the first render (shell: `songsmith server start`)", c.BaseURL())
	case st.Device == "mps" && !st.GPUOnly:
		ch.Result, ch.Detail = Fail, fmt.Sprintf("running without %s, so the MiniMax LLM runs on the CPU (about 50x slower); "+
			"stop that server and let songsmith start it (shell: `songsmith server start`)", server.RequiredFlag)
	default:
		ch.Result, ch.Detail = OK, fmt.Sprintf("running at %s on %s", c.BaseURL(), st.Device)
	}
	return ch
}

type objectInfoer interface {
	ObjectInfo(ctx context.Context, class string) (comfy.NodeInfo, bool, error)
}

func nodeChecks(ctx context.Context, c objectInfoer, models config.ComfyUI) []Check {
	nodes := Check{ID: IDNodes, Name: "ComfyUI nodes", Blocking: true}
	var missingNodes, unseenFiles []string
	for _, class := range slices.Sorted(maps.Keys(comfy.RequiredNodes)) {
		info, ok, err := c.ObjectInfo(ctx, class)
		if err != nil {
			nodes.Result, nodes.Detail = Fail, err.Error()
			return []Check{nodes}
		}
		if !ok {
			missingNodes = append(missingNodes, class)
			continue
		}
		for _, in := range comfy.RequiredNodes[class] {
			if !info.HasInput(in) {
				missingNodes = append(missingNodes, class+"."+in)
			}
		}
		for input, want := range map[string]string{"unet_name": models.DiffusionModel, "clip_name": models.TextEncoder, "vae_name": models.VAE} {
			if opts := info.ComboOptions(input); opts != nil && !slices.Contains(opts, want) {
				unseenFiles = append(unseenFiles, want)
			}
		}
	}
	if len(missingNodes) > 0 {
		nodes.Result, nodes.Detail = Fail, "missing "+strings.Join(missingNodes, ", ")+"; update ComfyUI to a version with MiniMax Music 3 support (shell: `git pull` in "+models.Dir+")"
	} else {
		nodes.Result, nodes.Detail = OK, "MiniMax Music 3 nodes available"
	}
	out := []Check{nodes}
	if len(unseenFiles) > 0 {
		out = append(out, Check{ID: IDServerModelFiles, Name: "ComfyUI model list", Result: Fail, Blocking: true,
			Detail: "ComfyUI cannot see " + strings.Join(unseenFiles, ", ") + "; restart ComfyUI so it lists the new files (shell: `songsmith server stop`, then `songsmith server start`)"})
	}
	return out
}

// writerCheck never blocks: lyrics can always be written by hand.
func writerCheck(o config.Ollama, names []string, err error) Check {
	ch := Check{ID: IDWriter, Name: "Ollama"}
	switch {
	case err != nil:
		ch.Result, ch.Missing = Warn, "Ollama"
		ch.Detail = fmt.Sprintf("Ollama is not reachable at %s, and songsmith cannot install it: get it from https://ollama.com. "+
			"Lyrics can still be written by hand (%v)", o.URL, err)
	case !slices.Contains(names, o.Model):
		ch.Result, ch.Fix, ch.Missing = Warn, FixWriterModel, "writer model "+o.Model
		pull := fmt.Sprintf("songsmith can pull %s: Finish setup (shell: `ollama pull %s`)", o.Model, o.Model)
		if len(names) == 0 {
			ch.Detail = fmt.Sprintf("no models are installed in Ollama. %s", pull)
		} else {
			ch.Detail = fmt.Sprintf("model %s is not installed, but %d other %s: pick one in Settings → Writer, or %s",
				o.Model, len(names), plural(len(names), "model is", "models are"), pull)
		}
	default:
		ch.Result, ch.Detail = OK, fmt.Sprintf("%s at %s", o.Model, o.URL)
	}
	return ch
}

// instructionsCheck reports on a configured instructions file; ok is false
// when none is set. It never blocks: drafts fall back to the built-in prompt.
func instructionsCheck(path string) (ch Check, ok bool) {
	if path == "" {
		return Check{}, false
	}
	ch = Check{ID: IDInstructions, Name: "Writing instructions", Result: OK, Detail: path}
	if _, err := writer.Instructions(path); err != nil {
		ch.Result = Warn
		ch.Detail = fmt.Sprintf("%v, so drafts use the built-in instructions. "+
			"Fix the file or change it in Settings → Writer (config key ollama.instructions_file)", err)
	}
	return ch, true
}

// ffprobeCheck never blocks: without ffprobe songs render but their length
// is not recorded.
func ffprobeCheck(lookErr error) Check {
	ch := Check{ID: IDFFprobe, Name: "ffprobe", Result: OK, Detail: "found"}
	if lookErr != nil {
		ch.Result, ch.Missing = Warn, "ffprobe"
		ch.Detail = "not found, and songsmith cannot install it: install ffmpeg (macOS: `brew install ffmpeg`). " +
			"songsmith uses it to record song lengths"
	}
	return ch
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
