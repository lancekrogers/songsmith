package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHub serves one file the way Hugging Face does: the resolve URL
// answers HEAD with a 302 and x-linked-* headers, and GET redirects to a
// CDN path that honours Range.
type fakeHub struct {
	t           *testing.T
	content     []byte
	serve       []byte // what the CDN sends; content when nil
	ignoreRange bool
	// cutAfter aborts the first GET after this many bytes.
	cutAfter int
	// holdAfter pauses the first GET after this many bytes until the
	// client goes away.
	holdAfter int

	mu     sync.Mutex
	gets   int
	ranges []string
}

func (h *fakeHub) file() ModelFile {
	sum := sha256.Sum256(h.content)
	return ModelFile{Dir: DirVAE, Name: "test.safetensors", Size: int64(len(h.content)), SHA256: hex.EncodeToString(sum[:]), Label: "Test file"}
}

func (h *fakeHub) start() (*httptest.Server, *Installer, ModelFile) {
	f := h.file()
	mux := http.NewServeMux()
	mux.HandleFunc("/resolve/"+f.RepoPath(), func(w http.ResponseWriter, r *http.Request) {
		if ua := r.UserAgent(); ua != "songsmith/test" {
			h.t.Errorf("user agent = %q", ua)
		}
		w.Header().Set("X-Linked-Size", strconv.Itoa(len(h.content)))
		w.Header().Set("X-Linked-Etag", `"`+f.SHA256+`"`)
		http.Redirect(w, r, "/cdn/"+f.Name, http.StatusFound)
	})
	mux.HandleFunc("/cdn/"+f.Name, func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.gets++
		first := h.gets == 1
		h.ranges = append(h.ranges, r.Header.Get("Range"))
		h.mu.Unlock()
		body := h.serve
		if body == nil {
			body = h.content
		}
		start := 0
		if rg := r.Header.Get("Range"); rg != "" && !h.ignoreRange {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			if err != nil {
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
			start = n
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", n, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-n))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
		}
		rest := body[start:]
		switch {
		case first && h.cutAfter > 0:
			w.Write(rest[:h.cutAfter])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case first && h.holdAfter > 0:
			w.Write(rest[:h.holdAfter])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
				h.t.Error("client never cancelled")
			}
			return
		}
		w.Write(rest)
	})
	srv := httptest.NewServer(mux)
	h.t.Cleanup(srv.Close)
	in := New("test")
	in.HFBase = srv.URL + "/resolve"
	in.FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	return srv, in, f
}

func testContent(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func comfyFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ComfyUI")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertInstalled(t *testing.T, dir string, f ModelFile, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filePath(dir, f))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("installed file differs (err %v, %d bytes)", err, len(got))
	}
	if _, err := os.Stat(partPath(dir, f)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".part left behind: %v", err)
	}
	if st := State(dir, f); st.Status != FileInstalled {
		t.Fatalf("state = %v", st.Status)
	}
}

func TestDownloadFresh(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(3 << 20)}
	_, in, f := h.start()
	dir := comfyFixture(t)
	// A wrong-size file is replaced only by the verified download.
	os.MkdirAll(filepath.Dir(filePath(dir, f)), 0o755)
	os.WriteFile(filePath(dir, f), []byte("old"), 0o644)
	if st := State(dir, f); st.Status != FileWrongSize {
		t.Fatalf("state = %v", st.Status)
	}

	var phases []Phase
	err := in.Download(t.Context(), dir, f, func(p Progress) { phases = append(phases, p.Phase) })
	if err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, dir, f, h.content)
	if phases[0] != PhaseStart || phases[len(phases)-1] != PhaseDone {
		t.Fatalf("phases = %v", phases)
	}
}

func TestDownloadResumesAfterCut(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(3 << 20), cutAfter: 1 << 20}
	_, in, f := h.start()
	dir := comfyFixture(t)

	err := in.Download(t.Context(), dir, f, nil)
	if err == nil || !strings.Contains(err.Error(), "run setup again to resume") {
		t.Fatalf("err = %v", err)
	}
	st := State(dir, f)
	if st.Status != FilePartial || st.Bytes != 1<<20 {
		t.Fatalf("after cut: %+v", st)
	}

	var resumedAt int64 = -1
	err = in.Download(t.Context(), dir, f, func(p Progress) {
		if p.Phase == PhaseStart {
			resumedAt = p.Bytes
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, dir, f, h.content)
	if resumedAt != 1<<20 || h.ranges[1] != "bytes=1048576-" {
		t.Fatalf("resumed at %d, ranges %q", resumedAt, h.ranges)
	}
}

func TestDownloadRestartsWhenRangeIgnored(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(2 << 20), ignoreRange: true}
	_, in, f := h.start()
	dir := comfyFixture(t)
	os.MkdirAll(filepath.Dir(partPath(dir, f)), 0o755)
	os.WriteFile(partPath(dir, f), h.content[:1000], 0o644)

	if err := in.Download(t.Context(), dir, f, nil); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, dir, f, h.content)
	if h.ranges[0] != "bytes=1000-" {
		t.Fatalf("ranges = %q", h.ranges)
	}
}

func TestDownloadChecksumMismatchDeletesPart(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(1 << 20)}
	h.serve = bytes.Repeat([]byte{1}, len(h.content))
	_, in, f := h.start()
	dir := comfyFixture(t)

	err := in.Download(t.Context(), dir, f, nil)
	if err == nil || !strings.Contains(err.Error(), "failed verification") {
		t.Fatalf("err = %v", err)
	}
	if st := State(dir, f); st.Status != FileMissing {
		t.Fatalf("state = %v", st.Status)
	}
}

func TestDownloadRejectsChangedUpstream(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(1000)}
	_, in, f := h.start()
	f.SHA256 = strings.Repeat("0", 64)
	err := in.Download(t.Context(), comfyFixture(t), f, nil)
	if err == nil || !strings.Contains(err.Error(), "changed upstream") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadSkipsInstalledFile(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(1000)}
	_, in, f := h.start()
	dir := comfyFixture(t)
	other := bytes.Repeat([]byte{9}, len(h.content))
	os.MkdirAll(filepath.Dir(filePath(dir, f)), 0o755)
	os.WriteFile(filePath(dir, f), other, 0o644)

	var last Progress
	if err := in.Download(t.Context(), dir, f, func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filePath(dir, f))
	if !bytes.Equal(got, other) || h.gets != 0 || last.Phase != PhaseSkipped {
		t.Fatalf("installed file touched: gets %d, phase %s", h.gets, last.Phase)
	}
}

func TestDownloadCancelKeepsPart(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(3 << 20), holdAfter: 1 << 20}
	_, in, f := h.start()
	dir := comfyFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	err := in.Download(ctx, dir, f, func(p Progress) {
		if p.Phase == PhaseDownload && p.Bytes > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "next run resumes") {
		t.Fatalf("err = %v", err)
	}
	st := State(dir, f)
	if st.Status != FilePartial || st.Bytes == 0 || st.Bytes > 1<<20 {
		t.Fatalf("after cancel: %+v", st)
	}
	if err := in.Download(t.Context(), dir, f, nil); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, dir, f, h.content)
	if h.ranges[1] != fmt.Sprintf("bytes=%d-", st.Bytes) {
		t.Fatalf("ranges = %q", h.ranges)
	}
}

func TestDownloadInsufficientSpace(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(1000)}
	_, in, f := h.start()
	dir := comfyFixture(t)
	var probed string
	in.FreeSpace = func(p string) (uint64, error) { probed = p; return 999, nil }

	err := in.Download(t.Context(), dir, f, nil)
	if err == nil || !strings.Contains(err.Error(), "not enough disk space") {
		t.Fatalf("err = %v", err)
	}
	// models/vae does not exist yet, so the probe falls back to its parent.
	if probed != dir || h.gets != 0 {
		t.Fatalf("probed %q, gets %d", probed, h.gets)
	}
	if _, err := os.Stat(partPath(dir, f)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a .part was created")
	}
}

func TestDownloadNeedsComfyUI(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(1000)}
	_, in, f := h.start()
	err := in.Download(t.Context(), filepath.Join(t.TempDir(), "none"), f, nil)
	if err == nil || !strings.Contains(err.Error(), "install it first") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadRefusesConcurrentRun(t *testing.T) {
	h := &fakeHub{t: t, content: testContent(3 << 20), holdAfter: 1 << 20}
	_, in, f := h.start()
	dir := comfyFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	done := make(chan error)
	go func() {
		var once sync.Once
		done <- in.Download(ctx, dir, f, func(p Progress) {
			if p.Phase == PhaseDownload {
				once.Do(func() { close(started) })
			}
		})
	}()
	<-started
	err := in.Download(t.Context(), dir, f, nil)
	cancel()
	<-done
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestRecommendedSet(t *testing.T) {
	var rec []string
	for _, f := range Manifest {
		if f.Recommended {
			rec = append(rec, f.RepoPath())
		}
		if len(f.SHA256) != 64 || f.Size <= 0 {
			t.Errorf("%s: bad size or sha", f.Name)
		}
	}
	want := "diffusion_models/minimax_music3_dit_fp16.safetensors text_encoders/minimax_music3_text_encoder_pruned_bf16.safetensors vae/minimax_music3_dav.safetensors"
	if strings.Join(rec, " ") != want {
		t.Fatalf("recommended = %v", rec)
	}
	int8, _ := Lookup(DirTextEncoder, "minimax_music3_text_encoder_pruned_int8_convrot.safetensors")
	if !strings.Contains(int8.Note, "NVIDIA cards with 16 GB") || !strings.Contains(int8.Note, "50x slower") {
		t.Fatalf("int8 note = %q", int8.Note)
	}
}
