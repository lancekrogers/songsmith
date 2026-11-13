package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

type samplersState struct {
	seq        int
	loading    bool
	loaded     bool
	samplers   []string
	schedulers []string
	err        error
}

type samplersLoadedMsg struct {
	seq                  int
	samplers, schedulers []string
	err                  error
}

func (samplersLoadedMsg) settingsAsync() {}

// loadSamplers reads the sampler and scheduler names the running ComfyUI
// server offers.
func (a *app) loadSamplers() tea.Cmd {
	s := &a.settings.samplers
	s.seq++
	s.loading = true
	seq, srv, ctx := s.seq, a.comfyServer(), a.ctx
	return a.events.run(func(send func(tea.Msg)) {
		ctx, cancel := context.WithTimeout(ctx, settingsLoadTimeout)
		defer cancel()
		info, ok, err := srv.ObjectInfo(ctx, "KSampler")
		if err == nil && !ok {
			err = errors.New("the server has no KSampler node")
		}
		msg := samplersLoadedMsg{seq: seq, err: err}
		if err == nil {
			msg.samplers, msg.schedulers = info.ComboOptions("sampler_name"), info.ComboOptions("scheduler")
		}
		send(msg)
	})
}

func (a *app) receiveSamplers(msg samplersLoadedMsg) {
	s := &a.settings.samplers
	if msg.seq != s.seq {
		return
	}
	s.loading, s.loaded = false, true
	s.samplers, s.schedulers, s.err = msg.samplers, msg.schedulers, msg.err
}

// defaultLengths are the lengths a new song can start with, in seconds.
var defaultLengths = []int{30, 60, 90, 120, 150, 180, 240, 300}

func minSec(s int) string { return fmt.Sprintf("%d:%02d", s/60, s%60) }

func (a app) renderRows() []settingRow {
	r := a.cfg().Render
	lengths := make([]string, len(defaultLengths))
	for i, s := range defaultLengths {
		lengths[i] = minSec(s)
	}
	rows := []settingRow{
		{key: "render.steps", name: "Steps", kind: rowSlider, value: fmt.Sprint(r.Steps), applies: appliesNextRender,
			help: "Diffusion steps, 10 to 60: more can add detail and take longer",
			nudge: func(a *app, d int) tea.Cmd {
				return a.nudgeInt("render.steps", "Steps", a.cfg().Render.Steps, 10, 60, 1, d)
			}},
		{key: "render.cfg_scale", name: "CFG scale", kind: rowSlider, value: fmt.Sprintf("%.1f", r.CFGScale), applies: appliesNextRender,
			help:  "How closely the render follows the style, 1.0 to 4.0",
			nudge: (*app).nudgeCFG},
		{key: "render.top_k", name: "Top-k", kind: rowSlider, value: fmt.Sprint(r.TopK), applies: appliesNextRender,
			help: "How many candidates the model samples from, 10 to 200",
			nudge: func(a *app, d int) tea.Cmd {
				return a.nudgeInt("render.top_k", "Top-k", a.cfg().Render.TopK, 10, 200, 5, d)
			}},
		{key: "render.default_seconds", name: "Default length", kind: rowChoice, value: minSec(r.DefaultSeconds),
			options: lengths, current: slices.Index(defaultLengths, r.DefaultSeconds), applies: appliesNewSong,
			help: "The length a new song starts with; each song can change it",
			set: func(a *app, i int) tea.Cmd {
				a.saved("render.default_seconds", defaultLengths[i], "Default length: "+minSec(defaultLengths[i]), appliesNewSong)
				return nil
			}},
	}
	s := a.settings.samplers
	if !s.loaded || s.err != nil {
		row := settingRow{name: "Sampler, scheduler", kind: rowInfo, value: r.Sampler + ", " + r.Scheduler,
			detail: "start the server in Settings → Music engine to list the choices",
			help:   "The choices come from the running ComfyUI server; start it in Settings → Music engine"}
		if !s.loaded {
			row.detail = "asking the ComfyUI server…"
		}
		return append(rows, row)
	}
	return append(rows,
		comboRow("Sampler", "render.sampler", s.samplers, r.Sampler),
		comboRow("Scheduler", "render.scheduler", s.schedulers, r.Scheduler),
	)
}

// comboRow is a list read from the server, ✓ on the configured value. A
// configured value the server does not offer is listed dim.
func comboRow(name, key string, options []string, current string) settingRow {
	row := settingRow{key: key, name: name, kind: rowPick, options: slices.Clone(options), muted: make([]bool, len(options)),
		current: slices.Index(options, current), applies: appliesNextRender,
		help: "Listed by the running ComfyUI server"}
	if row.current < 0 && current != "" {
		row.options = append(row.options, current+" · not on this server")
		row.muted = append(row.muted, true)
		row.current = len(options)
	}
	row.set = func(a *app, i int) tea.Cmd {
		switch {
		case i >= len(options):
			a.settingsNote(current + " is not offered by this ComfyUI server; renders with it will fail")
		case options[i] == current:
			a.settingsNote(name + " is already " + current)
		default:
			a.saved(key, options[i], name+": "+options[i], appliesNextRender)
		}
		return nil
	}
	return row
}

// nudgeInt steps an integer setting within [lo, hi], first snapping a value
// saved outside the range back into it.
func (a *app) nudgeInt(key, name string, cur, lo, hi, step, delta int) tea.Cmd {
	next := min(max(cur+delta*step, lo), hi)
	if next == cur {
		a.settingsNote(fmt.Sprintf("%s is at its limit, %d", name, cur))
		return nil
	}
	a.saved(key, next, fmt.Sprintf("%s: %d", name, next), appliesNextRender)
	return nil
}

// nudgeCFG steps the CFG scale in tenths between 1.0 and 4.0.
func (a *app) nudgeCFG(delta int) tea.Cmd {
	cur := int(math.Round(a.cfg().Render.CFGScale * 10))
	next := min(max(cur+delta, 10), 40)
	if next == cur {
		a.settingsNote(fmt.Sprintf("CFG scale is at its limit, %.1f", float64(cur)/10))
		return nil
	}
	v := float64(next) / 10
	a.saved("render.cfg_scale", v, fmt.Sprintf("CFG scale: %.1f", v), appliesNextRender)
	return nil
}
