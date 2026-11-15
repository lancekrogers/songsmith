package config

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNoMachinesLoadsAsBefore(t *testing.T) {
	cfg, err := Load(writeConfig(t, "render:\n  steps: 12\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Machines) != 0 || !cfg.Local.Enabled || cfg.ReclaimAfter != 10*time.Minute {
		t.Fatalf("machines %v, local %v, reclaim %s", cfg.Machines, cfg.Local, cfg.ReclaimAfter)
	}
}

func TestMachinesLoadWithDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `machines:
  - name: MacBook Pro
    url: http://macbook-pro-2:8188/
  - name: Attic
    url: attic.tailnet.ts.net:8190
    enabled: false
    vae: other_vae.safetensors
local:
  enabled: false
reclaim_after: 90s
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Machines) != 2 || cfg.Local.Enabled || cfg.ReclaimAfter != 90*time.Second {
		t.Fatalf("loaded %+v", cfg)
	}
	mbp, attic := cfg.Machines[0], cfg.Machines[1]
	if !mbp.Enabled || mbp.URL != "http://macbook-pro-2:8188" {
		t.Fatalf("a machine without enabled: %+v", mbp)
	}
	if attic.Enabled || attic.URL != "http://attic.tailnet.ts.net:8190" {
		t.Fatalf("attic %+v", attic)
	}
	models := attic.Models(cfg.ComfyUI)
	if models.VAE != "other_vae.safetensors" || models.DiffusionModel != cfg.ComfyUI.DiffusionModel || models.TextEncoder != cfg.ComfyUI.TextEncoder {
		t.Fatalf("attic models %+v", models)
	}
	if m, ok := cfg.Machine("macbook pro"); !ok || m.Name != "MacBook Pro" {
		t.Fatal("lookup by name ignores case")
	}
}

func TestBadMachinesAreRefusedClearly(t *testing.T) {
	cases := []struct{ yaml, want string }{
		{"machines:\n  - name: A\n    url: http://a:8188\n  - name: a\n    url: http://b:8188\n", `two machines are named "A" and "a"`},
		{"machines:\n  - name: A\n    url: ftp://a:8188\n", "starts with ftp://"},
		{"machines:\n  - name: A\n    url: http://a:8188/prompt\n", "more than a host and port"},
		{"machines:\n  - name: A\n    url: http://:8188\n", "no host name"},
		{"machines:\n  - name: A\n    url: \"\"\n", "address is empty"},
		{"machines:\n  - name: This Computer\n    url: http://a:8188\n", "name songsmith uses for the computer it runs on"},
		{"machines:\n  - url: http://a:8188\n", "needs a name"},
		{"machines:\n  - name: A\n    url: http://a:8188\n  - name: B\n    url: http://A:8188\n", `also the address of machine "A"`},
		{"machines:\n  - name: A\n    url: http://localhost:8188\n", "this computer's own ComfyUI server"},
		{"reclaim_after: 0s\n", "reclaim_after is 0s"},
		{"reclaim_after: soon\n", "`soon` into time.Duration"},
	}
	for _, c := range cases {
		_, err := Load(writeConfig(t, c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.yaml, err, c.want)
		}
	}
	if _, err := Load(writeConfig(t, "machines:\n  - name: GPU 2\n    url: http://127.0.0.1:8190\n")); err != nil {
		t.Fatalf("a second server on this computer's other port: %v", err)
	}
}

func TestSetSavesMachinesAndRefusesBadOnes(t *testing.T) {
	p := writeConfig(t, commented)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := cfg.WithMachine(Machine{Name: "MacBook Pro", URL: "macbook-pro-2:8188", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "machines", ms); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "reclaim_after", 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "local.enabled", false); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Machines) != 1 || cfg.Machines[0].URL != "http://macbook-pro-2:8188" || !cfg.Machines[0].Enabled ||
		cfg.ReclaimAfter != 2*time.Minute || cfg.Local.Enabled {
		t.Fatalf("saved %+v", cfg)
	}
	if !strings.Contains(read(t, p), "# the drafting model") {
		t.Fatal("comments lost")
	}

	if _, err := cfg.WithMachine(Machine{Name: "macbook pro", URL: "http://other:8188"}); err == nil {
		t.Fatal("added a second machine with the same name")
	}
	before := read(t, p)
	if err := Set(p, "machines", append(cfg.Machines, Machine{Name: "Dup", URL: "http://macbook-pro-2:8188"})); err == nil {
		t.Fatal("Set saved two machines at one address")
	}
	if err := Set(p, "reclaim_after", -time.Second); err == nil {
		t.Fatal("Set saved a negative reclaim_after")
	}
	if read(t, p) != before {
		t.Fatal("a refused change was written")
	}

	if _, err := cfg.WithoutMachine("MACBOOK PRO"); !errors.Is(err, ErrOnlyMachine) {
		t.Fatalf("removed the only machine that renders new songs: %v", err)
	}
	cfg.Local.Enabled = true
	ms, err = cfg.WithoutMachine("MACBOOK PRO")
	if err != nil || len(ms) != 0 {
		t.Fatalf("remove: %v %v", ms, err)
	}
	if _, err := cfg.WithoutMachine("Nope"); err == nil {
		t.Fatal("removed a machine that is not there")
	}
}

func TestMachineChangesKeepMachinesAddedElsewhere(t *testing.T) {
	p := writeConfig(t, commented)
	if _, err := AddMachine(p, Machine{Name: "Studio", URL: "studio:8188", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Another songsmith added Attic after this one read the file.
	stale, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	other, err := stale.WithMachine(Machine{Name: "Attic", URL: "attic:8188", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "machines", other); err != nil {
		t.Fatal(err)
	}

	added, err := AddMachine(p, Machine{Name: "Den", URL: "den:8188/", Enabled: true})
	if err != nil || added.URL != "http://den:8188" {
		t.Fatalf("add: %+v %v", added, err)
	}
	if err := SetMachineEnabled(p, "studio", false); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveMachine(p, "den"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	studio, _ := cfg.Machine("Studio")
	attic, ok := cfg.Machine("Attic")
	if len(cfg.Machines) != 2 || studio.Enabled || !ok || !attic.Enabled {
		t.Fatalf("machines after the changes: %+v", cfg.Machines)
	}
	if !strings.Contains(read(t, p), "# the drafting model") {
		t.Fatal("comments lost")
	}

	if err := SetMachineEnabled(p, ThisComputer, false); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := Load(p); cfg.Local.Enabled {
		t.Fatal("this computer still renders new songs")
	}
	for _, err := range []error{SetMachineEnabled(p, "Nope", false), func() error { _, err := RemoveMachine(p, "Nope"); return err }()} {
		if err == nil || !strings.Contains(err.Error(), `no machine named "Nope"`) {
			t.Errorf("a machine that is not there: %v", err)
		}
	}
}

func TestTheLastMachineThatTakesNewSongsStaysOn(t *testing.T) {
	p := writeConfig(t, commented)
	if _, err := AddMachine(p, Machine{Name: "Studio", URL: "studio:8188", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddMachine(p, Machine{Name: "Spare", URL: "spare:8188", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := SetMachineEnabled(p, ThisComputer, false); err != nil {
		t.Fatal(err)
	}
	before := read(t, p)
	for what, err := range map[string]error{
		"turning off Studio": SetMachineEnabled(p, "studio", false),
		"removing Studio":    func() error { _, err := RemoveMachine(p, "Studio"); return err }(),
	} {
		if !errors.Is(err, ErrOnlyMachine) || !strings.Contains(err.Error(), "Studio is the only machine that renders new songs; turn on another one first") {
			t.Errorf("%s: %v", what, err)
		}
	}
	if read(t, p) != before {
		t.Fatal("a refused change was written")
	}
	if _, err := RemoveMachine(p, "Spare"); err != nil {
		t.Fatalf("removing a machine that is off: %v", err)
	}
	if err := SetMachineEnabled(p, ThisComputer, true); err != nil {
		t.Fatal(err)
	}
	if err := SetMachineEnabled(p, "Studio", false); err != nil {
		t.Fatalf("turning off Studio once this computer is on: %v", err)
	}
	err := SetMachineEnabled(p, "this computer", false)
	if !errors.Is(err, ErrOnlyMachine) || !strings.HasPrefix(err.Error(), ThisComputer+" is the only machine") {
		t.Fatalf("turning off this computer last: %v", err)
	}

	// A hand-edited config with nothing on still loads and can be fixed.
	off := writeConfig(t, "local:\n  enabled: false\nmachines:\n  - name: A\n    url: http://a:8188\n    enabled: false\n")
	cfg, err := Load(off)
	if err != nil || cfg.TakesNewSongs() {
		t.Fatalf("all off: %v, takes new songs %v", err, cfg.TakesNewSongs())
	}
	if _, err := RemoveMachine(off, "A"); err != nil {
		t.Fatalf("removing a machine that is off while nothing is on: %v", err)
	}
}

func TestLiveSeesAMachineChangeAtOnce(t *testing.T) {
	p := writeConfig(t, "")
	live := NewLive(p, Default())
	if err := live.Update(func(path string) error {
		_, err := AddMachine(path, Machine{Name: "Studio", URL: "studio:8188", Enabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := live.Get().Machine("Studio"); !ok {
		t.Fatal("the running settings miss the machine just added")
	}
	if _, err := AddMachine(p, Machine{Name: "Attic", URL: "attic:8188", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := live.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := live.Get().Machine("Attic"); !ok {
		t.Fatal("a reload misses the machine added by another process")
	}
	if err := live.Update(func(string) error { return errors.New("disk full") }); err == nil {
		t.Fatal("a failed change was not reported")
	}
}

func TestAddressesOfOneServerAreOneAddress(t *testing.T) {
	cases := []struct{ typed, want string }{
		{"studio", "http://studio"},
		{"http://studio:80", "http://studio"},
		{"https://studio:443", "https://studio"},
		{"https://studio:80", "https://studio:80"},
		{"studio:8188/", "http://studio:8188"},
		{"http://[fe80::1%25en0]:8188", "http://[fe80::1%25en0]:8188"},
		{"[fe80::1%en0]:8188", "http://[fe80::1%25en0]:8188"},
		{"http://[0:0::1]:8190", "http://[::1]:8190"},
	}
	for _, c := range cases {
		got, err := NormalizeURL(c.typed)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.typed, got, err, c.want)
			continue
		}
		again, err := NormalizeURL(got)
		if err != nil || again != got {
			t.Errorf("%s: %q normalizes again to %q %v", c.typed, got, again, err)
		}
		if _, err := url.Parse(got); err != nil {
			t.Errorf("%s: %q does not parse: %v", c.typed, got, err)
		}
	}
	for typed, why := range map[string]string{
		"http://studio:":        "no port after the colon",
		"http://studio:08188":   "port 08188",
		"http://studio:0":       "port 0",
		"http://studio:65536":   "port 65536",
		"http://[fe80::1%25]:1": "",
	} {
		_, err := NormalizeURL(typed)
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v, want %q", typed, err, why)
		}
	}

	p := writeConfig(t, "comfyui:\n  port: 9000\n")
	if _, err := AddMachine(p, Machine{Name: "Studio", URL: "http://studio", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddMachine(p, Machine{Name: "Again", URL: "http://STUDIO:80", Enabled: true}); err == nil || !strings.Contains(err.Error(), `also the address of machine "Studio"`) {
		t.Fatalf("one server added twice: %v", err)
	}
	zoned, err := AddMachine(p, Machine{Name: "Link", URL: "http://[fe80::1%25en0]:8188", Enabled: true})
	if err != nil {
		t.Fatalf("a zoned address: %v", err)
	}
	if cfg, err := Load(p); err != nil {
		t.Fatalf("the saved zoned address does not load: %v", err)
	} else if m, _ := cfg.Machine("Link"); m.URL != zoned.URL {
		t.Fatalf("the zoned address came back as %q, saved %q", m.URL, zoned.URL)
	}
}

func TestThisComputersServerIsNotAddedUnderAnotherName(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "127.0.0.1", "localhost", "::1"} {
		for _, addr := range []string{"http://127.0.0.1:8188", "http://localhost:8188", "http://[::1]:8188", "http://0.0.0.0:8188"} {
			cfg := Default()
			cfg.ComfyUI.Host = host
			if _, err := cfg.WithMachine(Machine{Name: "Me", URL: addr, Enabled: true}); err == nil || !strings.Contains(err.Error(), "this computer's own ComfyUI server") {
				t.Errorf("comfyui.host %s, machine %s: %v", host, addr, err)
			}
		}
	}
	cfg := Default()
	cfg.ComfyUI.Host = "0.0.0.0"
	if ip, ok := networkAddress(t); ok {
		if _, err := cfg.WithMachine(Machine{Name: "Me", URL: "http://" + ip + ":8188", Enabled: true}); err == nil {
			t.Errorf("this computer's own network address %s was added while its server listens on every address", ip)
		}
		p := writeConfig(t, "comfyui:\n  host: 0.0.0.0\nmachines:\n  - name: Den\n    url: http://"+ip+":8188\n")
		if _, err := Load(p); err != nil {
			t.Errorf("a saved machine whose address this computer now has stopped the config loading: %v", err)
		}
		if _, err := RemoveMachine(p, "Den"); err != nil {
			t.Errorf("removing it: %v", err)
		}
	}
	if _, err := cfg.WithMachine(Machine{Name: "Other port", URL: "http://127.0.0.1:8190", Enabled: true}); err != nil {
		t.Fatalf("a second server on this computer's other port: %v", err)
	}
	cfg.ComfyUI.Host, cfg.ComfyUI.Port = "studio", 80
	if _, err := cfg.WithMachine(Machine{Name: "Me", URL: "http://studio:80", Enabled: true}); err == nil {
		t.Fatal("this computer's server, given by its name and the default port, was added")
	}
}

func TestMachineNamesHaveNoControlCharacters(t *testing.T) {
	for _, name := range []string{"Studio\tMac", "Studio\nMac", "Studio\x1b[31mMac", "Studio\u0085Mac"} {
		if err := CheckMachineName(name); err == nil || !strings.Contains(err.Error(), "tab, line break or other control character") {
			t.Errorf("%q: %v", name, err)
		}
	}
	if err := CheckMachineName("Studio Mac (M2) · upstairs"); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimAfterTakesABareNumberAsSeconds(t *testing.T) {
	cfg, err := Load(writeConfig(t, "reclaim_after: 600\n"))
	if err != nil || cfg.ReclaimAfter != 10*time.Minute {
		t.Fatalf("reclaim_after: 600 loaded as %s, %v", cfg.ReclaimAfter, err)
	}
	if _, err := Load(writeConfig(t, "reclaim_after: 0\n")); err == nil || !strings.Contains(err.Error(), "reclaim_after is 0s") {
		t.Fatalf("reclaim_after: 0: %v", err)
	}
	p := writeConfig(t, "reclaim_after: 90\nmachines:\n  - name: A\n    url: http://a:8188\n")
	if _, err := RemoveMachine(p, "A"); err != nil {
		t.Fatalf("removing a machine from a config with a bare reclaim_after: %v", err)
	}
	if err := Set(p, "render.steps", 20); err != nil {
		t.Fatalf("setting a key in a config with a bare reclaim_after: %v", err)
	}
}

// networkAddress is an IPv4 address of one of this computer's network
// interfaces other than loopback.
func networkAddress(t *testing.T) (string, bool) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() && !p.Addr().IsLoopback() {
			return p.Addr().String(), true
		}
	}
	return "", false
}
