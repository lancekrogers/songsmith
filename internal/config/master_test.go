package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/master"
)

func TestSetMasterDefaultReplacesTheWholeChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("# mine\nrender:\n  steps: 40 # keep\n"), 0o644)
	first := master.Settings{NoiseBed: 20, DCRumble: true, NotchTones: true, Loudness: -14, Ceiling: -1, LowCut: 30}
	if err := Set(path, "master.default", first); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "master.auto", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Master.Default != first || !cfg.Master.Auto || cfg.Render.Steps != 40 {
		t.Fatalf("loaded %+v, %v", cfg.Master, err)
	}
	second := master.Settings{Tilt: -1.5}
	if err := Set(path, "master.default", second); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(path)
	if cfg.Master.Default != second {
		t.Fatalf("old keys survived: %+v", cfg.Master.Default)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# keep") || !strings.Contains(string(data), "# mine") {
		t.Fatalf("comments lost:\n%s", data)
	}
	if err := Set(path, "master", master.Settings{}); err == nil {
		t.Fatal("a section with a value of the wrong type should be refused")
	}
	if err := Set(path, "render", 3); err == nil {
		t.Fatal("a section given a plain value should be refused")
	}
}
