package player

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// silentWAV writes a short silent file so playback tests make no sound.
func silentWAV(t *testing.T, seconds string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "silence.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=32000:cl=stereo", "-t", seconds, path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	return path
}

func TestPlayFinishesAndReportsDone(t *testing.T) {
	path := silentWAV(t, "0.5")
	p := New()
	if _, err := command(path); err != nil {
		t.Skip(err)
	}
	if err := p.Play(path); err != nil {
		t.Fatal(err)
	}
	if p.Current() != path {
		t.Fatalf("current = %q", p.Current())
	}
	select {
	case got := <-p.Done:
		if got != path {
			t.Fatalf("done = %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("playback did not finish")
	}
	if p.Current() != "" {
		t.Fatalf("current after finish = %q", p.Current())
	}
}

func TestStopAndSwitch(t *testing.T) {
	long := silentWAV(t, "30")
	p := New()
	if _, err := command(long); err != nil {
		t.Skip(err)
	}
	if err := p.Play(long); err != nil {
		t.Fatal(err)
	}
	second := silentWAV(t, "30")
	if err := p.Play(second); err != nil {
		t.Fatal(err)
	}
	if p.Current() != second {
		t.Fatalf("current = %q", p.Current())
	}
	select {
	case got := <-p.Done:
		if got != long {
			t.Fatalf("first done = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("switching did not stop the first file")
	}
	if p.Current() != second {
		t.Fatalf("stopping the first file cleared the second: %q", p.Current())
	}
	p.Stop()
	if p.Current() != "" {
		t.Fatal("stop did not clear current")
	}
	select {
	case <-p.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not end playback")
	}
}
