package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/setup"
)

func comfyCfg(dir string) config.ComfyUI {
	c := config.Default().ComfyUI
	c.Dir, c.Port = dir, 18188
	return c
}

func TestInstallCheck(t *testing.T) {
	c := comfyCfg("/x")
	ch := installCheck(c, setup.ComfyState{Status: setup.ComfyMissing})
	if ch.Result != Fail || !ch.Blocking || ch.Fix != FixComfyUI || ch.Missing != "ComfyUI" ||
		ch.Detail != "No ComfyUI in /x. Set the folder in Settings → Server, or install it there from Setup." {
		t.Fatalf("no main.py: %+v", ch)
	}
	ch = installCheck(c, setup.ComfyState{Status: setup.ComfyIncomplete, Detail: "Python packages not fully installed"})
	if ch.Result != Fail || ch.Fix != FixComfyUI || !inOrder(ch.Detail, "not finished", "Python packages", "Finish setup", "shell: `songsmith setup") {
		t.Fatalf("incomplete: %+v", ch)
	}
	ch = installCheck(c, setup.ComfyState{Status: setup.ComfyBlocked, Detail: "/x holds something else"})
	if ch.Result != Fail || ch.Fix != "" || ch.Missing != "" || ch.Detail != "/x holds something else" {
		t.Fatalf("blocked: %+v", ch)
	}
	if ch := installCheck(c, setup.ComfyState{Status: setup.ComfyInstalled}); ch.Result != OK || ch.Detail != "/x" {
		t.Fatalf("ok: %+v", ch)
	}
}

func writeSized(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestModelChecks(t *testing.T) {
	dir := t.TempDir()
	c := comfyCfg(dir)
	vae, _ := setup.Lookup(setup.DirVAE, c.VAE)
	writeSized(t, filepath.Join(dir, "models", "vae", c.VAE), vae.Size)
	dit, _ := setup.Lookup(setup.DirDiffusion, c.DiffusionModel)
	writeSized(t, setup.PartPath(dir, dit), dit.Size/4)
	checks := modelChecks(c)
	if len(checks) != 3 {
		t.Fatalf("checks = %+v", checks)
	}
	for _, ch := range checks[:2] {
		if ch.Result != Fail || !ch.Blocking || ch.Fix != FixMusicModel || ch.Missing != "music model files" ||
			!inOrder(ch.Detail, "songsmith can download it from Comfy-Org/MiniMax-Music-3", "Finish setup", "shell: `hf download") {
			t.Errorf("missing file: %+v", ch)
		}
	}
	if !strings.Contains(checks[0].Detail, "25% downloaded") {
		t.Errorf("partial file: %+v", checks[0])
	}
	if ch := checks[2]; ch.ID != IDVAE || ch.Name != "Model vae" || ch.Result != OK || ch.Detail != c.VAE+" (0.2 GB)" {
		t.Errorf("present file: %+v", ch)
	}

	writeSized(t, filepath.Join(dir, "models", "vae", c.VAE), 1000)
	if ch := modelChecks(c)[2]; ch.Result != Fail || !strings.Contains(ch.Detail, "expected 0.22 GB") {
		t.Errorf("wrong size: %+v", ch)
	}

	c.TextEncoder = "custom.safetensors"
	if ch := modelChecks(c)[1]; ch.Result != Fail || ch.Fix != "" || !strings.Contains(ch.Detail, "cannot download it") {
		t.Errorf("unknown missing file: %+v", ch)
	}
	writeSized(t, filepath.Join(dir, "models", "text_encoders", "custom.safetensors"), 10)
	if ch := modelChecks(c)[1]; ch.Result != OK {
		t.Errorf("unknown present file: %+v", ch)
	}
}

// TestChecksAgreeWithThePlan: a check is OK exactly when songsmith setup
// has nothing left to do for it.
func TestChecksAgreeWithThePlan(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"models":[{"name":"writer:latest"}]}`)
	}))
	defer ollama.Close()
	in := setup.New("test")
	in.Reach = func(context.Context, string) error { return nil }
	in.FreeSpace = func(string) (uint64, error) { return 1 << 50, nil }
	in.LookPath = func(f string) (string, error) { return "/bin/" + f, nil }

	dir := t.TempDir()
	cfg := config.Default()
	cfg.ComfyUI = comfyCfg(dir)
	cfg.Ollama.URL, cfg.Ollama.Model = ollama.URL, "writer"
	vae, _ := setup.Lookup(setup.DirVAE, cfg.ComfyUI.VAE)
	dit, _ := setup.Lookup(setup.DirDiffusion, cfg.ComfyUI.DiffusionModel)

	stages := []func(){
		func() {},
		func() { writeSized(t, filepath.Join(dir, "main.py"), 1) },
		func() {
			writeSized(t, filepath.Join(dir, ".venv", "bin", "python"), 1)
			writeSized(t, filepath.Join(dir, ".venv", "lib", "python3.12", "site-packages", "torch", "__init__.py"), 1)
		},
		func() { writeSized(t, filepath.Join(dir, "models", "vae", vae.Name), vae.Size) },
		func() { writeSized(t, setup.PartPath(dir, dit), 10) },
		func() { writeSized(t, filepath.Join(dir, "models", "vae", vae.Name), 10) },
	}
	pairs := map[setup.ActionID]string{
		setup.ActionComfyUI: IDComfyUI, setup.ActionDiffusion: IDDiffusionModel, setup.ActionTextEncoder: IDTextEncoder,
		setup.ActionVAE: IDVAE, setup.ActionWriter: IDWriter,
	}
	for i, stage := range stages {
		stage()
		checks := []Check{installCheck(cfg.ComfyUI, setup.InspectComfyUI(dir))}
		checks = append(checks, modelChecks(cfg.ComfyUI)...)
		checks = append(checks, writerCheck(cfg.Ollama, []string{"writer:latest"}, nil))
		for _, a := range in.Plan(t.Context(), cfg).Actions {
			ch, ok := Find(checks, pairs[a.ID])
			if !ok {
				t.Fatalf("stage %d: no check for %s", i, a.ID)
			}
			if (ch.Result == OK) != (a.Status == setup.StatusDone) {
				t.Errorf("stage %d: %s check %v (%s) but plan %v (%s)", i, a.ID, ch.Result, ch.Detail, a.Status, a.Detail)
			}
		}
	}
}

func TestServerCheck(t *testing.T) {
	c := comfyCfg("/x")
	cases := []struct {
		st   server.Status
		want Result
		in   string
	}{
		{server.Status{}, Warn, "not running at http://127.0.0.1:18188"},
		{server.Status{Running: true, Device: "mps"}, Fail, server.RequiredFlag},
		{server.Status{Running: true, Device: "mps", GPUOnly: true}, OK, "on mps"},
		{server.Status{Running: true, Device: "cuda"}, OK, "on cuda"},
	}
	for _, tc := range cases {
		ch := serverCheck(c, tc.st)
		if ch.Result != tc.want || !strings.Contains(ch.Detail, tc.in) || ch.Blocking || ch.Fix != "" {
			t.Errorf("%+v: %+v", tc.st, ch)
		}
	}
}

func TestWriterCheck(t *testing.T) {
	o := config.Default().Ollama
	if ch := writerCheck(o, nil, errors.New("connection refused")); ch.Result != Warn || ch.Fix != "" || ch.Blocking || ch.Missing != "Ollama" ||
		!inOrder(ch.Detail, "cannot install it", "https://ollama.com", "written by hand") {
		t.Fatalf("unreachable: %+v", ch)
	}
	if ch := writerCheck(o, []string{"other:1b", "another:7b"}, nil); ch.Result != Warn || ch.Fix != FixWriterModel || ch.Missing != "writer model "+o.Model ||
		!inOrder(ch.Detail, "2 other models are", "Settings → Writer", "Finish setup", "shell: `ollama pull "+o.Model) {
		t.Fatalf("other models installed: %+v", ch)
	}
	if ch := writerCheck(o, nil, nil); ch.Fix != FixWriterModel || strings.Contains(ch.Detail, "Settings → Writer") || !inOrder(ch.Detail, "no models", "Finish setup", "ollama pull") {
		t.Fatalf("no models: %+v", ch)
	}
	if ch := writerCheck(o, []string{o.Model}, nil); ch.Result != OK || ch.Detail != o.Model+" at "+o.URL {
		t.Fatalf("ok: %+v", ch)
	}
}

type fakeInfo map[string]comfy.NodeInfo

func (f fakeInfo) ObjectInfo(_ context.Context, class string) (comfy.NodeInfo, bool, error) {
	info, ok := f[class]
	return info, ok, nil
}

func nodeInfo(t *testing.T, inputs []string, combos map[string][]string) comfy.NodeInfo {
	t.Helper()
	var n comfy.NodeInfo
	n.Input.Required = map[string]json.RawMessage{}
	for _, in := range inputs {
		n.Input.Required[in] = json.RawMessage(`["INT"]`)
	}
	for in, opts := range combos {
		b, _ := json.Marshal([]any{opts})
		n.Input.Required[in] = b
	}
	return n
}

func TestNodeChecks(t *testing.T) {
	c := comfyCfg("/x")
	all := fakeInfo{}
	for class, inputs := range comfy.RequiredNodes {
		all[class] = nodeInfo(t, inputs, nil)
	}
	if got := nodeChecks(t.Context(), all, c); len(got) != 1 || got[0].Result != OK {
		t.Fatalf("all present: %+v", got)
	}

	partial := fakeInfo{}
	for class, info := range all {
		partial[class] = info
	}
	delete(partial, "SaveAudio")
	partial["VAELoader"] = nodeInfo(t, nil, map[string][]string{"vae_name": {"other.safetensors"}})
	got := nodeChecks(t.Context(), partial, c)
	if len(got) != 2 || got[0].Result != Fail || !strings.Contains(got[0].Detail, "SaveAudio") {
		t.Fatalf("missing node: %+v", got)
	}
	if got[1].ID != IDServerModelFiles || !got[1].Blocking || !strings.Contains(got[1].Detail, c.VAE) {
		t.Fatalf("unseen file: %+v", got[1])
	}
}

// inOrder reports whether s contains each part, in order: the in-app fix
// comes before the shell command.
func inOrder(s string, parts ...string) bool {
	for _, p := range parts {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return true
}

func TestFFprobeCheck(t *testing.T) {
	if ch := ffprobeCheck(nil); ch.Result != OK || ch.Missing != "" {
		t.Fatalf("found: %+v", ch)
	}
	if ch := ffprobeCheck(errors.New("not found")); ch.Result != Warn || ch.Blocking || ch.Fix != "" || ch.Missing != "ffprobe" || !strings.Contains(ch.Detail, "cannot install it") {
		t.Fatalf("missing: %+v", ch)
	}
}

func TestFFmpegCheck(t *testing.T) {
	if ch := ffmpegCheck(nil); ch.Result != OK || ch.Missing != "" {
		t.Fatalf("found: %+v", ch)
	}
	ch := ffmpegCheck(errors.New("not found"))
	if ch.Result != Warn || ch.Blocking || ch.Fix != "" || ch.Missing != "ffmpeg" {
		t.Fatalf("missing: %+v", ch)
	}
	for _, want := range []string{"brew install ffmpeg", "apt install ffmpeg", "master"} {
		if !strings.Contains(ch.Detail, want) {
			t.Fatalf("detail %q lacks %q", ch.Detail, want)
		}
	}
}

func TestMissing(t *testing.T) {
	checks := []Check{
		{Result: Fail, Missing: "ComfyUI", Fix: FixComfyUI},
		{Result: Fail, Missing: "music model files", Fix: FixMusicModel},
		{Result: Fail, Missing: "music model files", Fix: FixMusicModel},
		{Result: Warn},
		{Result: OK, Missing: "ignored"},
		{Result: Warn, Missing: "Ollama"},
		{Result: Warn, Missing: "ffprobe"},
	}
	in, out := Missing(checks)
	if strings.Join(in, ",") != "ComfyUI,music model files" || strings.Join(out, ",") != "Ollama,ffprobe" {
		t.Fatalf("installable %q elsewhere %q", in, out)
	}
}

func TestSummaries(t *testing.T) {
	checks := []Check{
		{ID: "a", Result: OK, Blocking: true},
		{ID: "b", Result: Fail},
		{ID: "c", Result: Fail, Blocking: true},
		{ID: "d", Result: Fail, Blocking: true},
	}
	if b, ok := Blocker(checks); !ok || b.ID != "c" {
		t.Fatalf("blocker = %+v", b)
	}
	if _, ok := Blocker(checks[:2]); ok {
		t.Fatal("non-blocking failure reported as blocker")
	}
	if !Failed(checks[1:2]) || Failed(checks[:1]) {
		t.Fatal("Failed")
	}
	if c, ok := Find(checks, "b"); !ok || c.ID != "b" {
		t.Fatal("Find")
	}
	if _, ok := Find(checks, "z"); ok {
		t.Fatal("Find missing")
	}
}

func TestInstructionsCheck(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(empty, []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("Write sea shanties."), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := instructionsCheck(""); ok {
		t.Fatal("check reported with no instructions file set")
	}
	for _, p := range []string{filepath.Join(dir, "gone.txt"), empty, dir} {
		ch, ok := instructionsCheck(p)
		if !ok || ch.ID != IDInstructions || ch.Result != Warn || ch.Blocking || ch.Missing != "" ||
			!inOrder(ch.Detail, p, "drafts use the built-in instructions", "Settings → Writer") {
			t.Errorf("%s: %+v", p, ch)
		}
	}
	if ch, ok := instructionsCheck(good); !ok || ch.Result != OK || ch.Detail != good {
		t.Errorf("good file: %+v", ch)
	}
}
