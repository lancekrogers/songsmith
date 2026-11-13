package tui

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

const (
	anchorTakes    = "takes"
	anchorPosition = "preview position"
)

// knob is one mastering setting drawn as a slider. Its range is where +/-
// step; enter types any value ffmpeg can apply.
type knob struct {
	name, group, unit, desc string
	field                   func(*master.Settings) *float64
	lo, hi, step            float64
	// first is where the first step from off lands; neutral is the value
	// shown for a stored zero that is not "off", such as a width of 100%.
	first, neutral  float64
	signed, offable bool
	// seconds reads typed values as m:ss as well as seconds.
	seconds bool
}

var knobs = []knob{
	{name: "Remove noise bed", group: "Repair", unit: "dB", lo: 0, hi: 30, step: 1, first: 8, offable: true,
		desc:  "Lowers steady hiss a little. It also dulls the song, and it cannot fix a render that came out noisy.",
		field: func(s *master.Settings) *float64 { return &s.NoiseBed }},
	{name: "Loudness target", group: "Level", unit: "LUFS", lo: -24, hi: -8, step: 0.5, first: -14, signed: true, offable: true,
		desc:  "How loud the whole song plays; streaming services play songs at about −14 LUFS",
		field: func(s *master.Settings) *float64 { return &s.Loudness }},
	{name: "Peak ceiling", group: "Level", unit: "dBTP", lo: -3, hi: 0, step: 0.1, first: -1, signed: true, offable: true,
		desc:  "The highest the waveform may reach; −1 dBTP leaves room for lossy encoding",
		field: func(s *master.Settings) *float64 { return &s.Ceiling }},
	{name: "Low cut", group: "Tone", unit: "Hz", lo: 0, hi: 120, step: 5, first: 30, offable: true,
		desc:  "Removes bass below this frequency; around 30 Hz clears rumble without thinning the bass",
		field: func(s *master.Settings) *float64 { return &s.LowCut }},
	{name: "Tilt", group: "Tone", unit: "dB", lo: -3, hi: 3, step: 0.5, signed: true,
		desc:  "Tips the balance: below 0 is warmer, above 0 brighter",
		field: func(s *master.Settings) *float64 { return &s.Tilt }},
	{name: "De-harsh", group: "Tone", unit: "dB", lo: 0, hi: 6, step: 0.5, first: 2, offable: true,
		desc:  "Softens a harsh, shouty 3–6 kHz range by this many dB",
		field: func(s *master.Settings) *float64 { return &s.DeHarsh }},
	{name: "Stereo width", group: "Tone", unit: "%", lo: 50, hi: 150, step: 10, neutral: 100,
		desc:  "Below 100% narrows toward mono; above 100% widens the sides",
		field: func(s *master.Settings) *float64 { return &s.Width }},
	{name: "Trim start", group: "Edges", unit: "s", lo: 0, hi: 10, step: 0.5, first: 0.5, offable: true, seconds: true,
		desc:  "Cuts this much off the start",
		field: func(s *master.Settings) *float64 { return &s.TrimStart }},
	{name: "Trim end", group: "Edges", unit: "s", lo: 0, hi: 10, step: 0.5, first: 0.5, offable: true, seconds: true,
		desc:  "Cuts this much off the end, such as a last note cut off at the length cap",
		field: func(s *master.Settings) *float64 { return &s.TrimEnd }},
	{name: "Fade in", group: "Edges", unit: "s", lo: 0, hi: 10, step: 0.5, first: 0.5, offable: true, seconds: true,
		desc:  "Fades the start in over this long",
		field: func(s *master.Settings) *float64 { return &s.FadeIn }},
	{name: "Fade out", group: "Edges", unit: "s", lo: 0, hi: 10, step: 0.5, first: 4, offable: true, seconds: true,
		desc:  "Fades the end out over this long, so an abrupt ending ends gently",
		field: func(s *master.Settings) *float64 { return &s.FadeOut }},
}

// value is the knob's setting in s as shown, with neutral for zero.
func (k knob) value(s master.Settings) float64 {
	v := *k.field(&s)
	if v == 0 && k.neutral != 0 {
		return k.neutral
	}
	return v
}

func (k knob) format(v float64) string {
	if v == 0 && k.offable {
		return "off"
	}
	n := master.Num(v)
	if k.signed {
		n = master.Signed(v)
	}
	if k.unit == "%" {
		return n + "%"
	}
	return n + " " + k.unit
}

// set stores v, keeping a neutral value as zero.
func (k knob) set(s master.Settings, v float64) master.Settings {
	if v == k.neutral && k.neutral != 0 {
		v = 0
	}
	*k.field(&s) = v
	return s
}

func (k knob) nudge(a *app, delta int) tea.Cmd {
	s := a.master.settings
	cur := k.value(s)
	var next float64
	switch {
	case cur == 0 && k.offable && k.first != 0:
		next = k.first
	default:
		next = cur + float64(delta)*k.step
		if cur >= k.lo && cur <= k.hi {
			next = min(max(next, k.lo), k.hi)
		}
		next = math.Round(next*100) / 100
	}
	if next == cur {
		a.masterNote(fmt.Sprintf("%s is at the end of the slider, %s; enter types another value", k.name, k.format(cur)))
		return nil
	}
	return a.setMaster(k.set(s, next), k.name+": "+k.format(next))
}

func (k knob) typeValue(a *app) tea.Cmd {
	initial := ""
	if v := k.value(a.master.settings); v != 0 || !k.offable {
		initial = master.Num(v)
	}
	return a.beginMasterEdit(k.name, initial, func(a *app, typed string) (tea.Cmd, error) {
		v, err := k.parse(typed)
		if err != nil {
			return nil, err
		}
		next := k.set(a.master.settings, v)
		if err := next.Check(); err != nil {
			return nil, errors.New(capitalize(err.Error()))
		}
		return a.setMaster(next, k.name+": "+k.format(v)), nil
	})
}

// parse reads a typed value: a number, with or without its unit, a time as
// m:ss for the edges, or off.
func (k knob) parse(typed string) (float64, error) {
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(typed, "−", "-")))
	t = strings.TrimSpace(strings.TrimSuffix(t, strings.ToLower(k.unit)))
	if (t == "" || t == "off") && k.offable {
		return 0, nil
	}
	if k.seconds {
		if n, err := parseLength(t); err == nil && strings.Contains(t, ":") {
			return float64(n), nil
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		hint := "a number"
		if k.offable {
			hint += ", or off"
		}
		if k.seconds {
			hint = "seconds such as 2.5 or 0:03, or off"
		}
		return 0, fmt.Errorf("Type %s for %s", hint, strings.ToLower(k.name))
	}
	return v, nil
}

func (k knob) row(s master.Settings) settingRow {
	v := k.value(s)
	scale := master.Num(k.lo) + "–" + master.Num(k.hi)
	if k.signed {
		scale = master.Signed(k.lo) + " to " + master.Signed(k.hi)
	}
	at := v
	if v == 0 && k.offable {
		at = k.lo
	}
	return settingRow{name: k.name, kind: rowSlider, group: k.group, value: padRight(k.format(v), 9),
		lo: k.lo, hi: k.hi, at: at, scale: scale, desc: k.desc, help: k.desc,
		nudge: k.nudge, run: k.typeValue}
}

// presetRow chooses a whole chain; Custom when the values were set by hand.
func presetRow(s master.Settings) settingRow {
	names := make([]string, len(master.Presets))
	for i, p := range master.Presets {
		names[i] = p.Name
	}
	row := settingRow{name: "Preset", kind: rowChoice, gap: true, options: names, current: master.Current(s),
		help: "Choosing a preset replaces the values below except Repair, which no preset includes"}
	row.desc = "Custom: the values below, set by hand; choosing a preset replaces them"
	if row.current >= 0 {
		row.desc = presetDescs[names[row.current]]
	} else {
		row.value = "Custom"
	}
	row.set = func(a *app, i int) tea.Cmd {
		return a.setMaster(master.Presets[i].Apply(a.master.settings), "Preset: "+master.Presets[i].Name)
	}
	return row
}

var presetDescs = map[string]string{
	"Off":               "Off: the take as it is; set any value below to change it",
	"Streaming":         "Streaming: −14 LUFS, −1 dBTP, a 30 Hz low cut",
	"Loud":              "Loud: −10 LUFS, −1 dBTP",
	"Warm":              "Warm: tilt −1.5 dB, de-harsh 2 dB",
	"Fix abrupt ending": "Fix abrupt ending: a 4 s fade out",
}

func (a app) masterRows() []settingRow {
	m := a.master
	var rows []settingRow
	if !m.defaults {
		rows = append(rows, a.takesRow())
	}
	if m.unavailable != nil {
		why := "Mastering needs ffmpeg, which is not installed: " + m.unavailable.Error() +
			". Install it (macOS: brew install ffmpeg; Debian or Ubuntu: sudo apt install ffmpeg; Fedora: sudo dnf install ffmpeg), then open this screen again."
		return append(rows, noteRows("ffmpeg", why, a.masterWidth()-rowIndent-2, &warningStyle)...)
	}
	if !m.defaults {
		rows = append(rows, a.listenRows()...)
	}
	rows = append(rows, presetRow(m.settings),
		toggleRow("Remove DC and rumble", "Repair", m.settings.DCRumble,
			"Removes a DC offset and rumble under 20 Hz. It does not touch the hiss.", func(s *master.Settings) *bool { return &s.DCRumble }),
		toggleRow("Notch render tones", "Repair", m.settings.NotchTones,
			"Cuts three narrow tones at fixed pitches that some long renders carry. It does not touch the hiss.", func(s *master.Settings) *bool { return &s.NotchTones }))
	for _, k := range knobs {
		rows = append(rows, k.row(m.settings))
	}
	if !m.defaults {
		rows = append(rows, a.masterActionRows()...)
	}
	return append(rows, a.autoRow(m.defaults))
}

func toggleRow(name, group string, on bool, desc string, field func(*master.Settings) *bool) settingRow {
	row := settingRow{name: name, kind: rowToggle, group: group, options: []string{"OFF", "ON"}, desc: desc, help: desc}
	if on {
		row.current = 1
	}
	row.set = func(a *app, i int) tea.Cmd {
		s := a.master.settings
		*field(&s) = i == 1
		return a.setMaster(s, name+": "+row.options[i])
	}
	return row
}

// takesRow lists the song's takes, ✓ on the one the library plays.
func (a app) takesRow() settingRow {
	m := a.master
	song, _ := a.masterSong()
	takes := a.masterTakes()
	row := settingRow{name: "Takes", anchor: anchorTakes, kind: rowPick, current: -1,
		value: "working on " + m.take, count: plural(len(takes), "1 take", fmt.Sprintf("%d takes", len(takes))),
		desc: "enter works on a take; c makes it the one the library plays",
		help: "The ✓ is the take the library plays; masters are new takes and never replace one"}
	for i, t := range takes {
		row.options = append(row.options, t.File)
		extra := takeDetail(t)
		if t.File == m.take {
			extra += " · working on"
		}
		row.extras = append(row.extras, extra)
		if t.File == song.Audio {
			row.current = i
		}
	}
	if len(takes) == 1 {
		row.detail = row.extras[0]
	}
	row.set = func(a *app, i int) tea.Cmd {
		file := a.masterTakes()[i].File
		if file == a.master.take {
			a.masterNote("Already working on " + file)
			return nil
		}
		a.master.take, a.master.measured, a.master.afterScope = file, false, scopeNone
		a.masterNote("Working on " + file)
		return tea.Batch(a.startMeasure(), a.schedulePreview())
	}
	return row
}

// takeDetail is a take's kind, length and loudness, as far as known.
func takeDetail(t library.Take) string {
	parts := []string{t.Label()}
	if t.Duration > 0 {
		parts = append(parts, fmtDuration(t.Duration))
	}
	if t.Loudness != 0 {
		parts = append(parts, master.Signed(math.Round(t.Loudness*10)/10)+" LUFS")
	}
	return strings.Join(parts, " · ")
}

// listenRows are the preview position and what the excerpts are doing.
func (a app) listenRows() []settingRow {
	m := a.master
	length := m.settings.Length(m.before.Info)
	if !m.measured {
		song, _ := a.masterSong()
		length = song.Duration
	}
	pos := settingRow{name: "Preview from", anchor: anchorPosition, kind: rowSlider, group: "Listen",
		value: padRight(fmtDuration(m.position), 9), lo: 0, hi: max(length, 1), at: m.position, scale: "of " + fmtDuration(length),
		desc:  "←/→ move it 5 s, [ and ] 15 s from anywhere; space plays the master from here, o the original",
		help:  "Excerpts run 30 s from here, the master and the original cut at the same place",
		nudge: func(a *app, d int) tea.Cmd { return a.moveTo(a.master.position + float64(d)*positionStep) },
		run: func(a *app) tea.Cmd {
			return a.beginMasterEdit("Preview from", fmtDuration(a.master.position), func(a *app, v string) (tea.Cmd, error) {
				n, err := parseLength(v)
				if err != nil || n < 0 {
					return nil, errors.New("Type a time such as 1:30 or 90")
				}
				return a.moveTo(float64(n)), nil
			})
		}}
	state, style := "", &dimStyle
	switch p := m.preview; {
	case p.err != "":
		state, style = "the excerpt failed: "+p.err, &errorStyle
	case p.preparing:
		state, style = "preparing preview…", &warningStyle
	case a.masterPlaying() == excerptMaster:
		state, style = "▶ playing the master", &statusStyle
	case a.masterPlaying() == excerptOriginal:
		state, style = "▶ playing the original", &statusStyle
	case p.file != "":
		state = "ready · space plays the master, o the original"
	}
	rows := []settingRow{pos}
	for _, r := range noteRows("Preview · state", state, a.masterWidth()-rowIndent-4, style) {
		r.indent = true
		rows = append(rows, r)
	}
	return rows
}

var plainStyle = lipgloss.NewStyle()

// masterActionRows master the take, offer the new master as the current
// take, and save the chain as the default.
func (a app) masterActionRows() []settingRow {
	m := a.master
	var rows []settingRow
	if j, ok := a.mastering.running[m.songID]; ok {
		run := settingRow{name: "Master this take", anchor: "master", kind: rowInfo, group: "Make it",
			mark:  warningStyle.Render(spinnerFrames[a.ticks%len(spinnerFrames)]),
			value: fmt.Sprintf("mastering %s · %d%%", j.source, int(j.done*100)),
			help:  "It keeps going if you leave this screen; the library shows its progress"}
		bar := settingRow{name: "Master · progress", kind: rowNote, indent: true, style: &plainStyle,
			value: progressBar(j.done, max(min(a.masterWidth()-rowIndent-4, 48), 10))}
		cancel := settingRow{name: "Cancel", anchor: "cancel master", kind: rowAction, verb: "cancel", indent: true, group: "Make it",
			value: "stop mastering", help: "Stops mastering; nothing is saved",
			run: func(a *app) tea.Cmd {
				a.cancelMastering(a.master.songID)
				a.masterNote("Stopping…")
				return nil
			}}
		if j.stopping {
			cancel.kind, cancel.value, cancel.run = rowInfo, "stopping…", nil
		}
		rows = append(rows, run, bar, cancel)
	} else {
		rows = append(rows, settingRow{name: "Master this take", anchor: "master", kind: rowAction, verb: "master", group: "Make it",
			value: "from " + m.take,
			desc:  "Renders the whole take with these settings as a new take; nothing is replaced",
			help:  "The master is saved beside the take; the library keeps playing the current take until you choose the new one",
			run:   func(a *app) tea.Cmd { return a.startMastering() }})
	}
	if made := m.made; made != "" && made != a.currentTakeName() {
		rows = append(rows, settingRow{name: "Make it the current take", anchor: "make current", kind: rowAction, verb: "use it", group: "Make it",
			value: made, desc: "The library plays the current take",
			run: func(a *app) tea.Cmd { return a.makeCurrent(made) }})
	}
	saved := a.cfg().Master.Default
	save := settingRow{name: "Save as my default", anchor: "save default", kind: rowAction, verb: "save", group: "Make it",
		value: master.Name(m.settings),
		desc:  "New songs open here with this chain, and new renders are mastered with it when that is on",
		run: func(a *app) tea.Cmd {
			if err := a.saveConfig("master.default", a.master.settings); err != nil {
				a.masterError("Not saved: " + err.Error())
				return nil
			}
			a.masterNote("Saved as your default · " + master.Summary(a.master.settings))
			return nil
		}}
	if saved == m.settings {
		save.kind, save.detail, save.run = rowInfo, "this is your default", nil
	}
	return append(rows, save)
}

// autoRow is the master.auto toggle.
func (a app) autoRow(defaults bool) settingRow {
	group := "Make it"
	if defaults {
		group = "New renders"
	}
	row := settingRow{key: "master.auto", name: "Master new renders automatically", kind: rowToggle, group: group,
		options: []string{"OFF", "ON"}, applies: appliesNextRender,
		desc: "Masters each new render with your default and makes it current; the raw render stays as its own take",
		help: "Uses the saved default chain: " + master.Summary(a.cfg().Master.Default)}
	if a.cfg().Master.Auto {
		row.current = 1
	}
	row.set = func(a *app, i int) tea.Cmd {
		on := i == 1
		if err := a.saveConfig("master.auto", on); err != nil {
			a.masterError("Not saved: " + err.Error())
			return nil
		}
		note := "Master new renders automatically: " + row.options[i]
		if on && a.cfg().Master.Default.IsZero() {
			note += " · your default changes nothing yet; Save as my default first"
		}
		a.masterNote(note)
		return nil
	}
	return row
}

func progressBar(done float64, width int) string {
	filled := min(width, int(done*float64(width)))
	return warningStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", width-filled))
}
