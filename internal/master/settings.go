// Package master post-processes a rendered song with ffmpeg: it repairs
// render noise, shapes tone and edges, and sets loudness and peak level.
package master

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Settings is one mastering chain. Every field is optional; its zero value
// leaves that stage out, so the zero Settings passes audio through unchanged.
type Settings struct {
	// NoiseBed is how many dB the steady noise bed is lowered.
	NoiseBed float64 `json:"noise_bed,omitempty" yaml:"noise_bed,omitempty"`
	// DCRumble removes DC offset and sub-audio rumble.
	DCRumble bool `json:"dc_rumble,omitempty" yaml:"dc_rumble,omitempty"`
	// NotchTones cuts the fixed tones at an eighth, a quarter and three
	// eighths of the sample rate.
	NotchTones bool `json:"notch_tones,omitempty" yaml:"notch_tones,omitempty"`

	// Loudness is the integrated loudness target in LUFS.
	Loudness float64 `json:"loudness,omitempty" yaml:"loudness,omitempty"`
	// Ceiling is the true-peak ceiling in dBTP.
	Ceiling float64 `json:"ceiling,omitempty" yaml:"ceiling,omitempty"`

	// LowCut is a high-pass frequency in Hz.
	LowCut float64 `json:"low_cut,omitempty" yaml:"low_cut,omitempty"`
	// Tilt tips the spectrum in dB: negative is warmer, positive brighter.
	Tilt float64 `json:"tilt,omitempty" yaml:"tilt,omitempty"`
	// DeHarsh is a gentle cut around 3–6 kHz, in dB.
	DeHarsh float64 `json:"de_harsh,omitempty" yaml:"de_harsh,omitempty"`
	// Width is the stereo width in percent; 0 and 100 leave it as it is.
	Width float64 `json:"width,omitempty" yaml:"width,omitempty"`

	// TrimStart and TrimEnd cut seconds off either end; FadeIn and FadeOut
	// fade the edges that are left, in seconds.
	TrimStart float64 `json:"trim_start,omitempty" yaml:"trim_start,omitempty"`
	TrimEnd   float64 `json:"trim_end,omitempty" yaml:"trim_end,omitempty"`
	FadeIn    float64 `json:"fade_in,omitempty" yaml:"fade_in,omitempty"`
	FadeOut   float64 `json:"fade_out,omitempty" yaml:"fade_out,omitempty"`
}

// IsZero reports whether s changes nothing.
func (s Settings) IsZero() bool { return s.normalized() == Settings{} }

// normalized drops values that change nothing, such as a width of 100%.
func (s Settings) normalized() Settings {
	if s.Width == 100 {
		s.Width = 0
	}
	return s
}

// Check reports values ffmpeg cannot apply.
func (s Settings) Check() error {
	var bad []string
	switch {
	case s.NoiseBed < 0:
		bad = append(bad, "noise bed removal can't be negative")
	case s.NoiseBed > 97:
		bad = append(bad, "noise bed removal goes up to 97 dB")
	}
	if s.Loudness != 0 && (s.Loudness < -70 || s.Loudness > -5) {
		bad = append(bad, "the loudness target must be between −70 and −5 LUFS")
	}
	if s.Ceiling > 0 || s.Ceiling < -24 {
		bad = append(bad, "the peak ceiling must be between −24 and 0 dBTP")
	}
	if s.LowCut < 0 {
		bad = append(bad, "the low cut can't be negative")
	}
	if s.DeHarsh < 0 {
		bad = append(bad, "de-harsh is a cut in dB, 0 or more")
	}
	if s.Width < 0 {
		bad = append(bad, "stereo width can't be negative")
	}
	for _, v := range []struct {
		name string
		v    float64
	}{{"trim start", s.TrimStart}, {"trim end", s.TrimEnd}, {"fade in", s.FadeIn}, {"fade out", s.FadeOut}} {
		if v.v < 0 {
			bad = append(bad, v.name+" can't be negative")
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(bad, "; "))
}

// Preset is a named chain of level, tone and edge values. Repair is never
// part of one: choosing a preset keeps the Repair settings as they are.
type Preset struct {
	Name     string
	Settings Settings
}

// Presets are the choices of the Preset row; the first is Off.
var Presets = []Preset{
	{Name: "Off"},
	{Name: "Streaming", Settings: Settings{Loudness: -14, Ceiling: -1, LowCut: 30}},
	{Name: "Loud", Settings: Settings{Loudness: -10, Ceiling: -1}},
	{Name: "Warm", Settings: Settings{Tilt: -1.5, DeHarsh: 2}},
	{Name: "Fix abrupt ending", Settings: Settings{FadeOut: 4}},
}

// With is s with every value o sets laid over it, so presets can be
// combined from the command line.
func (s Settings) With(o Settings) Settings {
	over := func(dst *float64, v float64) {
		if v != 0 {
			*dst = v
		}
	}
	over(&s.NoiseBed, o.NoiseBed)
	s.DCRumble = s.DCRumble || o.DCRumble
	s.NotchTones = s.NotchTones || o.NotchTones
	over(&s.Loudness, o.Loudness)
	over(&s.Ceiling, o.Ceiling)
	over(&s.LowCut, o.LowCut)
	over(&s.Tilt, o.Tilt)
	over(&s.DeHarsh, o.DeHarsh)
	over(&s.Width, o.Width)
	over(&s.TrimStart, o.TrimStart)
	over(&s.TrimEnd, o.TrimEnd)
	over(&s.FadeIn, o.FadeIn)
	over(&s.FadeOut, o.FadeOut)
	return s
}

// Find returns the preset named name, matched without regard to case.
func Find(name string) (Preset, bool) {
	for _, p := range Presets {
		if p.Name != "Off" && strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Preset{}, false
}

// withoutRepair is s with the Repair settings off.
func (s Settings) withoutRepair() Settings {
	s.NoiseBed, s.DCRumble, s.NotchTones = 0, false, false
	return s.normalized()
}

// Repairs reports whether any Repair setting is on.
func (s Settings) Repairs() bool { return s.NoiseBed != 0 || s.DCRumble || s.NotchTones }

// Apply is p's values with the Repair settings of s.
func (p Preset) Apply(s Settings) Settings {
	out := p.Settings
	out.NoiseBed, out.DCRumble, out.NotchTones = s.NoiseBed, s.DCRumble, s.NotchTones
	return out
}

// Current is the index in Presets that s's values outside Repair are, or
// -1 when they were set by hand.
func Current(s Settings) int {
	s = s.withoutRepair()
	for i, p := range Presets {
		if p.Settings.withoutRepair() == s {
			return i
		}
	}
	return -1
}

// Name is the preset s is, or "Custom", with "+ repair" when a Repair
// setting is on; "Off" when s changes nothing.
func Name(s Settings) string {
	name := "Custom"
	if i := Current(s); i >= 0 {
		name = Presets[i].Name
	}
	switch {
	case !s.Repairs():
		return name
	case name == "Off":
		return "Repair"
	}
	return name + " + repair"
}

// Summary is Name followed by every value s sets, such as
// "Streaming · −14 LUFS · −1 dBTP · low cut 30 Hz".
func Summary(s Settings) string {
	s = s.normalized()
	parts := []string{Name(s)}
	add := func(on bool, text string) {
		if on {
			parts = append(parts, text)
		}
	}
	add(s.NoiseBed != 0, "noise bed "+Signed(-s.NoiseBed)+" dB")
	add(s.DCRumble, "DC and rumble removed")
	add(s.NotchTones, "render tones notched")
	add(s.Loudness != 0, Signed(s.Loudness)+" LUFS")
	add(s.Ceiling != 0, Signed(s.Ceiling)+" dBTP")
	add(s.LowCut != 0, "low cut "+Num(s.LowCut)+" Hz")
	add(s.Tilt != 0, "tilt "+Signed(s.Tilt)+" dB")
	add(s.DeHarsh != 0, "de-harsh "+Num(s.DeHarsh)+" dB")
	add(s.Width != 0, "width "+Num(s.Width)+"%")
	add(s.TrimStart != 0, "trim start "+Num(s.TrimStart)+" s")
	add(s.TrimEnd != 0, "trim end "+Num(s.TrimEnd)+" s")
	add(s.FadeIn != 0, "fade in "+Num(s.FadeIn)+" s")
	add(s.FadeOut != 0, "fade out "+Num(s.FadeOut)+" s")
	return strings.Join(parts, " · ")
}

// Num writes x with no more decimals than it needs, up to two.
func Num(x float64) string {
	return strconv.FormatFloat(math.Round(x*100)/100, 'f', -1, 64)
}

// Signed is Num with a typographic minus, and a plus on positive values.
func Signed(x float64) string {
	switch n := Num(x); {
	case strings.HasPrefix(n, "-"):
		return "−" + n[1:]
	case n == "0":
		return n
	default:
		return "+" + n
	}
}
