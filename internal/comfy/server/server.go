// Package server starts, detects and stops the local ComfyUI process.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
)

// RequiredFlag keeps every model on the GPU. Without it ComfyUI runs the
// MiniMax text encoder (an 8B LLM) on the CPU on Apple Silicon, about 50x slower.
const RequiredFlag = "--gpu-only"

type Manager struct {
	cfg      config.ComfyUI
	stateDir string
	client   *comfy.Client
	// ReadyTimeout bounds how long Start waits for the HTTP API.
	ReadyTimeout time.Duration
}

func New(cfg config.ComfyUI, stateDir string) *Manager {
	return &Manager{cfg: cfg, stateDir: stateDir, client: comfy.NewClient(cfg.BaseURL()), ReadyTimeout: 3 * time.Minute}
}

func (m *Manager) Client() *comfy.Client { return m.client }
func (m *Manager) LogPath() string       { return filepath.Join(m.stateDir, "comfyui.log") }
func (m *Manager) pidPath() string       { return filepath.Join(m.stateDir, "comfyui.pid") }
func (m *Manager) Python() string        { return filepath.Join(m.cfg.Dir, ".venv", "bin", "python") }

type Status struct {
	Running bool
	// Managed is true when the running server was started by songsmith.
	Managed bool
	PID     int
	GPUOnly bool
	Device  string
	// OtherPort is the port of a server songsmith started that still runs
	// on a port other than the configured one; Stop stops it.
	OtherPort int
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	var st Status
	pid, port, ours := m.ourPID()
	if ours && port != m.cfg.Port {
		st.OtherPort, st.PID = port, pid
	}
	stats, err := m.client.SystemStats(ctx)
	if err != nil {
		return st, nil
	}
	st.Running = true
	st.GPUOnly = slices.Contains(stats.System.Argv, RequiredFlag)
	if len(stats.Devices) > 0 {
		st.Device = stats.Devices[0].Type
	}
	if ours && port == m.cfg.Port {
		st.Managed, st.PID = true, pid
	}
	return st, nil
}

// ourPID returns the pid and port from the pid file when that process is
// still the ComfyUI songsmith started, guarding against pid reuse after a
// crash or reboot.
func (m *Manager) ourPID() (pid, port int, ok bool) {
	pid, port, ok = m.readPID()
	if !ok || !alive(pid) {
		return 0, 0, false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0, false
	}
	cmdline := string(out)
	return pid, port, strings.Contains(cmdline, "main.py") && strings.Contains(cmdline, "--port "+strconv.Itoa(port))
}

// Start launches ComfyUI unless one is already listening, then waits until
// its API answers. The process is detached so it outlives songsmith and
// keeps models loaded between songs.
func (m *Manager) Start(ctx context.Context) (Status, error) {
	if st, _ := m.Status(ctx); st.Running {
		return st, nil
	}
	if _, port, ok := m.ourPID(); ok && port == m.cfg.Port {
		// A server songsmith started is still loading; wait for it instead of
		// launching a second one.
		return m.waitReady(ctx, nil)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Dir, "main.py")); err != nil {
		return Status{}, fmt.Errorf("comfyui not found at %s: %w", m.cfg.Dir, err)
	}
	if _, err := os.Stat(m.Python()); err != nil {
		return Status{}, fmt.Errorf("comfyui venv python not found at %s: %w", m.Python(), err)
	}
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		return Status{}, fmt.Errorf("create state dir: %w", err)
	}
	logf, err := os.OpenFile(m.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Status{}, fmt.Errorf("open comfyui log: %w", err)
	}
	defer logf.Close()

	cmd := exec.Command(m.Python(), "main.py",
		"--listen", m.cfg.Host, "--port", strconv.Itoa(m.cfg.Port),
		"--disable-auto-launch", RequiredFlag)
	cmd.Dir = m.cfg.Dir
	cmd.Env = append(os.Environ(), "PYTORCH_ENABLE_MPS_FALLBACK=1")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Status{}, fmt.Errorf("start comfyui: %w", err)
	}
	pid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := os.WriteFile(m.pidPath(), fmt.Appendf(nil, "%d %d", pid, m.cfg.Port), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return Status{}, fmt.Errorf("write pid file: %w", err)
	}

	return m.waitReady(ctx, exited)
}

// waitReady waits for the API to answer. exited, when not nil, reports the
// launched process dying first.
func (m *Manager) waitReady(ctx context.Context, exited <-chan error) (Status, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, m.ReadyTimeout, errors.New("comfyui did not become ready in time"))
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			_ = os.Remove(m.pidPath())
			return Status{}, fmt.Errorf("comfyui exited during startup (%v); last log lines:\n%s", err, tail(m.LogPath(), 15))
		case <-ctx.Done():
			return Status{}, fmt.Errorf("%w; log: %s", context.Cause(ctx), m.LogPath())
		case <-tick.C:
			if st, _ := m.Status(ctx); st.Running {
				return st, nil
			}
		}
	}
}

// Ensure starts ComfyUI if needed and rejects a server that would run the
// MiniMax LLM on the CPU.
func (m *Manager) Ensure(ctx context.Context) error {
	st, err := m.Start(ctx)
	if err != nil {
		return err
	}
	if st.Device == "mps" && !st.GPUOnly {
		return fmt.Errorf("comfyui at %s is running without %s, which makes renders about 50x slower; restart it with `songsmith server start`", m.cfg.BaseURL(), RequiredFlag)
	}
	return nil
}

// ErrNotManaged means a server is running that songsmith did not start.
var ErrNotManaged = errors.New("comfyui is running but was not started by songsmith; stop it yourself")

// Stop terminates a server that songsmith started, on the configured port
// or the one it was started on.
func (m *Manager) Stop(ctx context.Context) error {
	pid, _, ok := m.ourPID()
	if !ok {
		_ = os.Remove(m.pidPath())
		if st, _ := m.Status(ctx); st.Running {
			return ErrNotManaged
		}
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop comfyui (pid %d): %w", pid, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if alive(pid) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	return os.Remove(m.pidPath())
}

// readPID reads "pid port" from the pid file. A file from before the port
// was recorded holds only the pid, and counts as the configured port.
func (m *Manager) readPID() (pid, port int, ok bool) {
	data, err := os.ReadFile(m.pidPath())
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields) > 2 {
		return 0, 0, false
	}
	pid, err = strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, 0, false
	}
	port = m.cfg.Port
	if len(fields) == 2 {
		if port, err = strconv.Atoi(fields[1]); err != nil || port <= 0 {
			return 0, 0, false
		}
	}
	return pid, port, true
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func tail(path string, n int) string {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(data), "\r", "\n"), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
