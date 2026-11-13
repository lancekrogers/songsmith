package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/config"
)

func hostPort(t *testing.T, url string) (string, int) {
	h, p, err := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return h, port
}

func TestStatusReportsGPUOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"system":{"argv":["main.py","--gpu-only"]},"devices":[{"type":"mps"}]}`)
	}))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)
	m := New(config.ComfyUI{Host: host, Port: port}, t.TempDir())
	st, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || !st.GPUOnly || st.Device != "mps" || st.Managed {
		t.Fatalf("status = %+v", st)
	}
	if err := m.Stop(t.Context()); err != ErrNotManaged {
		t.Fatalf("stop on foreign server = %v", err)
	}
}

func TestStartFailsFastWhenProcessExits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(dir, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(py), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(py, []byte("#!/bin/sh\necho 'ModuleNotFoundError: torch' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	m := New(config.ComfyUI{Dir: dir, Host: "127.0.0.1", Port: port}, t.TempDir())
	m.ReadyTimeout = 10 * time.Second
	start := time.Now()
	_, err := m.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "ModuleNotFoundError") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s; should fail as soon as the process exits", time.Since(start))
	}
}

func TestStartMissingInstall(t *testing.T) {
	m := New(config.ComfyUI{Dir: filepath.Join(t.TempDir(), "nope"), Host: "127.0.0.1", Port: 1}, t.TempDir())
	if _, err := m.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "comfyui not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopIgnoresReusedPID(t *testing.T) {
	state := t.TempDir()
	m := New(config.ComfyUI{Host: "127.0.0.1", Port: 1}, state)
	// Our own pid is alive but is not ComfyUI, like a pid reused after a reboot.
	if err := os.WriteFile(m.pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := m.ourPID(); ok {
		t.Fatal("test process mistaken for ComfyUI")
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.pidPath()); !os.IsNotExist(err) {
		t.Fatal("stale pid file kept")
	}
}

func TestStartWaitsForOurStartingServer(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	// A process that looks like a ComfyUI songsmith launched but is not answering yet.
	fake := exec.Command("/bin/sh", "-c", "sleep 30; true", "main.py", "--port", strconv.Itoa(port))
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	defer fake.Process.Kill()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		out, _ := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(fake.Process.Pid)).Output()
		if strings.Contains(string(out), "main.py") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake ComfyUI did not start")
		}
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.py"), nil, 0o644)
	py := filepath.Join(dir, ".venv", "bin", "python")
	os.MkdirAll(filepath.Dir(py), 0o755)
	launched := filepath.Join(t.TempDir(), "launched")
	os.WriteFile(py, []byte("#!/bin/sh\ntouch "+launched+"\nsleep 30\n"), 0o755)

	m := New(config.ComfyUI{Dir: dir, Host: "127.0.0.1", Port: port}, t.TempDir())
	m.ReadyTimeout = 1500 * time.Millisecond
	os.MkdirAll(m.stateDir, 0o755)
	os.WriteFile(m.pidPath(), []byte(strconv.Itoa(fake.Process.Pid)), 0o644)

	if _, err := m.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(launched); err == nil {
		t.Fatal("launched a second ComfyUI while ours was still starting")
	}
}

// startFakeComfy runs a process whose command line looks like a ComfyUI
// listening on port, in its own process group so Stop can signal it.
func startFakeComfy(t *testing.T, port int) *exec.Cmd {
	t.Helper()
	fake := exec.Command("/bin/sh", "-c", "sleep 30; true", "main.py", "--port", strconv.Itoa(port))
	fake.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { fake.Wait(); close(done) }()
	t.Cleanup(func() {
		fake.Process.Kill()
		<-done
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		out, _ := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(fake.Process.Pid)).Output()
		if strings.Contains(string(out), "main.py") {
			return fake
		}
		if time.Now().After(deadline) {
			t.Fatal("fake ComfyUI did not start")
		}
	}
}

func closedPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestAServerStartedOnAnEarlierPortIsStillOurs(t *testing.T) {
	oldPort, newPort := closedPort(t), closedPort(t)
	fake := startFakeComfy(t, oldPort)
	state := t.TempDir()
	m := New(config.ComfyUI{Host: "127.0.0.1", Port: newPort}, state)
	if err := os.WriteFile(m.pidPath(), []byte(strconv.Itoa(fake.Process.Pid)+" "+strconv.Itoa(oldPort)), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := m.Status(t.Context())
	if st.Running || st.Managed || st.OtherPort != oldPort {
		t.Fatalf("status %+v", st)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if alive(fake.Process.Pid) {
		if out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(fake.Process.Pid)).Output(); !strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			t.Fatal("the server on the earlier port is still running")
		}
	}
	if _, err := os.Stat(m.pidPath()); !os.IsNotExist(err) {
		t.Fatal("pid file kept")
	}
}

func TestAPidFileWithoutAPortCountsAsTheConfiguredOne(t *testing.T) {
	port := closedPort(t)
	fake := startFakeComfy(t, port)
	m := New(config.ComfyUI{Host: "127.0.0.1", Port: port}, t.TempDir())
	if err := os.WriteFile(m.pidPath(), []byte(strconv.Itoa(fake.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if pid, p, ok := m.ourPID(); !ok || pid != fake.Process.Pid || p != port {
		t.Fatalf("ourPID = %d %d %v", pid, p, ok)
	}
}
