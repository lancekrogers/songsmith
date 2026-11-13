package health

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
)

func comfyCfg(dir string) config.ComfyUI {
	c := config.Default().ComfyUI
	c.Dir, c.Port = dir, 18188
	return c
}

func TestInstallCheck(t *testing.T) {
	c := comfyCfg("/x")
	missing := errors.New("missing")
	ch := installCheck(c, "/x/.venv/bin/python", missing, nil)
	if ch.Result != Fail || !ch.Blocking || ch.Fix != FixComfyUI || ch.Missing != "ComfyUI" ||
		!inOrder(ch.Detail, "not installed in /x", "songsmith can install it", "Finish setup", "Settings → ComfyUI → Install folder") {
		t.Fatalf("no main.py: %+v", ch)
	}
	if ch := installCheck(c, "/x/.venv/bin/python", nil, missing); ch.Result != Fail || !inOrder(ch.Detail, "no Python environment at /x/.venv/bin/python", "Finish setup", "shell: `uv venv") {
		t.Fatalf("no venv: %+v", ch)
	}
	if ch := installCheck(c, "", nil, nil); ch.Result != OK || ch.Detail != "/x" {
		t.Fatalf("ok: %+v", ch)
	}
}

func TestModelChecks(t *testing.T) {
	dir := t.TempDir()
	c := comfyCfg(dir)
	vae := filepath.Join(dir, "models", "vae", c.VAE)
	if err := os.MkdirAll(filepath.Dir(vae), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vae, make([]byte, 200_000_000/1000), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if ch := checks[2]; ch.ID != IDVAE || ch.Name != "Model vae" || ch.Result != OK || ch.Detail != c.VAE+" (0.0 GB)" {
		t.Errorf("present file: %+v", ch)
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
