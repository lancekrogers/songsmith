package master

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Measure is what Analyze reads from a file.
type Measure struct {
	// Loudness is integrated loudness in LUFS, TruePeak in dBTP and Range
	// the loudness range in LU.
	Loudness, TruePeak, Range float64
	// NoiseFloor is astats' noise floor in dBFS.
	NoiseFloor float64
	// Hiss is the 10th-percentile level of the energy above 8 kHz over
	// 100 ms windows, in dBFS: the steady bed under the music.
	Hiss float64
	// DC is the largest per-channel DC offset, as a fraction of full scale.
	DC       float64
	Duration float64
	Info     Info
}

// Runner masters files with the ffmpeg and ffprobe binaries on PATH.
type Runner struct {
	// FFmpeg and FFprobe are the binaries; empty means looking them up.
	FFmpeg, FFprobe string

	mu sync.Mutex
	// levels caches the level decision per input and pre-chain, so previews
	// measure the whole file once per change of the filters before it.
	levels map[string]Level
}

// NewRunner returns a Runner that finds ffmpeg and ffprobe on PATH.
func NewRunner() *Runner { return &Runner{levels: map[string]Level{}} }

// ErrNoFFmpeg means ffmpeg or ffprobe is not installed.
var ErrNoFFmpeg = errors.New("ffmpeg is not installed")

// Check reports whether ffmpeg and ffprobe can be run.
func (r *Runner) Check() error {
	for _, name := range []string{r.bin("ffmpeg"), r.bin("ffprobe")} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("%w: %s not found", ErrNoFFmpeg, filepath.Base(name))
		}
	}
	return nil
}

func (r *Runner) bin(name string) string {
	switch {
	case name == "ffmpeg" && r.FFmpeg != "":
		return r.FFmpeg
	case name == "ffprobe" && r.FFprobe != "":
		return r.FFprobe
	}
	return name
}

// fileArg keeps a path from being read as an option or a protocol.
func fileArg(path string) string { return "file:" + path }

// Probe reads a file's sample rate, channels and duration.
func (r *Runner) Probe(ctx context.Context, path string) (Info, error) {
	out, err := exec.CommandContext(ctx, r.bin("ffprobe"), "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels:format=duration", "-of", "json", fileArg(path)).Output()
	if err != nil {
		return Info{}, fmt.Errorf("probe %s: %w", filepath.Base(path), runError(err))
	}
	return parseProbe(out)
}

func parseProbe(out []byte) (Info, error) {
	var p struct {
		Streams []struct {
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return Info{}, fmt.Errorf("read ffprobe output: %w", err)
	}
	if len(p.Streams) == 0 {
		return Info{}, errors.New("the file has no audio")
	}
	sr, _ := strconv.Atoi(p.Streams[0].SampleRate)
	d, _ := strconv.ParseFloat(p.Format.Duration, 64)
	if sr <= 0 || p.Streams[0].Channels <= 0 {
		return Info{}, errors.New("the file's audio format is unreadable")
	}
	return Info{SampleRate: sr, Channels: p.Streams[0].Channels, Duration: d}, nil
}

// Analyze measures path.
func (r *Runner) Analyze(ctx context.Context, path string) (Measure, error) {
	in, err := r.Probe(ctx, path)
	if err != nil {
		return Measure{}, err
	}
	return r.analyze(ctx, path, in)
}

func (r *Runner) analyze(ctx context.Context, path string, in Info) (Measure, error) {
	m := Measure{Info: in, Duration: in.Duration}
	graph := "[0:a]asplit=2[a][b];[a]loudnorm=print_format=json[x];[b]astats=measure_perchannel=DC_offset:measure_overall=Noise_floor[y]"
	stderr, err := r.ffmpeg(ctx, nil, "-i", fileArg(path), "-filter_complex", graph,
		"-map", "[x]", "-f", "null", "-", "-map", "[y]", "-f", "null", "-")
	if err != nil {
		return m, fmt.Errorf("measure %s: %w", filepath.Base(path), err)
	}
	ln, err := parseLoudnorm(stderr)
	if err != nil {
		return m, fmt.Errorf("measure %s: %w", filepath.Base(path), err)
	}
	m.Loudness, m.TruePeak, m.Range = ln.I, ln.TP, ln.LRA
	m.DC, m.NoiseFloor = parseAstats(stderr)
	hiss, samples, err := r.hiss(ctx, path, in)
	if err != nil {
		return m, fmt.Errorf("measure %s: %w", filepath.Base(path), err)
	}
	m.Hiss = hiss
	if samples > 0 {
		m.Duration = float64(samples) / float64(in.SampleRate)
	}
	return m, nil
}

// hiss decodes the energy above 8 kHz and returns its 10th-percentile level
// over 100 ms windows, with the number of sample frames decoded.
func (r *Runner) hiss(ctx context.Context, path string, in Info) (float64, int, error) {
	cmd := exec.CommandContext(ctx, r.bin("ffmpeg"), "-nostdin", "-hide_banner", "-v", "error", "-i", fileArg(path),
		"-af", "highpass=f=8000:p=2,highpass=f=8000:p=2", "-f", "f32le", "-acodec", "pcm_f32le", "-")
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, 0, runError(err)
	}
	level, frames, readErr := windowFloor(bufio.NewReaderSize(out, 1<<16), in.Channels, max(in.SampleRate/10, 1))
	if err := cmd.Wait(); err != nil {
		return 0, 0, ffmpegError(err, errBuf.Bytes())
	}
	return level, frames, readErr
}

// windowFloor reads interleaved float32 frames of channels samples and
// returns the 10th-percentile RMS level, in dBFS, over windows of window
// frames, and the number of frames read.
func windowFloor(rd io.Reader, channels, window int) (float64, int, error) {
	buf := make([]byte, 4*channels)
	var levels []float64
	var sum float64
	frames, n := 0, 0
	flush := func() {
		if n > 0 {
			levels = append(levels, 10*math.Log10(max(sum/float64(n*channels), 1e-12)))
		}
		sum, n = 0, 0
	}
	for {
		if _, err := io.ReadFull(rd, buf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return 0, frames, err
		}
		for c := range channels {
			v := float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[4*c:])))
			sum += v * v
		}
		frames++
		n++
		if n == window {
			flush()
		}
	}
	if n >= window/2 {
		flush()
	}
	if len(levels) == 0 {
		return -120, frames, nil
	}
	slices.Sort(levels)
	return math.Round(levels[len(levels)/10]*10) / 10, frames, nil
}

var loudnormJSON = regexp.MustCompile(`(?s)\{\s*"input_i".*?\}`)

func parseLoudnorm(stderr []byte) (Loudness, error) {
	raw := loudnormJSON.Find(stderr)
	if raw == nil {
		return Loudness{}, errors.New("ffmpeg printed no loudness measurement")
	}
	var v struct {
		I      string `json:"input_i"`
		TP     string `json:"input_tp"`
		LRA    string `json:"input_lra"`
		Thresh string `json:"input_thresh"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Loudness{}, fmt.Errorf("read loudness measurement: %w", err)
	}
	f := func(s string, silent float64) float64 {
		x, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || math.IsInf(x, 0) || math.IsNaN(x) {
			return silent
		}
		return x
	}
	return Loudness{I: f(v.I, -70), TP: f(v.TP, -99), LRA: f(v.LRA, 0), Thresh: f(v.Thresh, -70)}, nil
}

var (
	astatsDC    = regexp.MustCompile(`DC offset: (-?[0-9.]+)`)
	astatsFloor = regexp.MustCompile(`Noise floor dB: (-?[0-9.]+|-inf)`)
)

// parseAstats reads the largest per-channel DC offset and the overall
// noise floor from astats' summary.
func parseAstats(stderr []byte) (dc, floor float64) {
	for _, m := range astatsDC.FindAllSubmatch(stderr, -1) {
		v, _ := strconv.ParseFloat(string(m[1]), 64)
		if math.Abs(v) > math.Abs(dc) {
			dc = v
		}
	}
	floor = -120
	if m := astatsFloor.FindSubmatch(stderr); m != nil {
		if v, err := strconv.ParseFloat(string(m[1]), 64); err == nil {
			floor = v
		}
	}
	return dc, floor
}

// level decides the level stage for s on path, measuring the pre-chain the
// first time it is asked for this input and these filters.
func (r *Runner) level(ctx context.Context, path string, s Settings, in Info, progress func(float64)) (*Level, error) {
	if s.Loudness == 0 {
		return nil, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	pre := PreChain(s, in)
	key := fmt.Sprintf("%s|%d|%d|%s|%g|%g", path, fi.Size(), fi.ModTime().UnixNano(), strings.Join(pre, ","), s.Loudness, s.Ceiling)
	r.mu.Lock()
	lv, ok := r.levels[key]
	r.mu.Unlock()
	if ok {
		return &lv, nil
	}
	m, err := r.measure(ctx, path, pre, in, progress)
	if err != nil {
		return nil, err
	}
	lv = DecideLevel(s, m)
	if lv.Limit {
		// Limiting takes some loudness; measure what the limiter leaves and
		// make that up, once.
		after, err := r.measure(ctx, path, append(slices.Clone(pre), limiter(in, lv.Gain, s.Ceiling)...), in, nil)
		if err != nil {
			return nil, err
		}
		lv.Gain += s.Loudness - after.I
	}
	r.mu.Lock()
	if r.levels == nil {
		r.levels = map[string]Level{}
	}
	r.levels[key] = lv
	r.mu.Unlock()
	return &lv, nil
}

// measure runs filters on path and reads loudnorm's measurement of them.
func (r *Runner) measure(ctx context.Context, path string, filters []string, in Info, progress func(float64)) (Loudness, error) {
	f := append(slices.Clone(filters), "loudnorm=print_format=json")
	stderr, err := r.ffmpeg(ctx, progressFor(progress, in.Duration), "-i", fileArg(path), "-af", strings.Join(f, ","), "-f", "null", "-")
	if err != nil {
		return Loudness{}, err
	}
	return parseLoudnorm(stderr)
}

// Render masters in into out as FLAC and returns a measurement of the
// result. It writes a hidden temporary file beside out and links it into
// place, so out appears whole or not at all and nothing is overwritten.
func (r *Runner) Render(ctx context.Context, in, out string, s Settings, progress func(float64)) (Measure, error) {
	if progress == nil {
		progress = func(float64) {}
	}
	if err := s.Check(); err != nil {
		return Measure{}, err
	}
	if _, err := os.Lstat(out); err == nil {
		return Measure{}, fmt.Errorf("%s already exists", filepath.Base(out))
	}
	info, err := r.Probe(ctx, in)
	if err != nil {
		return Measure{}, err
	}
	if s.Length(info) <= 0 {
		return Measure{}, errors.New("the trims remove the whole song")
	}
	lv, err := r.level(ctx, in, s, info, func(f float64) { progress(f * 0.35) })
	if err != nil {
		return Measure{}, fmt.Errorf("measure %s: %w", filepath.Base(in), err)
	}
	progress(0.35)
	tmp, err := os.CreateTemp(filepath.Dir(out), ".master-*.flac")
	if err != nil {
		return Measure{}, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	args := renderArgs(in, tmp.Name(), Chain(s, info, lv), info)
	if _, err := r.ffmpeg(ctx, progressFor(func(f float64) { progress(0.35 + f*0.55) }, s.Length(info)), args...); err != nil {
		return Measure{}, fmt.Errorf("master %s: %w", filepath.Base(in), err)
	}
	progress(0.9)
	m, err := r.Analyze(ctx, tmp.Name())
	if err != nil {
		return Measure{}, err
	}
	if err := place(tmp.Name(), out); err != nil {
		return Measure{}, fmt.Errorf("save %s: %w", filepath.Base(out), err)
	}
	progress(1)
	return m, nil
}

// place moves tmp to out without replacing a file already at out: a hard
// link where the filesystem has them, a checked rename where it does not.
func place(tmp, out string) error {
	err := os.Link(tmp, out)
	if err == nil || errors.Is(err, os.ErrExist) {
		return err
	}
	if _, statErr := os.Lstat(out); statErr == nil {
		return fmt.Errorf("%s already exists", filepath.Base(out))
	}
	return os.Rename(tmp, out)
}

// flacFrames hands the FLAC encoder whole frames: after a seek, or a trim,
// the first frame is short, and ffmpeg's FLAC encoder fails to open on it.
const flacFrames = ",asetnsamples=n=4608:p=0"

// renderArgs are ffmpeg's arguments for a render of in to out.
func renderArgs(in, out, chain string, info Info) []string {
	return []string{"-i", fileArg(in), "-map", "0:a:0", "-map_metadata", "0", "-af", chain + flacFrames,
		"-ar", strconv.Itoa(info.SampleRate), "-ac", strconv.Itoa(info.Channels), "-c:a", "flac", "-f", "flac", "-y", fileArg(out)}
}

// Preview masters seconds of in, starting at start on the output's
// timeline, into out, and measures the result. Its level stage is the one a
// render of the whole file would use, so the excerpt plays at the level the
// master will have.
func (r *Runner) Preview(ctx context.Context, in, out string, s Settings, start, seconds float64) (Measure, error) {
	if err := s.Check(); err != nil {
		return Measure{}, err
	}
	info, err := r.Probe(ctx, in)
	if err != nil {
		return Measure{}, err
	}
	if s.Length(info) <= 0 {
		return Measure{}, errors.New("the trims remove the whole song")
	}
	lv, err := r.level(ctx, in, s, info, nil)
	if err != nil {
		return Measure{}, fmt.Errorf("measure %s: %w", filepath.Base(in), err)
	}
	sp := excerpt(s, info, start, seconds)
	args := []string{"-ss", Num(sp.From), "-t", Num(sp.Skip + sp.Seconds), "-i", fileArg(in), "-map", "0:a:0",
		"-af", excerptChain(s, info, sp, lv) + flacFrames,
		"-ar", strconv.Itoa(info.SampleRate), "-ac", strconv.Itoa(info.Channels), "-c:a", "flac", "-f", "flac", "-y", fileArg(out)}
	if _, err := r.ffmpeg(ctx, nil, args...); err != nil {
		return Measure{}, fmt.Errorf("preview %s: %w", filepath.Base(in), err)
	}
	return r.Analyze(ctx, out)
}

// Original is the part of s that cuts the original the same way as the
// master, for an excerpt of the original at the same place.
func Original(s Settings) Settings {
	return Settings{TrimStart: s.TrimStart, TrimEnd: s.TrimEnd}
}

// ffmpeg runs ffmpeg with args and returns what it wrote to stderr. With
// progress set it reads -progress output from stdout.
func (r *Runner) ffmpeg(ctx context.Context, progress func(outSeconds float64), args ...string) ([]byte, error) {
	full := []string{"-nostdin", "-hide_banner", "-nostats"}
	if progress != nil {
		full = append(full, "-progress", "pipe:1")
	}
	cmd := exec.CommandContext(ctx, r.bin("ffmpeg"), append(full, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var wg sync.WaitGroup
	if progress != nil {
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		wg.Go(func() { readProgress(out, progress) })
	}
	if err := cmd.Start(); err != nil {
		return nil, runError(err)
	}
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ffmpegError(err, stderr.Bytes())
	}
	return stderr.Bytes(), nil
}

// progressFor turns -progress output times into a fraction of total.
func progressFor(progress func(float64), total float64) func(float64) {
	if progress == nil {
		return nil
	}
	return func(sec float64) {
		if total > 0 {
			progress(min(max(sec/total, 0), 1))
		}
	}
}

// readProgress reports each out_time_us line of ffmpeg's -progress output
// as seconds.
func readProgress(r io.Reader, progress func(float64)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "out_time_us=")
		if !ok {
			continue
		}
		if us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && us >= 0 {
			progress(float64(us) / 1e6)
		}
	}
}

func runError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w (%v)", ErrNoFFmpeg, err)
	}
	return err
}

// ffmpegError is err with the last lines ffmpeg printed.
func ffmpegError(err error, stderr []byte) error {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	tail := strings.TrimSpace(strings.Join(lines[max(len(lines)-3, 0):], " "))
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, tail)
}
