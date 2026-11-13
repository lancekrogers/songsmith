package setup

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// uvPython is the Python version uv creates ComfyUI's environment with; it
// is the version songsmith is tested on.
const uvPython = "3.12"

// Without uv, songsmith uses an installed Python 3.12 or 3.13: ComfyUI's
// README calls 3.13 very well supported and 3.12 the fallback, and warns
// that 3.14 breaks some custom node dependencies.
var supportedPython = []string{"3.12", "3.13"}

// PythonTool is how the ComfyUI environment gets created: through uv, or
// through an installed Python's venv module.
type PythonTool struct {
	UV      string
	Python  string
	Version string
}

func (t PythonTool) String() string {
	if t.UV != "" {
		return "uv (Python " + uvPython + ")"
	}
	return "Python " + t.Version + " (" + t.Python + ")"
}

// MissingError names a prerequisite songsmith cannot install itself and how
// to get it.
type MissingError struct {
	What string
	How  string
}

func (e *MissingError) Error() string { return e.What + ". " + e.How }

func (in *Installer) gitMissing() error {
	if _, err := in.LookPath("git"); err == nil {
		return nil
	}
	how := "Install it with your package manager, for example `sudo apt install git` or `sudo dnf install git`, then run setup again."
	if in.GOOS == "darwin" {
		how = "Install it with `xcode-select --install` (or `brew install git`), then run setup again."
	}
	return &MissingError{What: "git is not installed; songsmith needs it to download ComfyUI", How: how}
}

func (in *Installer) pythonHelp() string {
	if in.GOOS == "darwin" {
		return "Install uv with `brew install uv` (it fetches Python " + uvPython + " itself), or install Python with `brew install python@" + uvPython + "`, then run setup again."
	}
	return "Install uv with `curl -LsSf https://astral.sh/uv/install.sh | sh` (it fetches Python " + uvPython +
		" itself), or install Python and its venv module, for example `sudo apt install python" + uvPython + " python" + uvPython + "-venv`, then run setup again."
}

// FindPython picks uv when it is on PATH, otherwise the first installed
// python3.13, python3.12 or python3 of a supported version with a working
// venv module.
func (in *Installer) FindPython(ctx context.Context) (PythonTool, error) {
	if uv, err := in.LookPath("uv"); err == nil {
		return PythonTool{UV: uv}, nil
	}
	var rejected []string
	seen := map[string]bool{}
	for _, name := range []string{"python3.13", "python3.12", "python3"} {
		path, err := in.LookPath(name)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		ver, err := in.pythonVersion(ctx, path)
		if err != nil {
			if ctx.Err() != nil {
				return PythonTool{}, ctx.Err()
			}
			continue
		}
		if !slices.Contains(supportedPython, ver) {
			rejected = append(rejected, fmt.Sprintf("%s is Python %s", path, ver))
			continue
		}
		if err := in.Run(ctx, Cmd{Name: path, Args: []string{"-c", "import venv, ensurepip"}, Out: &bytes.Buffer{}}); err != nil {
			if ctx.Err() != nil {
				return PythonTool{}, ctx.Err()
			}
			rejected = append(rejected, fmt.Sprintf("%s has no working venv module", path))
			continue
		}
		return PythonTool{Python: path, Version: ver}, nil
	}
	what := "neither uv nor Python " + strings.Join(supportedPython, " or ") + " is installed; songsmith needs one to set up ComfyUI"
	if len(rejected) > 0 {
		what = "no usable Python found (" + strings.Join(rejected, "; ") + "); songsmith needs uv or Python " +
			strings.Join(supportedPython, " or ") + " with its venv module to set up ComfyUI"
	}
	return PythonTool{}, &MissingError{What: what, How: in.pythonHelp()}
}

func (in *Installer) pythonVersion(ctx context.Context, path string) (string, error) {
	var out bytes.Buffer
	err := in.Run(ctx, Cmd{Name: path, Args: []string{"-c", "import sys; print('%d.%d' % sys.version_info[:2])"}, Out: &out})
	if err != nil {
		return "", err
	}
	ver := strings.TrimSpace(out.String())
	major, minor, ok := strings.Cut(ver, ".")
	if _, err := strconv.Atoi(major); !ok || err != nil {
		return "", fmt.Errorf("unexpected version %q from %s", ver, path)
	}
	if _, err := strconv.Atoi(minor); err != nil {
		return "", fmt.Errorf("unexpected version %q from %s", ver, path)
	}
	return ver, nil
}
