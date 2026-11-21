package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
	"github.com/lancekrogers/songsmith/internal/tailnet"
)

// tailnetEnv is a tailnet of stand-ins: Den iMac ready, MacBook Air
// missing its VAE file, Kitchen PC with nothing listening, Garage Mini
// offline on Tailscale, and Studio Mac, saved already under its DNS name.
type tailnetEnv struct {
	*machinesEnv
	den, air *comfytest.Server
	kitchen  string
}

func newTailnetEnv(t *testing.T) *tailnetEnv {
	e := &tailnetEnv{machinesEnv: newMachinesEnv(t)}
	models := e.config().ComfyUI
	e.den = comfytest.New(t, "den")
	e.den.SetMemory(96 * comfytest.GiB)
	e.den.SetModels(models.DiffusionModel, models.TextEncoder, models.VAE)
	e.air = comfytest.New(t, "air")
	e.air.SetModels(models.DiffusionModel, models.TextEncoder)
	closed := comfytest.New(t, "kitchen")
	closed.Close()
	e.kitchen = closed.URL
	cs := []machines.Candidate{
		{Name: "Den iMac", Online: true, OS: "macOS", Address: "100.64.0.4", URLs: []string{e.den.URL}, Hosts: []string{"den-imac.example-tailnet.ts.net", "den-imac", "100.64.0.4"}},
		{Name: "Garage Mini", Online: false, OS: "macOS", Address: "100.64.0.7", URLs: []string{"http://127.0.0.1:1"}, Hosts: []string{"100.64.0.7"}},
		{Name: "Kitchen PC", Online: true, OS: "windows", Address: "100.64.0.6", URLs: []string{closed.URL}, Hosts: []string{"100.64.0.6"}},
		{Name: "MacBook Air", Online: true, OS: "macOS", Address: "100.64.0.5", URLs: []string{e.air.URL}, Hosts: []string{"100.64.0.5"}},
		{Name: "Studio Mac", Online: true, OS: "macOS", Address: "100.64.0.2", URLs: []string{"http://127.0.0.1:2"}, Hosts: []string{"studio.example-tailnet.ts.net", "studio", "100.64.0.2"}},
	}
	e.find = func(context.Context) ([]machines.Candidate, error) { return cs, nil }
	if err := config.Set(e.path, "machines", []config.Machine{{Name: "Studio Mac", URL: "http://studio.example-tailnet.ts.net:8188", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDiscoverListsEveryComputerAndAddsNothing(t *testing.T) {
	e := newTailnetEnv(t)
	before, _ := os.ReadFile(e.path)
	out, err := e.run("machines", "add", "--discover")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "Looking for computers on Tailscale...\nCOMPUTER") {
		t.Fatalf("output starts:\n%s", out)
	}
	row := func(name string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, name+"  ") {
				return l
			}
		}
		t.Fatalf("no row for %s:\n%s", name, out)
		return ""
	}
	for name, want := range map[string][]string{
		"Den iMac":    {"ready, mps, 96 GB, ComfyUI 0.3.99", e.den.URL},
		"MacBook Air": {"models missing", e.air.URL},
		"Kitchen PC":  {"not answering (connection refused)", e.kitchen},
		"Garage Mini": {"offline on Tailscale", "http://127.0.0.1:1"},
		"Studio Mac":  {"already added as Studio Mac"},
	} {
		for _, w := range want {
			if r := row(name); !strings.Contains(r, w) {
				t.Errorf("%s: %q lacks %q", name, r, w)
			}
		}
	}
	for _, want := range []string{
		`songsmith machines add --discover "Den iMac"`,
		`songsmith machines add <name> "Den iMac" --discover`,
		"Kitchen PC: Nothing answers at 100.64.0.6 on port ",
		"set comfyui.host to 100.64.0.6 in its songsmith config and run songsmith server start.",
		"MacBook Air: ComfyUI at " + e.air.URL + " cannot see minimax_music3_dav.safetensors",
		"ComfyUI has no login",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Garage Mini: ") || strings.Contains(out, "Studio Mac: ") {
		t.Errorf("a note for a computer that was not asked:\n%s", out)
	}
	if after, _ := os.ReadFile(e.path); !bytes.Equal(before, after) {
		t.Fatal("listing changed the config")
	}
}

func TestDiscoverAddsAComputerByItsName(t *testing.T) {
	e := newTailnetEnv(t)
	out, err := e.run("machines", "add", "--discover", "den imac")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"Checking Den iMac at " + e.den.URL, "Den iMac is ready at " + e.den.URL, "Saved Den iMac", "ComfyUI has no login"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if m, ok := e.config().Machine("Den iMac"); !ok || m.URL != e.den.URL || !m.Enabled {
		t.Fatalf("saved %+v %v", m, ok)
	}
}

func TestDiscoverAddsAComputerUnderAnotherName(t *testing.T) {
	e := newTailnetEnv(t)
	out, err := e.run("machines", "add", "Den", "den-imac.example-tailnet.ts.net.", "--discover", "--vae", "den_vae.safetensors")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Checking Den at "+e.den.URL) || !strings.Contains(out, "Den is models missing") {
		t.Fatalf("the check should use the VAE given:\n%s", out)
	}
	m, ok := e.config().Machine("Den")
	if !ok || m.URL != e.den.URL || m.VAE != "den_vae.safetensors" {
		t.Fatalf("saved %+v %v", m, ok)
	}
	if _, ok := e.config().Machine("Den iMac"); ok {
		t.Fatal("saved under its Tailscale name as well")
	}
}

func TestDiscoverSavesAComputerThatDoesNotAnswerWithItsFix(t *testing.T) {
	e := newTailnetEnv(t)
	out, err := e.run("machines", "add", "--discover", "100.64.0.6")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Kitchen PC is not answering (connection refused). Nothing answers at 100.64.0.6") || !strings.Contains(out, "Saved Kitchen PC") {
		t.Fatalf("output:\n%s", out)
	}
	out, err = e.run("machines", "add", "--discover", "Garage Mini")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Garage Mini is offline on Tailscale, so songsmith did not ask it") || strings.Contains(out, "Checking") {
		t.Fatalf("output:\n%s", out)
	}
	if m, ok := e.config().Machine("Garage Mini"); !ok || m.URL != "http://127.0.0.1:1" {
		t.Fatalf("saved %+v %v", m, ok)
	}
}

func TestDiscoverRefusesAComputerItCannotPick(t *testing.T) {
	e := newTailnetEnv(t)
	before, _ := os.ReadFile(e.path)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--discover", "Nope"}, `no computer on Tailscale is called "Nope"; the computers found are: Den iMac (100.64.0.4), Garage Mini (100.64.0.7)`},
		{[]string{"--discover", "studio"}, "Studio Mac is already added, as Studio Mac"},
		{[]string{"Big Mac", "100.64.0.2", "--discover"}, "Studio Mac is already added, as Studio Mac"},
		{[]string{"a", "b", "c", "--discover"}, "--discover takes at most a name and a computer"},
		{[]string{"Den iMac"}, "accepts 2 arg(s), received 1"},
	} {
		_, err := e.run(append([]string{"machines", "add"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.args, err)
		}
	}

	twins := []machines.Candidate{
		{Name: "imac", Online: true, Address: "100.64.0.4", URLs: []string{"http://127.0.0.1:1"}, Hosts: []string{"imac.example-tailnet.ts.net", "100.64.0.4"}},
		{Name: "imac", Online: true, Address: "100.64.0.8", URLs: []string{"http://127.0.0.1:3"}, Hosts: []string{"imac-1.example-tailnet.ts.net", "100.64.0.8"}},
	}
	e.find = func(context.Context) ([]machines.Candidate, error) { return twins, nil }
	_, err := e.run("machines", "add", "--discover", "IMAC")
	if err == nil || !strings.Contains(err.Error(), "2 computers on Tailscale match \"IMAC\": imac (100.64.0.4), imac (100.64.0.8); give its full Tailscale name or its address instead") {
		t.Fatalf("ambiguous: %v", err)
	}
	if after, _ := os.ReadFile(e.path); !bytes.Equal(before, after) {
		t.Fatal("a refused computer changed the config")
	}
}

func TestDiscoverWithoutTailscaleSaysSoInOneSentence(t *testing.T) {
	e := newMachinesEnv(t)
	r := tailnet.Reader{
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		Exists:   func(string) bool { return false },
	}
	e.find = func(ctx context.Context) ([]machines.Candidate, error) {
		_, err := r.Peers(ctx)
		return nil, err
	}
	_, err := e.run("machines", "add", "--discover")
	if err == nil || err.Error() != "Tailscale is not installed on this computer; install it from tailscale.com, or add a machine by its address" {
		t.Fatalf("%v", err)
	}

	e.find = func(context.Context) ([]machines.Candidate, error) { return nil, nil }
	out, err := e.run("machines", "add", "--discover")
	if err != nil || !strings.Contains(out, "Tailscale lists no other computers that could run ComfyUI. Add one by its address with `songsmith machines add <name> <address>`.") {
		t.Fatalf("no computers: %v\n%s", err, out)
	}
	if out, _ := e.run("machines"); !strings.Contains(out, "songsmith machines add --discover") {
		t.Fatalf("the no-machines hint does not mention --discover:\n%s", out)
	}
}

func TestTheDemoTailnetIsLoopbackOnly(t *testing.T) {
	e := newMachinesEnv(t)
	run := func(file string) (string, error) {
		t.Setenv(demoTailnetEnv, file)
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"--config", e.path, "machines", "add", "--discover"})
		err := root.Execute()
		return out.String(), err
	}
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "tailnet.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, u := range []string{"http://100.64.0.4:8188", "https://127.0.0.1:18516", "http://den.example-tailnet.ts.net:8188"} {
		_, err := run(write(`[{"name":"Den iMac","online":true,"address":"100.64.0.4","urls":["` + u + `"]}]`))
		if err == nil || !strings.Contains(err.Error(), "must be an http://127.0.0.1 address") {
			t.Errorf("%s: %v", u, err)
		}
	}
	srv := comfytest.New(t, "den")
	out, err := run(write(`[{"name":"Den iMac","os":"macOS","online":true,"address":"100.64.0.4","urls":["` + srv.URL + `"]}]`))
	if err != nil || !strings.Contains(out, "Den iMac") || !strings.Contains(out, srv.URL) {
		t.Fatalf("%v\n%s", err, out)
	}
	if f, err := (func() (machines.Finder, error) { t.Setenv(demoTailnetEnv, ""); return demoFinder() })(); f != nil || err != nil {
		t.Fatalf("unset, the seam should do nothing: %v", err)
	}
}
