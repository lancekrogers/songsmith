package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const commented = `# songsmith settings
comfyui:
  dir: /srv/comfy # big disk
  port: 9000
ollama:
  # the drafting model
  model: gemma4:26b # fast enough
render:
  steps: 12
library_dir: ~/songs
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetKeepsCommentsAndOtherKeys(t *testing.T) {
	p := writeConfig(t, commented)
	if err := Set(p, "ollama.model", "llama3.3:latest"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"# songsmith settings", "# big disk", "# the drafting model", "# fast enough", "model: llama3.3:latest"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "gemma4") {
		t.Errorf("old value kept:\n%s", got)
	}
	// Defaults stay out of the file: only what it already named, plus the key.
	for _, absent := range []string{"temperature", "host", "cfg_scale", "state_dir"} {
		if strings.Contains(got, absent) {
			t.Errorf("default %q baked in:\n%s", absent, got)
		}
	}
	order := []string{"comfyui:", "ollama:", "render:", "library_dir:"}
	last := -1
	for _, k := range order {
		i := strings.Index(got, k)
		if i < last {
			t.Fatalf("key order changed:\n%s", got)
		}
		last = i
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ollama.Model != "llama3.3:latest" || cfg.ComfyUI.Port != 9000 || cfg.Render.Steps != 12 || cfg.ComfyUI.Dir != "/srv/comfy" {
		t.Fatalf("values after set: %+v", cfg)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestSetAddsNestedKeyToExistingMap(t *testing.T) {
	p := writeConfig(t, commented)
	if err := Set(p, "render.sampler", "dpmpp_2m"); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "render.cfg_scale", 2.5); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Render.Sampler != "dpmpp_2m" || cfg.Render.CFGScale != 2.5 || cfg.Render.Steps != 12 {
		t.Fatalf("render = %+v", cfg.Render)
	}
	if got := read(t, p); strings.Count(got, "render:") != 1 {
		t.Fatalf("render map duplicated:\n%s", got)
	}
}

func TestSetCreatesMissingFileAndMaps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "dir", "config.yaml")
	if err := Set(p, "ollama.temperature", 1.1); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.TrimSpace(got) != "ollama:\n  temperature: 1.1" {
		t.Fatalf("file:\n%s", got)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ollama.Temperature != 1.1 || cfg.Ollama.Model != Default().Ollama.Model {
		t.Fatalf("cfg = %+v", cfg.Ollama)
	}
}

func TestSetHandlesEmptyFileAndNullSection(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "comment only": "# nothing yet\n", "null section": "render:\nlibrary_dir: /x\n"} {
		t.Run(name, func(t *testing.T) {
			p := writeConfig(t, body)
			if err := Set(p, "render.steps", 40); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Render.Steps != 40 {
				t.Fatalf("steps = %d in:\n%s", cfg.Render.Steps, read(t, p))
			}
			if strings.Contains(body, "#") && !strings.Contains(read(t, p), "# nothing yet") {
				t.Fatalf("comment lost:\n%s", read(t, p))
			}
		})
	}
}

func TestSetRefusesBadKeysAndValues(t *testing.T) {
	p := writeConfig(t, commented)
	cases := []struct {
		key   string
		value any
	}{
		{"render.step", 3},
		{"ollama", "x"},
		{"nope.model", "x"},
		{"render.steps", "lots"},
		{"render.steps", 2.5},
		{"comfyui.port.extra", 1},
	}
	for _, c := range cases {
		if err := Set(p, c.key, c.value); err == nil {
			t.Errorf("Set(%q, %v) succeeded", c.key, c.value)
		}
	}
	if read(t, p) != commented {
		t.Fatal("refused writes changed the file")
	}
}

func TestSetRefusesInvalidFile(t *testing.T) {
	p := writeConfig(t, "comfyui: [unclosed\n")
	if err := Set(p, "render.steps", 3); err == nil {
		t.Fatal("want parse error")
	}
	if read(t, p) != "comfyui: [unclosed\n" {
		t.Fatal("invalid file was rewritten")
	}
}

func TestLiveAppliesSavedChangesButKeepsFixedSettings(t *testing.T) {
	p := writeConfig(t, commented)
	start, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	live := NewLive(p, start)
	if err := live.Set("ollama.model", "llama3.3"); err != nil {
		t.Fatal(err)
	}
	if err := live.Set("library_dir", "/elsewhere"); err != nil {
		t.Fatal(err)
	}
	got := live.Get()
	if got.Ollama.Model != "llama3.3" {
		t.Fatalf("model = %q", got.Ollama.Model)
	}
	if got.LibraryDir != start.LibraryDir {
		t.Fatalf("library dir changed while running: %q", got.LibraryDir)
	}
	if again, _ := Load(p); again.LibraryDir != "/elsewhere" {
		t.Fatalf("library dir not saved for the next start: %q", again.LibraryDir)
	}

	if err := live.Set("render.steps", "many"); err == nil {
		t.Fatal("want error")
	}
	if live.Get().Ollama.Model != "llama3.3" {
		t.Fatal("failed save changed the running settings")
	}
}

func TestLiveConcurrentAccess(t *testing.T) {
	live := NewLive(filepath.Join(t.TempDir(), "c.yaml"), Default())
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if i%2 == 0 {
				cfg := live.Get()
				cfg.Render.Steps = i
				live.Replace(cfg)
			} else {
				_ = live.Get().Render.Steps
			}
		})
	}
	wg.Wait()
}

func TestPath(t *testing.T) {
	t.Setenv("SONGSMITH_CONFIG", "/env/config.yaml")
	if Path("/flag.yaml") != "/flag.yaml" || Path("") != "/env/config.yaml" {
		t.Fatalf("Path = %q / %q", Path("/flag.yaml"), Path(""))
	}
	if NewLive("", Default()).Path() != "/env/config.yaml" {
		t.Fatal("live path ignores the env var")
	}
}

func TestSetRefusesMultiDocumentFile(t *testing.T) {
	body := "render:\n  steps: 3\n---\nrender:\n  steps: 9\n"
	p := writeConfig(t, body)
	err := Set(p, "render.steps", 4)
	if err == nil || !strings.Contains(err.Error(), "more than one YAML document") {
		t.Fatalf("err = %v", err)
	}
	if read(t, p) != body {
		t.Fatalf("file changed:\n%s", read(t, p))
	}
	single := writeConfig(t, "---\nrender:\n  steps: 3\n")
	if err := Set(single, "render.steps", 4); err != nil {
		t.Fatalf("leading document marker: %v", err)
	}
}

func TestSetWritesThroughASymlink(t *testing.T) {
	target := writeConfig(t, commented)
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Set(link, "render.steps", 40); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", fi.Mode(), err)
	}
	if !strings.Contains(read(t, target), "steps: 40") {
		t.Fatalf("target not updated:\n%s", read(t, target))
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	for _, dir := range []string{filepath.Dir(target), filepath.Dir(link)} {
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("%s holds %d entries, want 1", dir, len(entries))
		}
	}
}

func TestSetRefusesAnchoredValuePlainly(t *testing.T) {
	body := "render:\n  steps: &n 7\n  top_k: *n\n"
	p := writeConfig(t, body)
	err := Set(p, "render.steps", 9)
	if err == nil || !strings.Contains(err.Error(), "render.steps uses a YAML anchor") || strings.Contains(err.Error(), "unknown anchor") {
		t.Fatalf("err = %v", err)
	}
	if read(t, p) != body {
		t.Fatalf("file changed:\n%s", read(t, p))
	}
}

func TestSetKeepsRelativeInstructionsFileLiteral(t *testing.T) {
	p := writeConfig(t, "")
	if err := Set(p, "ollama.instructions_file", "prompts/songs.txt"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); !strings.Contains(got, "instructions_file: prompts/songs.txt") {
		t.Fatalf("file:\n%s", got)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(p), "prompts", "songs.txt"); cfg.Ollama.InstructionsFile != want {
		t.Fatalf("instructions file = %q, want %q", cfg.Ollama.InstructionsFile, want)
	}
}

func TestLiveConcurrentSetsKeepEveryChange(t *testing.T) {
	p := writeConfig(t, "")
	live := NewLive(p, Default())
	firstRead, secondDone := make(chan struct{}), make(chan struct{})
	var loads atomic.Int32
	// The first reload stalls until a second Set has finished, or briefly
	// when that Set has to wait for this one.
	live.load = func(path string) (Config, error) {
		cfg, err := Load(path)
		if loads.Add(1) == 1 {
			close(firstRead)
			select {
			case <-secondDone:
			case <-time.After(200 * time.Millisecond):
			}
		}
		return cfg, err
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := live.Set("ollama.model", "first"); err != nil {
			t.Error(err)
		}
	})
	<-firstRead
	wg.Go(func() {
		defer close(secondDone)
		if err := live.Set("render.sampler", "second"); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	got := live.Get()
	if got.Ollama.Model != "first" || got.Render.Sampler != "second" {
		t.Fatalf("running settings lost a change: model %q sampler %q", got.Ollama.Model, got.Render.Sampler)
	}
}
