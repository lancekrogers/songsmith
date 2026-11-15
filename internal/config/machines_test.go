package config

import (
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

	ms, err = cfg.WithoutMachine("MACBOOK PRO")
	if err != nil || len(ms) != 0 {
		t.Fatalf("remove: %v %v", ms, err)
	}
	if _, err := cfg.WithoutMachine("Nope"); err == nil {
		t.Fatal("removed a machine that is not there")
	}
}
