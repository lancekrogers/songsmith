package master

import (
	"fmt"
	"math"
	"strings"
)

// Info describes an input file.
type Info struct {
	SampleRate int
	Channels   int
	Duration   float64
}

// Length is how long the output of s runs for an input of in.Duration.
func (s Settings) Length(in Info) float64 {
	return max(in.Duration-s.TrimStart-s.TrimEnd, 0)
}

// Loudness is a loudnorm measurement of the audio before the level stage.
type Loudness struct {
	I, TP, LRA, Thresh float64
}

// Level is how the level stage reaches the loudness target, decided once
// from a measurement and used for previews and the render alike.
type Level struct {
	Measured Loudness
	// Limit is set when the target gain would push peaks past the ceiling:
	// the stage then applies Gain dB into a true-peak limiter instead of
	// loudnorm's linear gain.
	Limit bool
	Gain  float64
}

// Linear reports whether loudnorm can reach target with one fixed gain
// without passing ceiling.
func (m Loudness) Linear(target, ceiling float64) bool {
	return m.TP+(target-m.I) <= ceiling-0.05 && m.LRA < 19 && ceiling >= -9
}

// limiterMargin is how far under the ceiling the limiter is set, since a
// limiter on oversampled audio still lets small true-peak overs through.
const limiterMargin = 0.5

// repairDelay is afftdn's latency, which the repair stage pads and trims
// away so its output stays sample-aligned with its input.
func repairDelay(sampleRate int) int { return int(float64(sampleRate) * 0.025) }

// toneFreqs are the fixed tones the renderer leaves, at an eighth, a
// quarter and three eighths of the sample rate.
func toneFreqs(sampleRate int) [3]float64 {
	sr := float64(sampleRate)
	return [3]float64{sr / 8, sr / 4, sr * 3 / 8}
}

// repairFilters remove DC and rumble, then the render tones, then the
// noise bed, in the order that sounded best on noisy renders.
func repairFilters(s Settings, in Info) []string {
	var f []string
	if s.DCRumble {
		f = append(f, "highpass=f=20:p=2", "highpass=f=20:p=2")
	}
	if s.NotchTones {
		for _, hz := range toneFreqs(in.SampleRate) {
			f = append(f, "bandreject=f="+Num(hz)+":width_type=q:w=40")
		}
	}
	if s.NoiseBed > 0 {
		n := repairDelay(in.SampleRate)
		f = append(f, fmt.Sprintf("apad=pad_len=%d", n),
			"afftdn=nr="+Num(s.NoiseBed)+":nf=-40:tn=1",
			fmt.Sprintf("atrim=start_sample=%d", n))
	}
	return f
}

func toneFilters(s Settings, in Info) []string {
	var f []string
	if s.LowCut > 0 {
		f = append(f, "highpass=f="+Num(s.LowCut)+":p=2")
	}
	if s.Tilt != 0 {
		f = append(f, "lowshelf=f=250:g="+Num(-s.Tilt/2), "highshelf=f=4000:g="+Num(s.Tilt/2))
	}
	if s.DeHarsh > 0 {
		f = append(f, "equalizer=f=4500:t=o:w=1.2:g="+Num(-s.DeHarsh))
	}
	if w := s.normalized().Width; w > 0 && in.Channels == 2 {
		f = append(f, "extrastereo=m="+Num(w/100)+":c=false")
	}
	return f
}

// span is the part of the output timeline a chain produces: from Start for
// Seconds, decoded from the input from From (all in seconds; the output
// timeline starts at TrimStart in the input).
type span struct {
	// From is where decoding starts in the input; Skip is how much of
	// the decoded audio is pre-roll dropped at the end of the chain.
	From, Skip, Seconds float64
}

// excerpt is the span for seconds of output from start, with enough
// pre-roll for the noise reduction to settle and for any fade the excerpt
// lands inside to be drawn from its beginning.
func excerpt(s Settings, in Info, start, seconds float64) span {
	length := s.Length(in)
	start = min(max(start, 0), length)
	seconds = min(seconds, length-start)
	from := max(start-1, 0)
	if from < s.FadeIn {
		from = 0
	}
	if fadeOut := length - s.FadeOut; s.FadeOut > 0 && start+seconds > fadeOut {
		from = min(from, max(fadeOut, 0))
	}
	return span{From: s.TrimStart + from, Skip: start - from, Seconds: seconds}
}

// trimFilters cut the trims from the whole input of a render; an excerpt's
// input is already seeked past them.
func trimFilters(s Settings, in Info) []string {
	if s.TrimStart <= 0 && s.TrimEnd <= 0 {
		return nil
	}
	var cut []string
	if s.TrimStart > 0 {
		cut = append(cut, "start="+Num(s.TrimStart))
	}
	if s.TrimEnd > 0 {
		cut = append(cut, "end="+Num(s.TrimStart+s.Length(in)))
	}
	return []string{"atrim=" + strings.Join(cut, ":"), "asetpts=PTS-STARTPTS"}
}

// fadeFilters fade the output's edges; at is where the audio they get
// starts on the output timeline, and decoded how long it runs.
func fadeFilters(s Settings, in Info, at, decoded float64) []string {
	var f []string
	if s.FadeIn > 0 && at == 0 {
		f = append(f, "afade=t=in:st=0:d="+Num(s.FadeIn))
	}
	if s.FadeOut > 0 {
		if st := s.Length(in) - s.FadeOut - at; st >= 0 && st < decoded {
			f = append(f, "afade=t=out:st="+Num(st)+":d="+Num(s.FadeOut))
		}
	}
	return f
}

// PreChain is every filter before the level stage, for a whole render.
func PreChain(s Settings, in Info) []string {
	s = s.normalized()
	f := repairFilters(s, in)
	f = append(f, trimFilters(s, in)...)
	f = append(f, toneFilters(s, in)...)
	return append(f, fadeFilters(s, in, 0, math.Inf(1))...)
}

func excerptPreChain(s Settings, in Info, sp span) []string {
	s = s.normalized()
	f := repairFilters(s, in)
	f = append(f, toneFilters(s, in)...)
	return append(f, fadeFilters(s, in, sp.From-s.TrimStart, sp.Skip+sp.Seconds)...)
}

// levelFilters reach the loudness target, or hold the ceiling alone.
func levelFilters(s Settings, in Info, lv *Level) []string {
	ceiling := s.Ceiling
	switch {
	case s.Loudness != 0 && lv != nil && !lv.Limit:
		m := lv.Measured
		lra := min(max(math.Ceil(m.LRA)+1, 7), 20)
		return []string{fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:linear=true",
			Num(s.Loudness), Num(ceiling), Num(lra), Num(m.I), Num(m.TP), Num(max(m.LRA, 0.01)), Num(m.Thresh)),
			fmt.Sprintf("aresample=%d", in.SampleRate)}
	case s.Loudness != 0 && lv != nil:
		return limiter(in, lv.Gain, ceiling)
	case s.Ceiling != 0:
		return limiter(in, 0, ceiling)
	}
	return nil
}

// limiter applies gain dB into a limiter at ceiling dBTP, run at four times
// the sample rate so it catches peaks between samples.
func limiter(in Info, gain, ceiling float64) []string {
	limit := min(max(math.Pow(10, (ceiling-limiterMargin)/20), 0.0625), 1)
	levelIn := min(max(math.Pow(10, gain/20), 0.015625), 64)
	return []string{
		fmt.Sprintf("aresample=%d", in.SampleRate*4),
		fmt.Sprintf("alimiter=level_in=%s:limit=%s:attack=5:release=50:level=false:latency=true", num4(levelIn), num4(limit)),
		fmt.Sprintf("aresample=%d", in.SampleRate),
	}
}

func num4(x float64) string { return fmt.Sprintf("%.4f", x) }

// Chain is the whole filter chain of a render. lv is the level stage's
// decision; it is needed only when s sets a loudness target.
func Chain(s Settings, in Info, lv *Level) string {
	f := append(PreChain(s, in), levelFilters(s, in, lv)...)
	if len(f) == 0 {
		return "anull"
	}
	return strings.Join(f, ",")
}

// excerptChain is Chain for a span: the same filters, then the pre-roll cut.
func excerptChain(s Settings, in Info, sp span, lv *Level) string {
	f := append(excerptPreChain(s, in, sp), levelFilters(s, in, lv)...)
	if sp.Skip > 0 {
		f = append(f, "atrim=start="+Num(sp.Skip), "asetpts=PTS-STARTPTS")
	}
	if len(f) == 0 {
		return "anull"
	}
	return strings.Join(f, ",")
}

// DecideLevel picks the level stage for a measurement of the pre-chain.
func DecideLevel(s Settings, m Loudness) Level {
	ceiling := s.Ceiling
	lv := Level{Measured: m}
	if !m.Linear(s.Loudness, ceiling) {
		lv.Limit, lv.Gain = true, s.Loudness-m.I
	}
	return lv
}
