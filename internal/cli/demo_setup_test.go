package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/setup"
)

func TestDemoSetupIsInertWithoutItsVariable(t *testing.T) {
	t.Setenv(demoSetupEnv, "")
	before := slices.Clone(setup.Manifest)
	in, err := tuiInstaller("test")
	if err != nil {
		t.Fatal(err)
	}
	real, ok := in.(*setup.Installer)
	if !ok || real.HFBase != setup.DefaultHFBase {
		t.Fatalf("installer %T %+v", in, in)
	}
	if !slices.Equal(setup.Manifest, before) {
		t.Fatal("the manifest changed")
	}
}

func TestDemoSetupRefusesAnythingButLoopback(t *testing.T) {
	for _, base := range []string{"https://huggingface.co", "http://192.0.2.1:18490", "file:///tmp/x"} {
		t.Setenv(demoSetupEnv, base)
		if _, err := tuiInstaller("test"); err == nil {
			t.Errorf("%s accepted", base)
		}
	}
}

func TestDemoSetupServesFromTheFakeOnly(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"dir":"vae","name":"minimax_music3_dav.safetensors","size":12,"sha256":"ab","label":"VAE"}]`)
	}))
	defer fake.Close()
	saved := setup.Manifest
	t.Cleanup(func() { setup.Manifest = saved })

	t.Setenv(demoSetupEnv, fake.URL)
	in, err := tuiInstaller("test")
	if err != nil {
		t.Fatal(err)
	}
	d := in.(*demoInstaller)
	if d.HFBase != fake.URL+"/hf" || len(setup.Manifest) != 1 || setup.Manifest[0].Size != 12 {
		t.Fatalf("base %s, manifest %+v", d.HFBase, setup.Manifest)
	}
	if _, err := d.HTTP.Get("http://192.0.2.1/"); err == nil || !strings.Contains(err.Error(), "refuses") {
		t.Fatalf("reached a non-loopback host: %v", err)
	}
	if err := d.InstallComfyUI(t.Context(), "/opt/ComfyUI", "", func(setup.Progress) {}); err == nil {
		t.Fatal("simulated an install outside a temporary folder")
	}
}
