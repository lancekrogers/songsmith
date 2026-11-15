package main

import (
	"testing"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
)

func TestEachStandInChecksAsTheRecordingShowsIt(t *testing.T) {
	s, err := standIns("127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	models := config.Default().ComfyUI
	for name, url := range map[string]string{"this computer": s.here.URL, "Studio Mac": s.studio.URL, "Den iMac": s.den.URL} {
		if st := machines.Check(t.Context(), url, models); st.State != machines.Ready || len(st.Unchecked) != 0 {
			t.Errorf("%s: %+v", name, st)
		}
	}
	if st := machines.Check(t.Context(), s.air.URL, models); st.State != machines.ModelsMissing || len(st.Missing) != 1 || st.Missing[0] != models.VAE {
		t.Errorf("MacBook Air: %+v", st)
	}
}
