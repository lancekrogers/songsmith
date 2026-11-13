package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type FileStatus int

const (
	FileMissing FileStatus = iota
	FilePartial
	FileWrongSize
	FileInstalled
)

func (s FileStatus) String() string {
	return [...]string{"missing", "partial", "wrong size", "installed"}[s]
}

// FileState describes one model file on disk. Bytes is the size of the
// installed file, or of the .part file while a download is partial.
type FileState struct {
	Status FileStatus
	Bytes  int64
	Size   int64
}

func (s FileState) Percent() float64 {
	if s.Size <= 0 {
		return 0
	}
	return min(float64(s.Bytes)/float64(s.Size)*100, 100)
}

func filePath(comfyDir string, f ModelFile) string {
	return filepath.Join(comfyDir, "models", f.Dir, f.Name)
}

func partPath(comfyDir string, f ModelFile) string { return filePath(comfyDir, f) + ".part" }

// PartPath is where an unfinished download of f is kept between runs.
func PartPath(comfyDir string, f ModelFile) string { return partPath(comfyDir, f) }

func State(comfyDir string, f ModelFile) FileState {
	st := FileState{Size: f.Size}
	fi, err := os.Stat(filePath(comfyDir, f))
	if err == nil && fi.Size() == f.Size {
		st.Status, st.Bytes = FileInstalled, fi.Size()
		return st
	}
	if pi, perr := os.Stat(partPath(comfyDir, f)); perr == nil {
		st.Status, st.Bytes = FilePartial, pi.Size()
		return st
	}
	if err == nil {
		st.Status, st.Bytes = FileWrongSize, fi.Size()
	}
	return st
}

// ErrBusy means another process is downloading the same file.
var ErrBusy = errors.New("another songsmith is already downloading this file")

// Download fetches f into <comfyDir>/models/<dir>, resuming a .part left by
// an earlier run. An installed file of the expected size is never touched;
// any other file at the destination is replaced only by renaming a
// completed, verified .part over it.
func (in *Installer) Download(ctx context.Context, comfyDir string, f ModelFile, progress func(Progress)) (err error) {
	emit := throttle(progress, 250*time.Millisecond)
	asset := f.Label
	dest := filePath(comfyDir, f)
	part := partPath(comfyDir, f)

	if State(comfyDir, f).Status == FileInstalled {
		emit(newProgress(asset, PhaseSkipped, f.Size, f.Size, "already installed"))
		return nil
	}
	if _, err := os.Stat(filepath.Join(comfyDir, "main.py")); err != nil {
		return fmt.Errorf("ComfyUI is not installed in %s; install it first (Settings → Setup, or `songsmith setup --install --comfyui`)", comfyDir)
	}

	size, sum, err := in.remoteInfo(ctx, f)
	if err != nil {
		return err
	}
	var have int64
	if pi, err := os.Stat(part); err == nil {
		have = min(pi.Size(), size)
	}
	if err := in.checkSpace(filepath.Dir(dest), size-have); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}

	pf, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", part, err)
	}
	defer func() {
		if cerr := pf.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", part, cerr)
		}
	}()
	if err := syscall.Flock(int(pf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%s: %w", f.Name, ErrBusy)
		}
		return fmt.Errorf("lock %s: %w", part, err)
	}

	fi, err := pf.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", part, err)
	}
	have = fi.Size()
	if have > size {
		if err := pf.Truncate(0); err != nil {
			return fmt.Errorf("reset %s: %w", part, err)
		}
		have = 0
	}

	h := sha256.New()
	if have > 0 {
		emit(newProgress(asset, PhaseVerify, have, size, "checking the "+GB(have)+" already downloaded"))
		if _, err := io.Copy(h, ctxReader{ctx, io.NewSectionReader(pf, 0, have)}); err != nil {
			return fmt.Errorf("read %s: %w", part, err)
		}
		emit(newProgress(asset, PhaseStart, have, size, "resuming at "+GB(have)))
	} else {
		emit(newProgress(asset, PhaseStart, 0, size, "downloading from huggingface.co/"+Repo))
	}

	if have < size {
		have, err = in.fetch(ctx, f, pf, h, have, size, func(n int64) {
			emit(newProgress(asset, PhaseDownload, n, size, ""))
		})
		if syncErr := pf.Sync(); err == nil && syncErr != nil {
			err = fmt.Errorf("sync %s: %w", part, syncErr)
		}
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("download of %s paused at %s of %s; %s is kept and the next run resumes it: %w",
					f.Name, GB(have), GB(size), filepath.Base(part), ctx.Err())
			}
			return fmt.Errorf("download of %s stopped at %s of %s (run setup again to resume): %w", f.Name, GB(have), GB(size), err)
		}
	}

	emit(newProgress(asset, PhaseVerify, have, size, "verifying SHA-256"))
	got := hex.EncodeToString(h.Sum(nil))
	if have != size || got != sum {
		_ = pf.Close()
		_ = os.Remove(part)
		return fmt.Errorf("%s failed verification (got %d bytes, sha256 %s; want %d bytes, sha256 %s); the download was deleted, run setup again",
			f.Name, have, got, size, sum)
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	emit(newProgress(asset, PhaseDone, size, size, dest))
	return nil
}

// remoteInfo reads the size and SHA-256 Hugging Face reports for f without
// following the redirect to the CDN, and checks them against the manifest.
func (in *Installer) remoteInfo(ctx context.Context, f ModelFile) (int64, string, error) {
	req, err := in.newRequest(ctx, http.MethodHead, in.HFBase+"/"+f.RepoPath())
	if err != nil {
		return 0, "", err
	}
	noRedirect := *in.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return 0, "", unreachable("huggingface.co", "download the MiniMax Music 3 files", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, "", fmt.Errorf("huggingface.co answered HTTP %d for %s", resp.StatusCode, f.RepoPath())
	}
	size, sum := f.Size, f.SHA256
	if v := resp.Header.Get("X-Linked-Size"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("bad x-linked-size %q for %s", v, f.RepoPath())
		}
		size = n
	}
	if v := strings.Trim(strings.TrimPrefix(resp.Header.Get("X-Linked-Etag"), "W/"), `"`); v != "" {
		sum = strings.ToLower(v)
	}
	if size != f.Size || sum != f.SHA256 {
		return 0, "", fmt.Errorf("%s changed upstream (now %d bytes, sha256 %s; songsmith expects %d bytes, sha256 %s); update songsmith",
			f.RepoPath(), size, sum, f.Size, f.SHA256)
	}
	return size, sum, nil
}

// fetch appends the bytes from have onward to pf and h, restarting from zero
// when the server ignores the Range header. It returns the bytes on disk.
func (in *Installer) fetch(ctx context.Context, f ModelFile, pf *os.File, h hash.Hash, have, size int64, tick func(int64)) (int64, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err := in.newRequest(ctx, http.MethodGet, in.HFBase+"/"+f.RepoPath())
	if err != nil {
		return have, err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return have, unreachable("huggingface.co", "download the MiniMax Music 3 files", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		start, ok := rangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != have {
			return have, fmt.Errorf("server resumed at the wrong offset (%q, wanted %d)", resp.Header.Get("Content-Range"), have)
		}
	case resp.StatusCode == http.StatusOK:
		if have > 0 {
			if err := pf.Truncate(0); err != nil {
				return have, fmt.Errorf("restart %s: %w", pf.Name(), err)
			}
			h.Reset()
			have = 0
		}
	default:
		return have, fmt.Errorf("download %s: HTTP %d", f.RepoPath(), resp.StatusCode)
	}
	if _, err := pf.Seek(have, io.SeekStart); err != nil {
		return have, fmt.Errorf("seek %s: %w", pf.Name(), err)
	}

	stall := time.AfterFunc(in.StallTimeout, func() {
		cancel(fmt.Errorf("no data for %s", in.StallTimeout))
	})
	defer stall.Stop()
	buf := make([]byte, 1<<20)
	for have < size {
		n, rerr := resp.Body.Read(buf[:min(int64(len(buf)), size-have)])
		if n > 0 {
			stall.Reset(in.StallTimeout)
			if _, err := pf.Write(buf[:n]); err != nil {
				return have, fmt.Errorf("write %s: %w", pf.Name(), err)
			}
			h.Write(buf[:n])
			have += int64(n)
			tick(have)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
				return have, cause
			}
			return have, rerr
		}
	}
	if have < size {
		return have, io.ErrUnexpectedEOF
	}
	return have, nil
}

func rangeStart(v string) (int64, bool) {
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(start, 10, 64)
	return n, err == nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
