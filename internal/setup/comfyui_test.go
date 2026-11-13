package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSystem stands in for PATH and subprocesses. Each command it runs
// leaves the files the real one would.
type fakeSystem struct {
	t       *testing.T
	tools   map[string]string // name -> path on PATH
	pyVer   map[string]string // python path -> version
	noVenv  map[string]bool
	failOn  string
	ran     []string
	reachOK bool
}

func newFakeSystem(t *testing.T, tools ...string) *fakeSystem {
	fs := &fakeSystem{t: t, tools: map[string]string{}, pyVer: map[string]string{}, noVenv: map[string]bool{}, reachOK: true}
	for _, name := range tools {
		fs.tools[name] = "/fake/bin/" + name
	}
	return fs
}

func (fs *fakeSystem) installer(goos string) *Installer {
	in := New("test")
	in.GOOS = goos
	in.LookPath = func(name string) (string, error) {
		if p, ok := fs.tools[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	in.Run = fs.run
	in.Reach = func(context.Context, string) error {
		if fs.reachOK {
			return nil
		}
		return errors.New("dial tcp: lookup failed")
	}
	in.FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	return in
}

func (fs *fakeSystem) run(ctx context.Context, c Cmd) error {
	line := filepath.Base(c.Name) + " " + strings.Join(c.Args, " ")
	if strings.Contains(line, "sys.version_info") {
		if v, ok := fs.pyVer[c.Name]; ok {
			fmt.Fprintln(c.Out, v)
			return nil
		}
		return errors.New("no such python")
	}
	if strings.Contains(line, "import venv, ensurepip") {
		if fs.noVenv[c.Name] {
			return errors.New("No module named ensurepip")
		}
		return nil
	}
	fs.ran = append(fs.ran, line)
	fmt.Fprintf(c.Out, "working on %s\rfinished %s\n", c.Args[0], c.Args[0])
	if fs.failOn != "" && strings.Contains(line, fs.failOn) {
		return errors.New("exit status 1")
	}
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fs.t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			fs.t.Fatal(err)
		}
	}
	switch {
	case c.Args[0] == "clone":
		mk(filepath.Join(c.Args[len(c.Args)-1], "main.py"))
	case c.Args[0] == "venv" || strings.Contains(line, "-m venv"):
		mk(filepath.Join(c.Dir, ".venv", "bin", "python"))
	case strings.Contains(line, "torch"):
		mk(filepath.Join(c.Dir, ".venv", "lib", "python3.12", "site-packages", "torch", "__init__.py"))
	}
	return nil
}

func TestInstallComfyUIFreshWithUV(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	in := fs.installer("darwin")
	dir := filepath.Join(t.TempDir(), "share", "songsmith", "ComfyUI")
	logPath := filepath.Join(t.TempDir(), "state", "install.log")

	var details []string
	err := in.InstallComfyUI(t.Context(), dir, logPath, func(p Progress) { details = append(details, p.Step+"|"+p.Detail) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"git clone --depth 1 --progress " + DefaultComfyRepo + " " + filepath.Join(filepath.Dir(dir), ".ComfyUI.songsmith-clone"),
		"uv venv --seed --python 3.12 .venv",
		"uv pip install --python " + venvPython(dir) + " torch torchvision torchaudio",
		"uv pip install --python " + venvPython(dir) + " -r requirements.txt",
	}
	if strings.Join(fs.ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran:\n%s", strings.Join(fs.ran, "\n"))
	}
	if st := InspectComfyUI(dir); st.Status != ComfyInstalled {
		t.Fatalf("status = %v (%s)", st.Status, st.Detail)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "== Install torch") || !strings.Contains(string(log), "finished pip") {
		t.Fatalf("log:\n%s", log)
	}
	if !slices.Contains(details, "Install torch|finished pip") {
		t.Fatalf("details = %q", details)
	}
}

func TestInstallComfyUIFallsBackToSupportedPython(t *testing.T) {
	fs := newFakeSystem(t, "git", "python3", "python3.12")
	fs.pyVer["/fake/bin/python3"] = "3.15"
	fs.pyVer["/fake/bin/python3.12"] = "3.12"
	in := fs.installer("linux")
	dir := filepath.Join(t.TempDir(), "ComfyUI")
	if err := in.InstallComfyUI(t.Context(), dir, filepath.Join(t.TempDir(), "log"), nil); err != nil {
		t.Fatal(err)
	}
	if fs.ran[1] != "python3.12 -m venv .venv" || !strings.HasPrefix(fs.ran[2], "python -m pip install torch") {
		t.Fatalf("ran:\n%s", strings.Join(fs.ran, "\n"))
	}
}

func TestInstallComfyUIResumesAfterFailedPip(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	fs.failOn = "requirements.txt"
	in := fs.installer("darwin")
	dir := filepath.Join(t.TempDir(), "ComfyUI")
	logPath := filepath.Join(t.TempDir(), "install.log")

	err := in.InstallComfyUI(t.Context(), dir, logPath, nil)
	if err == nil || !strings.Contains(err.Error(), logPath) {
		t.Fatalf("err = %v", err)
	}
	if st := InspectComfyUI(dir); st.Status != ComfyIncomplete {
		t.Fatalf("status = %v", st.Status)
	}

	fs.failOn, fs.ran = "", nil
	if err := in.InstallComfyUI(t.Context(), dir, logPath, nil); err != nil {
		t.Fatal(err)
	}
	if len(fs.ran) != 2 || !strings.Contains(fs.ran[0], "torch") || !strings.Contains(fs.ran[1], "requirements.txt") {
		t.Fatalf("resume ran:\n%s", strings.Join(fs.ran, "\n"))
	}
	if st := InspectComfyUI(dir); st.Status != ComfyInstalled {
		t.Fatalf("status = %v", st.Status)
	}
}

func TestInstallComfyUISkipsInstalled(t *testing.T) {
	fs := newFakeSystem(t)
	in := fs.installer("darwin")
	dir := t.TempDir()
	for _, p := range []string{"main.py", ".venv/bin/python", ".venv/lib/python3.12/site-packages/torch/__init__.py"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), nil, 0o644)
	}
	var last Progress
	if err := in.InstallComfyUI(t.Context(), dir, filepath.Join(t.TempDir(), "log"), func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if len(fs.ran) != 0 || last.Phase != PhaseSkipped {
		t.Fatalf("ran %q, phase %s", fs.ran, last.Phase)
	}
}

func TestInstallComfyUIRefusesForeignFolder(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	in := fs.installer("darwin")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644)
	err := in.InstallComfyUI(t.Context(), dir, filepath.Join(t.TempDir(), "log"), nil)
	if err == nil || !strings.Contains(err.Error(), "not empty") || len(fs.ran) != 0 {
		t.Fatalf("err = %v, ran %q", err, fs.ran)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(b) != "mine" {
		t.Fatal("folder was changed")
	}
}

func TestInstallComfyUIUsesEmptyFolder(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	in := fs.installer("darwin")
	dir := t.TempDir()
	if err := in.InstallComfyUI(t.Context(), dir, filepath.Join(t.TempDir(), "log"), nil); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "main.py")) {
		t.Fatal("no checkout in the empty folder")
	}
}

func TestInstallComfyUIMissingPrerequisites(t *testing.T) {
	cases := []struct {
		name   string
		tools  []string
		pyVer  map[string]string
		noVenv bool
		goos   string
		want   []string
	}{
		{"no git on macOS", []string{"uv"}, nil, false, "darwin", []string{"git is not installed", "xcode-select --install"}},
		{"no git on Linux", []string{"uv"}, nil, false, "linux", []string{"git is not installed", "sudo apt install git"}},
		{"no python", []string{"git"}, nil, false, "darwin", []string{"neither uv nor Python 3.12 or 3.13", "brew install uv"}},
		{"python too new", []string{"git", "python3"}, map[string]string{"/fake/bin/python3": "3.15"}, false, "linux",
			[]string{"/fake/bin/python3 is Python 3.15", "astral.sh/uv"}},
		{"no venv module", []string{"git", "python3"}, map[string]string{"/fake/bin/python3": "3.12"}, true, "linux",
			[]string{"no working venv module", "python3.12-venv"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeSystem(t, tc.tools...)
			for k, v := range tc.pyVer {
				fs.pyVer[k] = v
				fs.noVenv[k] = tc.noVenv
			}
			err := fs.installer(tc.goos).InstallComfyUI(t.Context(), filepath.Join(t.TempDir(), "ComfyUI"), filepath.Join(t.TempDir(), "log"), nil)
			var missing *MissingError
			if !errors.As(err, &missing) {
				t.Fatalf("err = %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%q missing from %q", w, err)
				}
			}
			if len(fs.ran) != 0 {
				t.Fatalf("ran %q", fs.ran)
			}
		})
	}
}

func TestInstallComfyUIOffline(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	fs.reachOK = false
	err := fs.installer("darwin").InstallComfyUI(t.Context(), filepath.Join(t.TempDir(), "ComfyUI"), filepath.Join(t.TempDir(), "log"), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot reach github.com") || len(fs.ran) != 0 {
		t.Fatalf("err = %v, ran %q", err, fs.ran)
	}
}

func TestInstallComfyUIInsufficientSpace(t *testing.T) {
	fs := newFakeSystem(t, "git", "uv")
	in := fs.installer("darwin")
	in.FreeSpace = func(string) (uint64, error) { return 1e9, nil }
	err := in.InstallComfyUI(t.Context(), filepath.Join(t.TempDir(), "a", "b"), filepath.Join(t.TempDir(), "log"), nil)
	if err == nil || !strings.Contains(err.Error(), "not enough disk space") {
		t.Fatalf("err = %v", err)
	}
}

func TestLastLineSplitsCarriageReturns(t *testing.T) {
	var got []string
	w := &lastLine{report: func(s string) { got = append(got, s) }}
	io.WriteString(w, "Receiving objects:  10%\rReceiving obj")
	io.WriteString(w, "ects: 100%\n\nResolving")
	w.flush()
	if strings.Join(got, "|") != "Receiving objects:  10%|Receiving objects: 100%|Resolving" {
		t.Fatalf("got %q", got)
	}
}
