package setup

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
)

// TestRealVAEDownload downloads the 0.2 GB VAE from Hugging Face into the
// ComfyUI folder named by SONGSMITH_SETUP_PROOF_DIR. With
// SONGSMITH_SETUP_PROOF_STOP_AT set it cancels once that many bytes have
// arrived, leaving the .part for the next run to resume.
func TestRealVAEDownload(t *testing.T) {
	dir := os.Getenv("SONGSMITH_SETUP_PROOF_DIR")
	if dir == "" {
		t.Skip("set SONGSMITH_SETUP_PROOF_DIR to a scratch ComfyUI folder to download the real VAE")
	}
	stopAt, _ := strconv.ParseInt(os.Getenv("SONGSMITH_SETUP_PROOF_STOP_AT"), 10, 64)
	vae, _ := Lookup(DirVAE, "minimax_music3_dav.safetensors")
	t.Logf("before: %s, %d bytes on disk", State(dir, vae).Status, State(dir, vae).Bytes)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var firstDownload int64 = -1
	err := New("dev").Download(ctx, dir, vae, func(p Progress) {
		switch {
		case p.Phase == PhaseStart || p.Phase == PhaseVerify || p.Phase == PhaseDone || p.Phase == PhaseSkipped:
			t.Logf("%-8s bytes=%d %s", p.Phase, p.Bytes, p.Detail)
		case p.Phase == PhaseDownload && firstDownload < 0:
			firstDownload = p.Bytes
			t.Logf("first download tick at %d bytes", p.Bytes)
		}
		if stopAt > 0 && p.Phase == PhaseDownload && p.Bytes >= stopAt {
			cancel()
		}
	})
	st := State(dir, vae)
	t.Logf("after: %s, %d bytes on disk, err: %v", st.Status, st.Bytes, err)
	if stopAt > 0 {
		if !errors.Is(err, context.Canceled) || st.Status != FilePartial {
			t.Fatalf("expected an interrupted download, got %v", err)
		}
		return
	}
	if err != nil || st.Status != FileInstalled {
		t.Fatalf("download failed: %v", err)
	}
}
