package master

import (
	"os"
	"slices"
	"strings"
	"testing"
)

var (
	stereo44 = Info{SampleRate: 44100, Channels: 2, Duration: 180}
	measured = &Level{Measured: Loudness{I: -18.2, TP: -6, LRA: 6.1, Thresh: -28.5}}
)

func TestZeroSettingsPassThrough(t *testing.T) {
	if !(Settings{}).IsZero() || !(Settings{Width: 100}).IsZero() {
		t.Fatal("zero settings are not zero")
	}
	if got := Chain(Settings{}, stereo44, nil); got != "anull" {
		t.Fatalf("chain = %q", got)
	}
	if got := Chain(Settings{Width: 100}, stereo44, nil); got != "anull" {
		t.Fatalf("width 100%% changed something: %q", got)
	}
}

func TestChainForEachSetting(t *testing.T) {
	cases := []struct {
		name string
		s    Settings
		in   Info
		want string
	}{
		{"noise bed", Settings{NoiseBed: 20}, stereo44,
			"apad=pad_len=1102,afftdn=nr=20:nf=-40:tn=1,atrim=start_sample=1102"},
		{"noise bed at 48 kHz", Settings{NoiseBed: 12.5}, Info{SampleRate: 48000, Channels: 2, Duration: 10},
			"apad=pad_len=1200,afftdn=nr=12.5:nf=-40:tn=1,atrim=start_sample=1200"},
		{"dc and rumble", Settings{DCRumble: true}, stereo44, "highpass=f=20:p=2,highpass=f=20:p=2"},
		{"notches at 44.1 kHz", Settings{NotchTones: true}, stereo44,
			"bandreject=f=5512.5:width_type=q:w=40,bandreject=f=11025:width_type=q:w=40,bandreject=f=16537.5:width_type=q:w=40"},
		{"notches at 48 kHz", Settings{NotchTones: true}, Info{SampleRate: 48000, Channels: 2, Duration: 10},
			"bandreject=f=6000:width_type=q:w=40,bandreject=f=12000:width_type=q:w=40,bandreject=f=18000:width_type=q:w=40"},
		{"all of repair, in order", Settings{NoiseBed: 20, DCRumble: true, NotchTones: true}, stereo44,
			"highpass=f=20:p=2,highpass=f=20:p=2,bandreject=f=5512.5:width_type=q:w=40,bandreject=f=11025:width_type=q:w=40," +
				"bandreject=f=16537.5:width_type=q:w=40,apad=pad_len=1102,afftdn=nr=20:nf=-40:tn=1,atrim=start_sample=1102"},
		{"low cut", Settings{LowCut: 30}, stereo44, "highpass=f=30:p=2"},
		{"warmer", Settings{Tilt: -1.5}, stereo44, "lowshelf=f=250:g=0.75,highshelf=f=4000:g=-0.75"},
		{"brighter", Settings{Tilt: 2}, stereo44, "lowshelf=f=250:g=-1,highshelf=f=4000:g=1"},
		{"de-harsh", Settings{DeHarsh: 2}, stereo44, "equalizer=f=4500:t=o:w=1.2:g=-2"},
		{"width", Settings{Width: 120}, stereo44, "extrastereo=m=1.2:c=false"},
		{"width on mono", Settings{Width: 120}, Info{SampleRate: 44100, Channels: 1, Duration: 10}, "anull"},
		{"trim start", Settings{TrimStart: 1.5}, stereo44, "atrim=start=1.5,asetpts=PTS-STARTPTS"},
		{"trim end", Settings{TrimEnd: 10}, stereo44, "atrim=end=170,asetpts=PTS-STARTPTS"},
		{"both trims", Settings{TrimStart: 2, TrimEnd: 10}, stereo44, "atrim=start=2:end=170,asetpts=PTS-STARTPTS"},
		{"fade in", Settings{FadeIn: 0.5}, stereo44, "afade=t=in:st=0:d=0.5"},
		{"fade out", Settings{FadeOut: 4}, stereo44, "afade=t=out:st=176:d=4"},
		{"fade out after a trim", Settings{TrimEnd: 10, FadeOut: 4}, stereo44,
			"atrim=end=170,asetpts=PTS-STARTPTS,afade=t=out:st=166:d=4"},
		{"ceiling alone", Settings{Ceiling: -1}, stereo44,
			"aresample=176400,alimiter=level_in=1.0000:limit=0.8414:attack=5:release=50:level=false:latency=true,aresample=44100"},
		{"loudness", Settings{Loudness: -14, Ceiling: -1}, stereo44,
			"loudnorm=I=-14:TP=-1:LRA=8:measured_I=-18.2:measured_TP=-6:measured_LRA=6.1:measured_thresh=-28.5:linear=true,aresample=44100"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Chain(c.s, c.in, measured); got != c.want {
				t.Fatalf("chain\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestChainOrderRepairFirst(t *testing.T) {
	s := Settings{NoiseBed: 20, DCRumble: true, LowCut: 30, Tilt: 1, FadeOut: 4, TrimStart: 1, Loudness: -14, Ceiling: -1}
	f := strings.Split(Chain(s, stereo44, measured), ",")
	at := func(prefix string) int {
		return slices.IndexFunc(f, func(x string) bool { return strings.HasPrefix(x, prefix) })
	}
	order := []int{at("highpass=f=20"), at("afftdn"), at("atrim=start=1"), at("highpass=f=30"), at("lowshelf"), at("afade"), at("loudnorm")}
	if !slices.IsSorted(order) || slices.Contains(order, -1) {
		t.Fatalf("filters out of order: %v in %v", order, f)
	}
}

func TestPresetChains(t *testing.T) {
	want := map[string]string{
		"Streaming":         "highpass=f=30:p=2,loudnorm=I=-14:TP=-1:LRA=8:measured_I=-18.2:measured_TP=-6:measured_LRA=6.1:measured_thresh=-28.5:linear=true,aresample=44100",
		"Loud":              "aresample=176400,alimiter=level_in=2.5704:limit=0.8414:attack=5:release=50:level=false:latency=true,aresample=44100",
		"Warm":              "lowshelf=f=250:g=0.75,highshelf=f=4000:g=-0.75,equalizer=f=4500:t=o:w=1.2:g=-2",
		"Fix abrupt ending": "afade=t=out:st=176:d=4",
	}
	for _, p := range Presets {
		if p.Name == "Off" {
			if !p.Settings.IsZero() {
				t.Fatal("Off sets something")
			}
			continue
		}
		if p.Settings.NoiseBed != 0 || p.Settings.DCRumble || p.Settings.NotchTones {
			t.Fatalf("%s includes Repair", p.Name)
		}
		lv := DecideLevel(p.Settings, measured.Measured)
		if got := Chain(p.Settings, stereo44, &lv); got != want[p.Name] {
			t.Errorf("%s\n got %s\nwant %s", p.Name, got, want[p.Name])
		}
	}
}

func TestDecideLevel(t *testing.T) {
	m := Loudness{I: -18.2, TP: -6, LRA: 6.1, Thresh: -28.5}
	if lv := DecideLevel(Settings{Loudness: -14, Ceiling: -1}, m); lv.Limit {
		t.Fatal("a 4.2 dB gain that leaves the peak at −1.8 dBTP fits a −1 dBTP ceiling linearly")
	}
	lv := DecideLevel(Settings{Loudness: -10, Ceiling: -1}, m)
	if !lv.Limit || lv.Gain != 8.2 {
		t.Fatalf("level = %+v", lv)
	}
	if lv := DecideLevel(Settings{Loudness: -14, Ceiling: -12}, m); !lv.Limit {
		t.Fatal("a ceiling below loudnorm's range must use the limiter")
	}
}

func TestPresetsSelectAndCustom(t *testing.T) {
	if Current(Settings{}) != 0 || Name(Settings{}) != "Off" || Name(Settings{Width: 100}) != "Off" {
		t.Fatal("zero settings are not Off")
	}
	streaming, _ := Find("streaming")
	s := streaming.Settings
	if Current(s) != 1 || Name(s) != "Streaming" {
		t.Fatalf("name = %q", Name(s))
	}
	s.Tilt = 0.5
	if Current(s) != -1 || Name(s) != "Custom" {
		t.Fatalf("a changed value should be Custom: %q", Name(s))
	}
	s = streaming.Settings
	s.DCRumble = true
	if Current(s) != 1 || Name(s) != "Streaming + repair" || Name(Settings{NotchTones: true}) != "Repair" {
		t.Fatalf("repair is outside the presets: %q", Name(s))
	}
	loud, _ := Find("loud")
	if got := loud.Apply(s); !got.DCRumble || got.Loudness != -10 || got.LowCut != 0 {
		t.Fatalf("a preset should keep Repair and replace the rest: %+v", got)
	}
	if _, ok := Find("Off"); ok {
		t.Fatal("Off is not a preset to find by name")
	}
	if _, ok := Find("clean up static"); ok {
		t.Fatal("there is no repair preset")
	}
	if got := Summary(streaming.Settings); got != "Streaming · −14 LUFS · −1 dBTP · low cut 30 Hz" {
		t.Fatalf("summary = %q", got)
	}
}

func TestCheckRejectsWhatFFmpegCannotDo(t *testing.T) {
	for _, s := range []Settings{{Loudness: -2}, {Ceiling: 1}, {NoiseBed: -1}, {FadeOut: -1}, {Width: -5}} {
		if s.Check() == nil {
			t.Errorf("%+v passed", s)
		}
	}
	if err := (Settings{Loudness: -30, Ceiling: -20, LowCut: 500, NoiseBed: 60, Width: 300}).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestExcerptSpans(t *testing.T) {
	s := Settings{TrimStart: 1, TrimEnd: 2, FadeIn: 3, FadeOut: 4}
	in := Info{SampleRate: 44100, Channels: 2, Duration: 180} // 177 s out
	cases := []struct {
		start float64
		want  span
		fade  string
	}{
		{0, span{From: 1, Skip: 0, Seconds: 30}, "afade=t=in:st=0:d=3"},
		{2, span{From: 1, Skip: 2, Seconds: 30}, "afade=t=in:st=0:d=3"},
		{60, span{From: 60, Skip: 1, Seconds: 30}, ""},
		{160, span{From: 160, Skip: 1, Seconds: 17}, "afade=t=out:st=14:d=4"},
		{175, span{From: 174, Skip: 2, Seconds: 2}, "afade=t=out:st=0:d=4"},
	}
	for _, c := range cases {
		sp := excerpt(s, in, c.start, 30)
		if sp != c.want {
			t.Errorf("start %v: span %+v, want %+v", c.start, sp, c.want)
		}
		chain := excerptChain(s, in, sp, nil)
		if c.fade != "" && !strings.Contains(chain, c.fade) || c.fade == "" && strings.Contains(chain, "afade") {
			t.Errorf("start %v: chain %s, want fade %q", c.start, chain, c.fade)
		}
		if strings.Contains(chain, "atrim=start=1:") || strings.Contains(chain, "atrim=end") {
			t.Errorf("start %v: an excerpt is seeked, not trimmed: %s", c.start, chain)
		}
	}
}

func TestRenderArgsKeepPathsWhole(t *testing.T) {
	in, out := "/tmp/a dir/-x,y;[z]:q.flac", "/tmp/out 'b'.flac"
	args := renderArgs(in, out, "anull", stereo44)
	if !slices.Contains(args, "file:"+in) || args[len(args)-1] != "file:"+out {
		t.Fatalf("paths not passed whole: %q", args)
	}
	af := args[slices.Index(args, "-af")+1]
	if strings.Contains(af, "/tmp") {
		t.Fatalf("a path reached the filter graph: %q", af)
	}
	for _, want := range []string{"-ar", "44100", "-ac", "2", "flac"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %q", want, args)
		}
	}
}

func TestParsers(t *testing.T) {
	stderr, err := os.ReadFile("testdata/analyze.stderr")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := parseLoudnorm(stderr)
	if err != nil {
		t.Fatal(err)
	}
	if ln != (Loudness{I: -11.05, TP: -8.58, LRA: 0, Thresh: -21.05}) {
		t.Fatalf("loudnorm = %+v", ln)
	}
	dc, floor := parseAstats(stderr)
	if dc != 0.029967 || floor != -9.671612 {
		t.Fatalf("dc %v floor %v", dc, floor)
	}
	if _, err := parseLoudnorm([]byte("nothing here")); err == nil {
		t.Fatal("no measurement should be an error")
	}
	silent, _ := parseLoudnorm([]byte(`{ "input_i" : "-inf", "input_tp" : "-inf", "input_lra" : "0.00", "input_thresh" : "-70.00" }`))
	if silent.I != -70 || silent.TP != -99 {
		t.Fatalf("silence = %+v", silent)
	}

	probe, err := os.ReadFile("testdata/probe.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := parseProbe(probe)
	if err != nil || info != (Info{SampleRate: 44100, Channels: 2, Duration: 5}) {
		t.Fatalf("probe = %+v, %v", info, err)
	}
	if _, err := parseProbe([]byte(`{"streams":[],"format":{}}`)); err == nil {
		t.Fatal("a file without audio should be an error")
	}

	progress, err := os.Open("testdata/progress.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	var got []float64
	readProgress(progress, func(s float64) { got = append(got, s) })
	if !slices.Equal(got, []float64{5}) {
		t.Fatalf("progress = %v", got)
	}
}
