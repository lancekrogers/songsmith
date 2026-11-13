package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/lancekrogers/songsmith/internal/config"
)

type ActionID string

const (
	ActionComfyUI     ActionID = "comfyui"
	ActionDiffusion   ActionID = "model:" + DirDiffusion
	ActionTextEncoder ActionID = "model:" + DirTextEncoder
	ActionVAE         ActionID = "model:" + DirVAE
	ActionWriter      ActionID = "writer"
)

type Kind int

const (
	KindComfyUI Kind = iota
	KindModel
	KindWriter
)

type Status int

const (
	StatusDone Status = iota
	StatusMissing
	StatusPartial
	// StatusUnavailable: songsmith cannot install this here; Detail says why
	// and what the user can do.
	StatusUnavailable
)

func (s Status) String() string {
	return [...]string{"done", "missing", "partial", "not installable here"}[s]
}

// Action is one install step with its current state.
type Action struct {
	ID       ActionID
	Kind     Kind
	Name     string
	Status   Status
	Detail   string
	Required bool
	// Percent is how much of a partial download is on disk.
	Percent float64
	// DownloadBytes is what is left to fetch; 0 when done or unknown. For
	// ComfyUI it is an estimate.
	DownloadBytes int64
	// File is set for model actions found in the manifest.
	File  *ModelFile
	Model string
}

func (a Action) Pending() bool { return a.Status != StatusDone }

type Plan struct {
	Actions []Action
}

// DownloadBytes totals what is left to fetch for every pending action.
func (p Plan) DownloadBytes() int64 {
	var n int64
	for _, a := range p.Actions {
		if a.Pending() {
			n += a.DownloadBytes
		}
	}
	return n
}

// MissingRequired returns the required actions that are not done.
func (p Plan) MissingRequired() []Action {
	var out []Action
	for _, a := range p.Actions {
		if a.Required && a.Pending() {
			out = append(out, a)
		}
	}
	return out
}

func (p Plan) Ready() bool { return len(p.MissingRequired()) == 0 }

// Slot is one configured model file and the action that installs it.
type Slot struct {
	ID   ActionID
	Dir  string
	Name string
	Role string
}

// ModelSlots lists the configured model files in install order.
func ModelSlots(c config.ComfyUI) []Slot {
	return []Slot{
		{ActionDiffusion, DirDiffusion, c.DiffusionModel, "Diffusion model"},
		{ActionTextEncoder, DirTextEncoder, c.TextEncoder, "Text encoder"},
		{ActionVAE, DirVAE, c.VAE, "VAE"},
	}
}

// Plan returns every install action in the order they run, with the state
// of each on this machine. It reads the disk, asks Ollama for its model
// list and, only when something is left to download, checks that the hosts
// it comes from answer.
func (in *Installer) Plan(ctx context.Context, cfg config.Config) Plan {
	comfyDir := cfg.ComfyUI.Dir
	comfy := in.comfyAction(ctx, comfyDir)
	var models []Action
	for _, s := range ModelSlots(cfg.ComfyUI) {
		models = append(models, modelAction(comfyDir, s.ID, s.Dir, s.Name, s.Role))
	}
	installable := func(a Action) bool { return a.Pending() && a.Status != StatusUnavailable }

	var hosts []host
	if installable(comfy) {
		if !exists(filepath.Join(comfyDir, "main.py")) {
			hosts = append(hosts, hostGitHub)
		}
		hosts = append(hosts, hostPyPI)
	}
	if slices.ContainsFunc(models, installable) {
		hosts = append(hosts, hostHF)
	}
	offline := in.reachHosts(ctx, hosts)
	markOffline := func(a *Action, hs ...host) {
		for _, h := range hs {
			if err := offline[h.name]; err != nil && installable(*a) {
				a.Status, a.Detail = StatusUnavailable, unreachable(h.name, h.purpose, err).Error()
			}
		}
	}
	markOffline(&comfy, hostGitHub, hostPyPI)

	hasCheckout := exists(filepath.Join(comfyDir, "main.py"))
	for i := range models {
		a := &models[i]
		markOffline(a, hostHF)
		if installable(*a) && !hasCheckout {
			if comfy.Status == StatusUnavailable {
				a.Status = StatusUnavailable
				a.Detail = "needs ComfyUI, which cannot be installed here: " + comfy.Detail
			} else {
				a.Detail += "; downloads after ComfyUI is installed"
			}
		}
	}

	p := Plan{Actions: append([]Action{comfy}, models...)}
	free, freeErr := in.FreeSpace(existingAncestor(comfyDir))
	var need int64
	// Each download action becomes unavailable once the space it needs,
	// added to what earlier actions need, exceeds what is free.
	for i := range p.Actions {
		a := &p.Actions[i]
		if !installable(*a) || a.DownloadBytes <= 0 || freeErr != nil {
			continue
		}
		need += a.DownloadBytes
		if uint64(need) > free {
			a.Status = StatusUnavailable
			a.Detail = fmt.Sprintf("not enough disk space: %s needed in total, %s free on the volume holding %s",
				GB(need), GB(int64(free)), existingAncestor(comfyDir))
		}
	}
	p.Actions = append(p.Actions, writerAction(ctx, cfg.Ollama))
	return p
}

type host struct{ name, url, purpose string }

var (
	hostGitHub = host{"github.com", "https://github.com", "download ComfyUI"}
	hostPyPI   = host{"pypi.org", "https://pypi.org/simple/", "install ComfyUI's Python packages"}
	hostHF     = host{"huggingface.co", "https://huggingface.co", "download the MiniMax Music 3 files"}
)

// reachHosts checks hosts concurrently and returns the failures by name.
func (in *Installer) reachHosts(ctx context.Context, hosts []host) map[string]error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]error{}
	for _, h := range hosts {
		wg.Go(func() {
			if err := in.Reach(ctx, h.url); err != nil {
				mu.Lock()
				out[h.name] = err
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return out
}

func (in *Installer) comfyAction(ctx context.Context, dir string) Action {
	a := Action{ID: ActionComfyUI, Kind: KindComfyUI, Name: "ComfyUI", Required: true}
	st := InspectComfyUI(dir)
	switch st.Status {
	case ComfyInstalled:
		a.Status, a.Detail = StatusDone, st.Detail
		return a
	case ComfyBlocked:
		a.Status, a.Detail = StatusUnavailable, st.Detail
		return a
	case ComfyIncomplete:
		a.Status, a.Detail = StatusPartial, st.Detail
	default:
		a.Status, a.Detail = StatusMissing, st.Detail
	}
	a.DownloadBytes = in.ComfyDiskEstimate()
	if !st.HasCheckout {
		if err := in.gitMissing(); err != nil {
			a.Status, a.Detail = StatusUnavailable, err.Error()
			return a
		}
	}
	if !st.HasVenv {
		py, err := in.FindPython(ctx)
		if err != nil {
			a.Status, a.Detail = StatusUnavailable, err.Error()
			return a
		}
		a.Detail += "; environment through " + py.String()
	}
	if in.GOOS == "linux" {
		a.Detail += " (Linux install is untested)"
	}
	return a
}

func modelAction(comfyDir string, id ActionID, dir, name, role string) Action {
	a := Action{ID: id, Kind: KindModel, Name: role, Required: true}
	f, ok := Lookup(dir, name)
	if !ok {
		path := filepath.Join(comfyDir, "models", dir, name)
		if _, err := os.Stat(path); err == nil {
			a.Status, a.Detail = StatusDone, name+" (not from "+Repo+", size not checked)"
		} else {
			a.Status = StatusUnavailable
			a.Detail = fmt.Sprintf("%s is not a file in %s, so songsmith cannot download it; put it in %s yourself or choose a listed file in Settings → Music model",
				name, Repo, filepath.Dir(path))
		}
		return a
	}
	a.File = &f
	st := State(comfyDir, f)
	switch st.Status {
	case FileInstalled:
		a.Status, a.Detail = StatusDone, fmt.Sprintf("%s (%s)", f.Name, GB(f.Size))
	case FilePartial:
		a.Status, a.Percent = StatusPartial, st.Percent()
		a.DownloadBytes = f.Size - min(st.Bytes, f.Size)
		a.Detail = fmt.Sprintf("%s: %.0f%% downloaded (%s of %s), resumes where it stopped", f.Name, a.Percent, GB(st.Bytes), GB(f.Size))
	case FileWrongSize:
		a.Status, a.DownloadBytes = StatusMissing, f.Size
		a.Detail = fmt.Sprintf("%s is %s on disk, expected %s; a verified download replaces it", f.Name, GB(st.Bytes), GB(f.Size))
	default:
		a.Status, a.DownloadBytes = StatusMissing, f.Size
		a.Detail = fmt.Sprintf("%s (%s) missing", f.Name, GB(f.Size))
	}
	return a
}

func writerAction(ctx context.Context, o config.Ollama) Action {
	a := Action{ID: ActionWriter, Kind: KindWriter, Name: "Writer model", Model: o.Model}
	names, err := WriterModels(ctx, o.URL)
	switch {
	case err != nil:
		a.Status, a.Detail = StatusUnavailable, err.Error()
	case slices.Contains(names, ModelTag(o.Model)):
		a.Status, a.Detail = StatusDone, o.Model+" in Ollama"
	case len(names) > 0:
		a.Status = StatusMissing
		a.Detail = fmt.Sprintf("%s is not in Ollama, which has %d other models: choose one in Settings → Writer, or pull %s", o.Model, len(names), o.Model)
	default:
		a.Status, a.Detail = StatusMissing, o.Model+" is not in Ollama yet; setup pulls it"
	}
	return a
}
