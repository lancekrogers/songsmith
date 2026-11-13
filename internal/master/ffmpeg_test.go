package master

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// needFFmpeg skips a test when ffmpeg or ffprobe is not installed. These
// tests only encode and measure files; nothing is played.
func needFFmpeg(t *testing.T) *Runner {
	t.Helper()
	r := NewRunner()
	if err := r.Check(); err != nil {
		t.Skip(err)
	}
	return r
}

// generate writes a FLAC made by a lavfi expression.
func generate(t *testing.T, name, expr string, rate int, seconds float64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	src := "aevalsrc=" + expr + ":s=" + Num(float64(rate)) + ":d=" + Num(seconds)
	out, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", src, "-c:a", "flac", "-f", "flac", path).CombinedOutput()
	if err != nil {
		t.Fatalf("generate %s: %v: %s", name, err, out)
	}
	return path
}

const toneAndNoise = "'0.25*sin(2*PI*440*t)+0.06*(random(0)-0.5)|0.25*sin(2*PI*554*t)+0.06*(random(1)-0.5)'"

func render(t *testing.T, r *Runner, in string, s Settings) (string, Measure) {
	t.Helper()
	out := filepath.Join(filepath.Dir(in), "out.flac")
	var last float64
	m, err := r.Render(context.Background(), in, out, s, func(f float64) {
		if f < last {
			t.Errorf("progress went back from %v to %v", last, f)
		}
		last = f
	})
	if err != nil {
		t.Fatal(err)
	}
	if last != 1 {
		t.Fatalf("progress ended at %v", last)
	}
	return out, m
}

func TestRenderReachesLoudnessUnderTheCeiling(t *testing.T) {
	r := needFFmpeg(t)
	cases := []struct {
		preset, signal string
		limited        bool
	}{
		{"Streaming", toneAndNoise, false},
		{"Loud", toneAndNoise, false},
		// Sharp clicks over a quiet tone: a fixed gain would push them far
		// past the ceiling.
		{"Loud", "'0.1*sin(2*PI*440*t)+if(lt(mod(n,4410),4),0.8,0)|0.1*sin(2*PI*554*t)+if(lt(mod(n,4410),4),0.8,0)'", true},
	}
	for _, c := range cases {
		t.Run(c.preset, func(t *testing.T) {
			p, _ := Find(c.preset)
			in := generate(t, "in.flac", c.signal, 44100, 5)
			before, err := r.Analyze(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			lv := DecideLevel(p.Settings, Loudness{I: before.Loudness, TP: before.TruePeak, LRA: before.Range})
			if lv.Limit != c.limited {
				t.Fatalf("limited = %v, want %v", lv.Limit, c.limited)
			}
			_, m := render(t, r, in, p.Settings)
			t.Logf("%s: %.2f → %.2f LUFS, %.2f → %.2f dBTP", c.preset, before.Loudness, m.Loudness, before.TruePeak, m.TruePeak)
			if math.Abs(m.Loudness-p.Settings.Loudness) > 0.5 {
				t.Errorf("loudness %.2f LUFS, want %.0f ± 0.5", m.Loudness, p.Settings.Loudness)
			}
			if m.TruePeak > p.Settings.Ceiling {
				t.Errorf("true peak %.2f dBTP is over the %.0f ceiling", m.TruePeak, p.Settings.Ceiling)
			}
			if m.Info.SampleRate != 44100 || m.Info.Channels != 2 || m.Duration != before.Duration {
				t.Errorf("format or length changed: %+v, %.5f s → %.5f s", m.Info, before.Duration, m.Duration)
			}
		})
	}
}

func TestRenderKeepsFormatAndNeverOverwrites(t *testing.T) {
	r := needFFmpeg(t)
	in := generate(t, "in.flac", "'0.2*sin(2*PI*440*t)'", 48000, 3)
	out, m := render(t, r, in, Settings{Tilt: -1})
	codec, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name",
		"-of", "default=nw=1:nk=1", out).Output()
	if err != nil || string(codec) != "flac\n" {
		t.Fatalf("codec %q, %v", codec, err)
	}
	if m.Info.SampleRate != 48000 || m.Info.Channels != 1 {
		t.Fatalf("format = %+v, want 48 kHz mono", m.Info)
	}
	before, _ := os.ReadFile(out)
	if _, err := r.Render(context.Background(), in, out, Settings{Tilt: 1}, nil); err == nil {
		t.Fatal("rendering onto an existing file should fail")
	}
	after, _ := os.ReadFile(out)
	if string(before) != string(after) {
		t.Fatal("the existing file changed")
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".master-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestTrimsAndFades(t *testing.T) {
	r := needFFmpeg(t)
	in := generate(t, "in.flac", "'0.3*sin(2*PI*440*t)'", 44100, 5)
	out, m := render(t, r, in, Settings{TrimStart: 1, TrimEnd: 0.5, FadeIn: 0.5, FadeOut: 1.5})
	if math.Abs(m.Duration-3.5) > 1.0/44100 {
		t.Fatalf("duration %.5f s, want 3.5", m.Duration)
	}
	head := excerptLevel(t, out, 0, 0.1)
	middle := excerptLevel(t, out, 1.5, 0.5)
	tail := excerptLevel(t, out, 3.35, 0.1)
	if head > middle-10 || tail > middle-10 {
		t.Fatalf("edges not faded: head %.1f, middle %.1f, tail %.1f dB", head, middle, tail)
	}
}

// excerptLevel is the RMS level of a part of a file.
func excerptLevel(t *testing.T, path string, start, seconds float64) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-ss", Num(start), "-t", Num(seconds), "-i", path,
		"-ac", "1", "-f", "f32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	n := len(out) / 4
	for i := range n {
		v := float64(math.Float32frombits(uint32(out[4*i]) | uint32(out[4*i+1])<<8 | uint32(out[4*i+2])<<16 | uint32(out[4*i+3])<<24))
		sum += v * v
	}
	return 10 * math.Log10(max(sum/float64(max(n, 1)), 1e-12))
}

func TestRepairLowersHissKeepsLengthAndRemovesDC(t *testing.T) {
	r := needFFmpeg(t)
	expr := "'0.25*sin(2*PI*440*t)+0.02*(random(0)-0.5)+0.03|0.25*sin(2*PI*330*t)+0.02*(random(1)-0.5)+0.03'"
	in := generate(t, "in.flac", expr, 44100, 5)
	before, err := r.Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, after := render(t, r, in, Settings{NoiseBed: 20, DCRumble: true, NotchTones: true})
	t.Logf("hiss %.1f → %.1f dB, DC %.4f → %.4f", before.Hiss, after.Hiss, before.DC, after.DC)
	if after.Duration != before.Duration {
		t.Errorf("length changed: %.6f → %.6f s", before.Duration, after.Duration)
	}
	if before.Hiss-after.Hiss < 10 {
		t.Errorf("hiss dropped %.1f dB, want 10 or more", before.Hiss-after.Hiss)
	}
	if math.Abs(before.DC) < 0.02 || math.Abs(after.DC) > 0.001 {
		t.Errorf("DC %.4f → %.4f", before.DC, after.DC)
	}
}

func TestRepairStaysSampleAligned(t *testing.T) {
	r := needFFmpeg(t)
	// A click one second in, over quiet noise.
	in := generate(t, "in.flac", "'if(eq(n,44100),0.9,0)+0.01*(random(0)-0.5)'", 44100, 3)
	out, _ := render(t, r, in, Settings{NoiseBed: 20})
	for _, path := range []string{in, out} {
		if at := loudestSample(t, path); at != 44100 {
			t.Fatalf("%s: click at sample %d, want 44100", filepath.Base(path), at)
		}
	}
}

func loudestSample(t *testing.T, path string) int {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-ac", "1", "-f", "f32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	best, at := 0.0, -1
	for i := range len(out) / 4 {
		v := math.Abs(float64(math.Float32frombits(uint32(out[4*i]) | uint32(out[4*i+1])<<8 | uint32(out[4*i+2])<<16 | uint32(out[4*i+3])<<24)))
		if v > best {
			best, at = v, i
		}
	}
	return at
}

func TestPreviewMatchesTheRenderLevel(t *testing.T) {
	r := needFFmpeg(t)
	// A seek into the middle of a FLAC starts on a short frame, which the
	// FLAC encoder once refused.
	in := generate(t, "in.flac", toneAndNoise, 44100, 40)
	p, _ := Find("Streaming")
	out := filepath.Join(t.TempDir(), "preview.flac")
	m, err := r.Preview(context.Background(), in, out, p.Settings, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Duration-3) > 0.01 {
		t.Fatalf("preview runs %.3f s, want 3", m.Duration)
	}
	if math.Abs(m.Loudness+14) > 0.6 {
		t.Fatalf("preview at %.2f LUFS, want about −14", m.Loudness)
	}
	orig := filepath.Join(t.TempDir(), "original.flac")
	o, err := r.Preview(context.Background(), in, orig, Original(p.Settings), 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(o.Duration-3) > 0.01 || math.Abs(o.Loudness-m.Loudness) < 1 {
		t.Fatalf("original excerpt %.3f s at %.2f LUFS", o.Duration, o.Loudness)
	}
}
