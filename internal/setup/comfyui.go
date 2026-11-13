package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const comfyAsset = "ComfyUI"

// incompleteMarker sits in a venv songsmith created until its packages are
// installed, so an interrupted install resumes instead of looking finished.
const incompleteMarker = ".songsmith-install-incomplete"

// ComfyDiskEstimate is roughly what a ComfyUI checkout and its environment
// take on disk: torch with CUDA libraries is much larger on Linux.
func (in *Installer) ComfyDiskEstimate() int64 {
	if in.GOOS == "darwin" {
		return 3e9
	}
	return 10e9
}

type ComfyStatus int

const (
	ComfyMissing ComfyStatus = iota
	// ComfyIncomplete: the checkout exists but the environment or its
	// packages do not; setup resumes from there.
	ComfyIncomplete
	// ComfyBlocked: the folder holds something other than ComfyUI.
	ComfyBlocked
	ComfyInstalled
)

type ComfyState struct {
	Status ComfyStatus
	Detail string
	// Steps already done, so the plan can say what is left.
	HasCheckout, HasVenv, HasTorch bool
}

func venvPython(dir string) string { return filepath.Join(dir, ".venv", "bin", "python") }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func hasTorch(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, ".venv", "lib", "python3*", "site-packages", "torch"))
	return len(m) > 0
}

// InspectComfyUI reports how far a ComfyUI install in dir has got.
func InspectComfyUI(dir string) ComfyState {
	st := ComfyState{
		HasCheckout: exists(filepath.Join(dir, "main.py")),
		HasVenv:     exists(venvPython(dir)),
		HasTorch:    hasTorch(dir),
	}
	incomplete := exists(filepath.Join(dir, ".venv", incompleteMarker))
	switch {
	case st.HasCheckout && st.HasVenv && st.HasTorch && !incomplete:
		st.Status, st.Detail = ComfyInstalled, dir
	case st.HasCheckout && !st.HasVenv:
		st.Status, st.Detail = ComfyIncomplete, "checkout found, Python environment not created yet"
	case st.HasCheckout:
		st.Status, st.Detail = ComfyIncomplete, "Python packages not fully installed"
	default:
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) > 0 {
			st.Status = ComfyBlocked
			st.Detail = dir + " exists, is not empty and holds no ComfyUI (no main.py); choose another folder in Settings → Music engine → Install folder, or empty it"
		} else {
			st.Status, st.Detail = ComfyMissing, "not installed in "+dir
		}
	}
	return st
}

// InstallComfyUI clones ComfyUI into dir, creates dir/.venv and installs
// torch and ComfyUI's requirements into it, appending all subprocess output
// to logPath. Steps already done are skipped, so a failed run resumes.
func (in *Installer) InstallComfyUI(ctx context.Context, dir, logPath string, progress func(Progress)) error {
	emit := progress
	if emit == nil {
		emit = func(Progress) {}
	}
	dir = filepath.Clean(dir)
	st := InspectComfyUI(dir)
	switch st.Status {
	case ComfyInstalled:
		emit(newProgress(comfyAsset, PhaseSkipped, 0, 0, "already installed in "+dir))
		return nil
	case ComfyBlocked:
		return errors.New(st.Detail)
	}

	if !st.HasCheckout {
		if err := in.gitMissing(); err != nil {
			return err
		}
	}
	py, err := in.FindPython(ctx)
	if err != nil && !st.HasVenv {
		return err
	}
	if err := in.checkSpace(dir, in.ComfyDiskEstimate()); err != nil {
		return err
	}
	if !st.HasCheckout {
		if err := in.Reach(ctx, "https://github.com"); err != nil {
			return unreachable("github.com", "download ComfyUI", err)
		}
	}
	if err := in.Reach(ctx, "https://pypi.org/simple/"); err != nil {
		return unreachable("pypi.org", "install ComfyUI's Python packages", err)
	}

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create log folder: %w", err)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open install log: %w", err)
	}
	defer logf.Close()

	const steps = 4
	step := func(n int, name string) func(string) {
		base := Progress{Asset: comfyAsset, Step: name, Phase: PhaseInstall, Percent: float64(n-1) / steps * 100}
		fmt.Fprintf(logf, "\n== %s\n", name)
		return func(line string) {
			p := base
			p.Detail = line
			emit(p)
		}
	}
	run := func(report func(string), c Cmd) error {
		w := &lastLine{report: report}
		c.Out = io.MultiWriter(logf, w)
		err := in.Run(ctx, c)
		w.flush()
		if err != nil && ctx.Err() == nil {
			return fmt.Errorf("%w (see %s)", err, logPath)
		}
		return err
	}

	emit(newProgress(comfyAsset, PhaseStart, 0, 0, "installing into "+dir))
	if !st.HasCheckout {
		report := step(1, "Download ComfyUI")
		report("git clone " + in.ComfyRepo)
		if err := in.clone(ctx, dir, func(c Cmd) error { return run(report, c) }); err != nil {
			return fmt.Errorf("download ComfyUI: %w", err)
		}
	}

	marker := filepath.Join(dir, ".venv", incompleteMarker)
	if !st.HasVenv {
		report := step(2, "Create Python environment")
		report("using " + py.String())
		c := Cmd{Dir: dir, Name: py.Python, Args: []string{"-m", "venv", ".venv"}}
		if py.UV != "" {
			c = Cmd{Dir: dir, Name: py.UV, Args: []string{"venv", "--seed", "--python", uvPython, ".venv"}}
		}
		if err := run(report, c); err != nil {
			return fmt.Errorf("create Python environment: %w", err)
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			return fmt.Errorf("mark install in progress: %w", err)
		}
	}

	pip := func(args ...string) Cmd {
		if py.UV != "" {
			return Cmd{Dir: dir, Name: py.UV, Args: append([]string{"pip", "install", "--python", venvPython(dir)}, args...)}
		}
		return Cmd{Dir: dir, Name: venvPython(dir), Args: append([]string{"-m", "pip", "install"}, args...)}
	}
	report := step(3, "Install torch")
	if err := run(report, pip("torch", "torchvision", "torchaudio")); err != nil {
		return fmt.Errorf("install torch: %w", err)
	}
	report = step(4, "Install ComfyUI requirements")
	if err := run(report, pip("-r", "requirements.txt")); err != nil {
		return fmt.Errorf("install ComfyUI requirements: %w", err)
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("finish install: %w", err)
	}
	emit(newProgress(comfyAsset, PhaseDone, 0, 0, "installed in "+dir))
	return nil
}

// clone checks ComfyUI out beside dir and renames it into place, so an
// interrupted clone never leaves dir half-filled.
func (in *Installer) clone(ctx context.Context, dir string, run func(Cmd) error) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	tmp := filepath.Join(parent, "."+filepath.Base(dir)+".songsmith-clone")
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("remove unfinished clone %s: %w", tmp, err)
	}
	err := run(Cmd{
		Dir:  parent,
		Name: "git",
		Args: []string{"clone", "--depth", "1", "--progress", in.ComfyRepo, tmp},
		Env:  []string{"GIT_TERMINAL_PROMPT=0"},
	})
	if err != nil {
		return err
	}
	if !exists(filepath.Join(tmp, "main.py")) {
		return fmt.Errorf("the clone of %s has no main.py", in.ComfyRepo)
	}
	// dir may exist as an empty folder; os.Remove refuses anything else.
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace empty %s: %w", dir, err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("move clone into %s: %w", dir, err)
	}
	return nil
}

// lastLine reports the most recent non-empty output line, treating both
// "\r" and "\n" as line ends so progress bars show their latest state.
type lastLine struct {
	mu     sync.Mutex
	buf    []byte
	report func(string)
}

func (w *lastLine) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" {
			w.report(line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *lastLine) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if line := strings.TrimSpace(string(w.buf)); line != "" {
		w.report(line)
	}
	w.buf = nil
}
