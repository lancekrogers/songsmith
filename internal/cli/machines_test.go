package cli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

// machinesEnv is a config whose own ComfyUI server is on a closed port and
// whose Ollama never answers, so nothing reaches a real server.
type machinesEnv struct {
	t    *testing.T
	dir  string
	path string
}

func newMachinesEnv(t *testing.T) *machinesEnv {
	dir := t.TempDir()
	e := &machinesEnv{t: t, dir: dir, path: filepath.Join(dir, "config.yaml")}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	for _, kv := range []struct {
		key   string
		value any
	}{
		{"library_dir", filepath.Join(dir, "library")}, {"state_dir", filepath.Join(dir, "state")},
		{"comfyui.dir", filepath.Join(dir, "ComfyUI")}, {"comfyui.host", "127.0.0.1"}, {"comfyui.port", port},
		{"ollama.url", "http://127.0.0.1:1"},
	} {
		if err := config.Set(e.path, kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *machinesEnv) run(args ...string) (string, error) {
	e.t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--config", e.path}, args...))
	err := root.Execute()
	return out.String(), err
}

func (e *machinesEnv) config() config.Config {
	e.t.Helper()
	cfg, err := config.Load(e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

func TestMachinesAddChecksThenSaves(t *testing.T) {
	e := newMachinesEnv(t)
	srv := comfytest.New(t, "mbp")
	srv.SetMemory(32 * comfytest.GiB)
	out, err := e.run("machines", "add", "MacBook Pro", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Checking MacBook Pro at " + srv.URL, "ready at " + srv.URL, "32 GB", "Saved MacBook Pro", "Tailscale"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	cfg := e.config()
	if len(cfg.Machines) != 1 || cfg.Machines[0].URL != srv.URL || !cfg.Machines[0].Enabled {
		t.Fatalf("saved %+v", cfg.Machines)
	}

	down := comfytest.New(t, "attic")
	down.Close()
	out, err = e.run("machines", "add", "Attic", down.URL, "--vae", "attic_vae.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Attic is offline: not answering at "+down.URL) || !strings.Contains(out, "Saved Attic") {
		t.Fatalf("adding an offline machine:\n%s", out)
	}
	if m, _ := e.config().Machine("Attic"); m.VAE != "attic_vae.safetensors" {
		t.Fatalf("attic %+v", m)
	}

	before, _ := os.ReadFile(e.path)
	for _, args := range [][]string{
		{"machines", "add", "macbook pro", "http://elsewhere:8188"},
		{"machines", "add", "Third", srv.URL},
		{"machines", "add", config.ThisComputer, "http://elsewhere:8188"},
		{"machines", "add", "Bad", "ftp://elsewhere"},
	} {
		if _, err := e.run(args...); err == nil {
			t.Errorf("%v was saved", args)
		}
	}
	if after, _ := os.ReadFile(e.path); !bytes.Equal(before, after) {
		t.Fatal("a refused machine changed the config")
	}
}

func TestMachinesListsEachWithItsStatus(t *testing.T) {
	e := newMachinesEnv(t)
	out, err := e.run("machines")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, config.ThisComputer) || !strings.Contains(out, "stopped") || !strings.Contains(out, "No other machines yet") {
		t.Fatalf("no machines:\n%s", out)
	}

	ready := comfytest.New(t, "mbp")
	old := comfytest.New(t, "old")
	old.SetMissingNode("VAEDecodeAudioTiled")
	gone := comfytest.New(t, "gone")
	gone.Close()
	for _, m := range []struct{ name, url string }{{"MacBook Pro", ready.URL}, {"Old Mini", old.URL}, {"Laptop", gone.URL}} {
		if _, err := e.run("machines", "add", m.name, m.url); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Set(e.path, "local.enabled", false); err != nil {
		t.Fatal(err)
	}
	out, err = e.run("machines")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	row := func(name string) string {
		for _, l := range lines {
			if strings.HasPrefix(l, name+"  ") {
				return l
			}
		}
		t.Fatalf("no row for %s:\n%s", name, out)
		return ""
	}
	if r := row(config.ThisComputer); !strings.Contains(r, "stopped") || !strings.HasSuffix(strings.TrimSpace(r), "off") {
		t.Errorf("this computer: %q", r)
	}
	if r := row("MacBook Pro"); !strings.Contains(r, "ready") || !strings.Contains(r, "not measured yet") {
		t.Errorf("ready machine: %q", r)
	}
	if r := row("Old Mini"); !strings.Contains(r, "ComfyUI too old") {
		t.Errorf("old machine: %q", r)
	}
	if r := row("Laptop"); !strings.Contains(r, "offline") {
		t.Errorf("offline machine: %q", r)
	}
	if !strings.Contains(out, "Laptop: not answering at "+gone.URL) || !strings.Contains(out, "Old Mini: ComfyUI at "+old.URL+" lacks VAEDecodeAudioTiled") {
		t.Errorf("details:\n%s", out)
	}
}

func TestMachinesRemove(t *testing.T) {
	e := newMachinesEnv(t)
	srv := comfytest.New(t, "mbp")
	if _, err := e.run("machines", "add", "MacBook Pro", srv.URL); err != nil {
		t.Fatal(err)
	}
	out, err := e.run("machines", "remove", "macbook pro")
	if err != nil || !strings.Contains(out, "Removed MacBook Pro") {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if len(e.config().Machines) != 0 {
		t.Fatal("still saved")
	}
	if _, err := e.run("machines", "remove", "Nope"); err == nil || !strings.Contains(err.Error(), "the machines are: "+config.ThisComputer) {
		t.Fatalf("remove an unknown machine: %v", err)
	}
}

func (e *machinesEnv) songFiles() (lyrics, caption string) {
	lyrics, caption = filepath.Join(e.dir, "lyrics.txt"), filepath.Join(e.dir, "caption.txt")
	os.WriteFile(lyrics, []byte("[Verse]\nla la la"), 0o644)
	os.WriteFile(caption, []byte("Global Metadata: Folk\n\nVocal Details: Soft lead\n\nArrangement: Verse: guitar."), 0o644)
	return lyrics, caption
}

func TestGenerateOnAPinnedMachine(t *testing.T) {
	e := newMachinesEnv(t)
	srv := comfytest.New(t, "mbp")
	if _, err := e.run("machines", "add", "MacBook Pro", srv.URL); err != nil {
		t.Fatal(err)
	}
	lyrics, caption := e.songFiles()
	out, err := e.run("generate", "--machine", "macbook pro", "--title", "Remote", "--lyrics", lyrics, "--caption", caption, "--seconds", "30")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "It goes to MacBook Pro") || !strings.Contains(out, "Done in") {
		t.Fatalf("output:\n%s", out)
	}
	songs, _ := library.NewStore(e.config().LibraryDir).List()
	if len(songs) != 1 || songs[0].Machine != "MacBook Pro" || len(songs[0].Takes) != 1 || songs[0].Takes[0].Machine != "MacBook Pro" {
		t.Fatalf("songs %+v", songs)
	}
	if len(srv.Prompts()) != 1 {
		t.Fatalf("prompts %v", srv.Prompts())
	}
}

func TestGenerateRefusesAMachineItCannotUse(t *testing.T) {
	e := newMachinesEnv(t)
	gone := comfytest.New(t, "gone")
	if _, err := e.run("machines", "add", "Laptop", gone.URL); err != nil {
		t.Fatal(err)
	}
	gone.Close()
	lyrics, caption := e.songFiles()
	_, err := e.run("generate", "--machine", "Nope", "--lyrics", lyrics, "--caption", caption)
	if err == nil || !strings.Contains(err.Error(), `no machine named "Nope"`) {
		t.Fatalf("unknown machine: %v", err)
	}
	if songs, _ := library.NewStore(e.config().LibraryDir).List(); len(songs) != 0 {
		t.Fatal("a song was made for a machine that does not exist")
	}

	_, err = e.run("generate", "--machine", "Laptop", "--title", "Waits", "--lyrics", lyrics, "--caption", caption)
	if err == nil || !strings.Contains(err.Error(), "Laptop cannot take it: not answering at "+gone.URL) || !strings.Contains(err.Error(), "renders it once a machine can take it") {
		t.Fatalf("offline machine: %v", err)
	}
	songs, _ := library.NewStore(e.config().LibraryDir).List()
	if len(songs) != 1 || songs[0].Status != library.StatusQueued || songs[0].Machine != "Laptop" {
		t.Fatalf("the song should stay queued for its machine: %+v", songs)
	}

	if err := config.Set(e.path, "local.enabled", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run("generate", "--machine", config.ThisComputer, "--lyrics", lyrics, "--caption", caption); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("this computer turned off: %v", err)
	}
}
