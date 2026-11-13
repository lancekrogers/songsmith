package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

const (
	DefaultHFBase    = "https://huggingface.co/" + Repo + "/resolve/main"
	DefaultComfyRepo = "https://github.com/comfyanonymous/ComfyUI.git"
)

// Cmd is one subprocess run by an installer.
type Cmd struct {
	Dir  string
	Name string
	Args []string
	Env  []string
	Out  io.Writer
}

// Runner runs a subprocess to completion. Tests swap in a fake.
type Runner func(ctx context.Context, c Cmd) error

// Installer holds the dependencies every install action uses. New returns
// one wired to the network, the filesystem and real subprocesses.
type Installer struct {
	HFBase    string
	ComfyRepo string
	UserAgent string
	HTTP      *http.Client
	// FreeSpace reports the bytes available to an unprivileged user on the
	// volume holding path, which must exist.
	FreeSpace func(path string) (uint64, error)
	LookPath  func(file string) (string, error)
	Run       Runner
	// Reach checks that a URL answers, before a step that needs the network.
	Reach func(ctx context.Context, url string) error
	GOOS  string
	// StallTimeout aborts a download that received no bytes for this long.
	StallTimeout time.Duration
}

// New returns an Installer whose requests identify as songsmith/<version>.
func New(version string) *Installer {
	in := &Installer{
		HFBase:    DefaultHFBase,
		ComfyRepo: DefaultComfyRepo,
		UserAgent: "songsmith/" + version,
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ForceAttemptHTTP2:     true,
		}},
		FreeSpace:    freeSpace,
		LookPath:     exec.LookPath,
		Run:          execRunner,
		GOOS:         runtime.GOOS,
		StallTimeout: 2 * time.Minute,
	}
	in.Reach = in.reach
	return in
}

func (in *Installer) newRequest(ctx context.Context, method, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", in.UserAgent)
	return req, nil
}

func (in *Installer) reach(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := in.newRequest(ctx, http.MethodHead, url)
	if err != nil {
		return err
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// unreachable turns a network failure into a message that names the host
// and what it is needed for.
func unreachable(host, purpose string, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("cannot reach %s, which songsmith needs to %s. Check the internet connection (and any proxy settings), then run setup again: %w", host, purpose, err)
}

func execRunner(ctx context.Context, c Cmd) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdout, cmd.Stderr = c.Out, c.Out
	// Own process group, so cancelling also stops git's and pip's children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w", filepath.Base(c.Name), err)
	}
	return nil
}

func freeSpace(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("check free space on %s: %w", path, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// existingAncestor returns path or its nearest parent that exists, so free
// space can be checked before the install folder is created.
func existingAncestor(path string) string {
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// checkSpace returns an error when the volume holding path has less than
// need bytes available.
func (in *Installer) checkSpace(path string, need int64) error {
	if need <= 0 {
		return nil
	}
	at := existingAncestor(path)
	free, err := in.FreeSpace(at)
	if err != nil {
		return err
	}
	if uint64(need) > free {
		return fmt.Errorf("not enough disk space: needs %s on the volume holding %s, only %s free. Free up space or choose another folder in Settings, then run setup again",
			GB(need), at, GB(int64(free)))
	}
	return nil
}
