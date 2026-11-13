package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/setup"
	"github.com/lancekrogers/songsmith/internal/tui"
)

// demoSetupEnv points the TUI's installer at a local stand-in for Hugging
// Face (demos/setupfake) for recording demos/setup.tape. Unset, it does
// nothing.
const demoSetupEnv = "SONGSMITH_DEMO_SETUP"

// tuiInstaller is the installer the TUI uses: the real one, unless the demo
// variable is set, when every download goes to the loopback stand-in and
// ComfyUI's install is simulated.
func tuiInstaller(version string) (tui.Installer, error) {
	in := setup.New(version)
	base := os.Getenv(demoSetupEnv)
	if base == "" {
		return in, nil
	}
	return newDemoInstaller(in, base)
}

type demoInstaller struct{ *setup.Installer }

func newDemoInstaller(in *setup.Installer, base string) (*demoInstaller, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("%s must be an http://127.0.0.1 address, not %q", demoSetupEnv, base)
	}
	dial := &net.Dialer{Timeout: 5 * time.Second}
	in.HTTP = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || !isLoopback(host) {
				return nil, fmt.Errorf("demo setup refuses to reach %s", addr)
			}
			return dial.DialContext(ctx, network, addr)
		},
	}}
	in.HFBase = base + "/hf"
	in.Reach = func(context.Context, string) error { return nil }
	in.LookPath = func(file string) (string, error) { return "/demo/" + file, nil }
	in.Run = func(context.Context, setup.Cmd) error { return errors.New("demo setup runs no programs") }

	resp, err := in.HTTP.Get(base + "/catalog.json")
	if err != nil {
		return nil, fmt.Errorf("demo setup: %w", err)
	}
	defer resp.Body.Close()
	var catalog []struct {
		Dir, Name, SHA256, Label, Tag, Note string
		Size                                int64
		Recommended                         bool
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil || len(catalog) == 0 {
		return nil, fmt.Errorf("demo setup: bad catalog: %v", err)
	}
	setup.Manifest = setup.Manifest[:0:0]
	for _, f := range catalog {
		setup.Manifest = append(setup.Manifest, setup.ModelFile{Dir: f.Dir, Name: f.Name, Size: f.Size, SHA256: f.SHA256,
			Label: f.Label, Tag: f.Tag, Note: f.Note, Recommended: f.Recommended})
	}
	return &demoInstaller{in}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// InstallComfyUI lays out what a finished install leaves, with a few lines
// of output, instead of cloning and installing packages.
func (d *demoInstaller) InstallComfyUI(ctx context.Context, dir, _ string, progress func(setup.Progress)) error {
	if !underTemp(dir) {
		return fmt.Errorf("demo setup only installs into a temporary folder, not %s", dir)
	}
	steps := []string{
		"Cloning into '.ComfyUI.songsmith-clone'...",
		"Receiving objects: 100% (2210/2210), done.",
		"Using CPython 3.12 · Creating virtual environment at: .venv",
		"Collecting torch",
		"Downloading torch-2.9.1-cp312-none-macosx_11_0_arm64.whl (74.5 MB)",
		"Installing collected packages: torch, torchaudio, comfyui-frontend-package",
		"Successfully installed 61 packages",
	}
	for _, line := range steps {
		progress(setup.Progress{Asset: "ComfyUI", Phase: setup.PhaseInstall, Detail: line})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
	for _, p := range []string{"main.py", ".venv/bin/python", ".venv/lib/python3.12/site-packages/torch/__init__.py"} {
		path := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return err
		}
	}
	progress(setup.Progress{Asset: "ComfyUI", Phase: setup.PhaseDone, Percent: 100, Detail: "installed in " + dir})
	return nil
}

func underTemp(dir string) bool {
	dir = filepath.Clean(dir)
	for _, root := range []string{"/tmp", "/private/tmp", filepath.Clean(os.TempDir())} {
		if strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
