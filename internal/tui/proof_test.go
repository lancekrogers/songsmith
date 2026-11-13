package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/setup"
)

// proofInstaller is the real installer with an empty plan, so the checks
// that follow each job read nothing from the network.
type proofInstaller struct{ *setup.Installer }

func (proofInstaller) Plan(context.Context, config.Config) setup.Plan { return setup.Plan{} }

// TestRealVAEDownloadThroughTheJobRunner downloads the 0.2 GB VAE from
// Hugging Face through the TUI's install jobs into the ComfyUI folder named
// by SONGSMITH_TUI_PROOF_DIR, cancels it part way, resumes it and checks the
// file. Skipped unless that variable is set.
func TestRealVAEDownloadThroughTheJobRunner(t *testing.T) {
	dir := os.Getenv("SONGSMITH_TUI_PROOF_DIR")
	if dir == "" {
		t.Skip("set SONGSMITH_TUI_PROOF_DIR to a scratch ComfyUI folder to download the real VAE")
	}
	if _, err := os.Stat(filepath.Join(dir, "main.py")); err != nil {
		t.Fatalf("%s needs a main.py: %v", dir, err)
	}
	const stopAt = 40 << 20
	a, _ := newTestModel(t, fakeDrafter{})
	cfg := a.cfg()
	cfg.ComfyUI.Dir, cfg.Ollama.URL = dir, "http://127.0.0.1:1"
	a.d.Config = config.NewLive(a.d.Config.Path(), cfg)
	a.d.Installer = proofInstaller{setup.New("dev")}
	vae, _ := setup.Lookup(setup.DirVAE, "minimax_music3_dav.safetensors")
	target := modelTarget(a.cfg(), vae)
	key := target.key()
	if st := setup.State(dir, vae); st.Status != setup.FileMissing {
		t.Fatalf("start from an empty folder, found %s", st.Status)
	}

	// run starts the job and delivers its messages until stop says so.
	run := func(stop func(app, tea.Msg) bool) app {
		a.startJob(target, nil)()
		deadline := time.Now().Add(10 * time.Minute)
		for {
			msg, ok := a.events.next()
			if !ok {
				if time.Now().After(deadline) {
					t.Fatal("timed out")
				}
				time.Sleep(5 * time.Millisecond)
				continue
			}
			if p, ok := msg.(jobProgressMsg); ok && p.progress.Phase != setup.PhaseDownload {
				t.Logf("%-8s bytes=%d %s", p.progress.Phase, p.progress.Bytes, p.progress.Detail)
			}
			a, _ = send(t, a, msg)
			if stop(a, msg) {
				return a
			}
		}
	}

	var firstTick int64 = -1
	a = run(func(a app, msg tea.Msg) bool {
		if p, ok := msg.(jobProgressMsg); ok && p.progress.Phase == setup.PhaseDownload {
			if firstTick < 0 {
				firstTick = p.progress.Bytes
				t.Logf("first run: first download tick at %d bytes", firstTick)
			}
			if p.progress.Bytes >= stopAt {
				t.Logf("cancelling at %d bytes", p.progress.Bytes)
				a.cancelJob(key)
			}
		}
		_, done := msg.(jobDoneMsg)
		return done
	})
	part := setup.State(dir, vae)
	if part.Status != setup.FilePartial || part.Bytes <= 0 || part.Bytes >= vae.Size {
		t.Fatalf("after cancel: %s with %d bytes (it must stop part way)", part.Status, part.Bytes)
	}
	t.Logf("after cancel: %s kept with %d of %d bytes", filepath.Base(setup.PartPath(dir, vae)), part.Bytes, vae.Size)

	var resumedAt, resumeTick int64 = -1, -1
	a = run(func(a app, msg tea.Msg) bool {
		if p, ok := msg.(jobProgressMsg); ok {
			if p.progress.Phase == setup.PhaseStart && resumedAt < 0 {
				resumedAt = p.progress.Bytes
			}
			if p.progress.Phase == setup.PhaseDownload && resumeTick < 0 {
				resumeTick = p.progress.Bytes
				t.Logf("second run: first download tick at %d bytes", resumeTick)
			}
		}
		_, done := msg.(jobDoneMsg)
		return done
	})
	if err, failed := a.jobs.failed[key]; failed {
		t.Fatalf("resume failed: %s", err)
	}
	if resumedAt != part.Bytes || resumeTick <= part.Bytes {
		t.Fatalf("resumed at %d, first tick %d; the partial file had %d bytes", resumedAt, resumeTick, part.Bytes)
	}
	path := filepath.Join(dir, "models", vae.Dir, vae.Name)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		t.Fatal(err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	t.Logf("installed %s: %d bytes, sha256 %s", path, n, sum)
	if n != vae.Size || sum != vae.SHA256 {
		t.Fatalf("want %d bytes, sha256 %s", vae.Size, vae.SHA256)
	}
}
