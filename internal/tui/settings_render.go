package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
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
	const quality, songs = "Quality", "New songs"
	rows := []settingRow{
		{key: "render.steps", name: "Steps", kind: rowSlider, group: quality, value: fmt.Sprint(r.Steps), applies: appliesNextRender,
			lo: 10, hi: 60, at: float64(r.Steps), scale: "10–60",
			help: "More steps render slower and can come out cleaner",
			nudge: func(a *app, d int) tea.Cmd {
				return a.nudgeInt("render.steps", "Steps", a.cfg().Render.Steps, 10, 60, 1, 1, d, appliesNextRender)
			},
			run: func(a *app) tea.Cmd {
				return a.typeInt("render.steps", "Steps", a.cfg().Render.Steps, 1, appliesNextRender)
			}},
		{key: "render.cfg_scale", name: "CFG scale", kind: rowSlider, group: quality, value: fmt.Sprintf("%.1f", r.CFGScale), applies: appliesNextRender,
			lo: 1, hi: 4, at: r.CFGScale, scale: "1.0–4.0",
			help:  "Higher follows the style more closely; lower leaves the model freer",
			nudge: (*app).nudgeCFG,
			run: func(a *app) tea.Cmd {
				return a.typeFloat("render.cfg_scale", "CFG scale", a.cfg().Render.CFGScale, 0, true, appliesNextRender)
			}},
		{key: "render.top_k", name: "Top-k", kind: rowSlider, group: quality, value: fmt.Sprint(r.TopK), applies: appliesNextRender,
			lo: 10, hi: 200, at: float64(r.TopK), scale: "10–200",
			help: "How many candidates each step picks from: higher varies more, lower plays safe",
			nudge: func(a *app, d int) tea.Cmd {
				return a.nudgeInt("render.top_k", "Top-k", a.cfg().Render.TopK, 10, 200, 1, 5, d, appliesNextRender)
			},
			run: func(a *app) tea.Cmd {
				return a.typeInt("render.top_k", "Top-k", a.cfg().Render.TopK, 1, appliesNextRender)
			}},
		{key: "render.default_seconds", name: "Default length", kind: rowChoice, group: songs, value: minSec(r.DefaultSeconds),
			options: lengths, current: slices.Index(defaultLengths, r.DefaultSeconds), applies: appliesNewSong,
			help: "The length a new song starts with; each song can change it. Enter types any length from 0:10 to 5:00",
			run:  (*app).typeLength,
			set: func(a *app, i int) tea.Cmd {
				a.saved("render.default_seconds", defaultLengths[i], "Default length: "+minSec(defaultLengths[i]), appliesNewSong)
				return nil
			}},
	}
	return append(rows, a.lowMemoryRows()...)
}

// advancedRows are the expert knobs: the sampler and scheduler the running
// ComfyUI server offers, and where Ollama and the server are.
func (a app) advancedRows() []settingRow {
	cfg, saved := a.cfg(), a.settings.saved
	var rows []settingRow
	s := a.settings.samplers
	if s.loaded && s.err == nil && len(s.samplers)+len(s.schedulers) > 0 {
		rows = append(rows,
			comboRow("Sampler", "render.sampler", s.samplers, cfg.Render.Sampler),
			comboRow("Scheduler", "render.scheduler", s.schedulers, cfg.Render.Scheduler))
	} else {
		row := settingRow{name: "Sampler", kind: rowInfo, value: cfg.Render.Sampler + ", scheduler " + cfg.Render.Scheduler, group: "Sampling",
			desc: "Which sampler and scheduler renders use",
			help: "The choices come from the running ComfyUI server; start it in Settings → Server"}
		note := "start the server in Settings → Server to list the choices"
		if !s.loaded {
			note = "asking the ComfyUI server…"
		}
		rows = append(rows, row, settingRow{name: "Sampler · note", kind: rowNote, value: note})
	}
	const conn = "Connections"
	url := settingRow{
		key: "ollama.url", name: "Ollama URL", kind: rowText, value: cfg.Ollama.URL, group: conn,
		applies: appliesNextDraft,
		help:    "Where the Ollama server listens",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("Ollama URL", a.cfg().Ollama.URL, (*app).commitOllamaURL)
		},
	}
	host := settingRow{key: "comfyui.host", name: "ComfyUI host", kind: rowText, group: conn,
		help: "The address the ComfyUI server listens on and songsmith connects to",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("ComfyUI host", a.settings.saved.ComfyUI.Host, (*app).commitHost)
		}}
	restartValue(&host, saved.ComfyUI.Host, cfg.ComfyUI.Host)
	port := settingRow{key: "comfyui.port", name: "ComfyUI port", kind: rowText, group: conn,
		help: "The port the ComfyUI server listens on",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("ComfyUI port", strconv.Itoa(a.settings.saved.ComfyUI.Port), (*app).commitPort)
		}}
	restartValue(&port, strconv.Itoa(saved.ComfyUI.Port), strconv.Itoa(cfg.ComfyUI.Port))
	a.stopFirstHelp(&port, strconv.Itoa(saved.ComfyUI.Port), strconv.Itoa(cfg.ComfyUI.Port))
	unload := settingRow{key: "ollama.unload_before_render", name: "Unload the writer before rendering", kind: rowToggle,
		group: "Memory", options: []string{"OFF", "ON"}, applies: appliesNextRender,
		desc: "frees the writer model's memory while a song renders",
		help: "When a song starts rendering, songsmith asks Ollama to unload the writer model, so the music model has its memory"}
	if cfg.Ollama.UnloadBeforeRender {
		unload.current = 1
	}
	unload.set = func(a *app, i int) tea.Cmd {
		on := i == 1
		a.saved("ollama.unload_before_render", on, "Unload the writer before rendering: "+unload.options[i], appliesNextRender)
		return nil
	}
	return append(rows, url, host, port, unload)
}

// lowMemoryRows choose whether renders free the language model before
// decoding. On auto a note under the choice says what auto chose for the
// running server.
func (a app) lowMemoryRows() []settingRow {
	mode := a.cfg().Render.LowMemory
	options := make([]string, len(config.LowMemoryModes))
	for i, m := range config.LowMemoryModes {
		options[i] = string(m)
	}
	row := settingRow{key: "render.low_memory", name: "Low-memory rendering", kind: rowChoice, group: "Memory",
		options: options, current: slices.Index(config.LowMemoryModes, cmp.Or(mode, config.LowMemoryAuto)), applies: appliesNextRender,
		desc: "Frees the language model before decoding. Slower by a model reload; needed on machines with about 32 GB.",
		help: "Renders in two prompts and frees the models between them; auto turns it on below " + backend.GB(backend.LowMemoryBelow)}
	row.set = func(a *app, i int) tea.Cmd {
		a.saved("render.low_memory", config.LowMemoryModes[i], "Low-memory rendering: "+options[i], appliesNextRender)
		return nil
	}
	if row.current != 0 {
		return []settingRow{row}
	}
	return []settingRow{row, {name: "Low-memory rendering · auto", kind: rowNote, indent: true, value: autoLowMemory(a.settings.server)}}
}

// autoLowMemory is what auto comes to on the running server.
func autoLowMemory(sv serverState) string {
	switch {
	case !sv.known:
		return "auto · asking the server…"
	case !sv.status.Running:
		return "auto · decided at each render"
	}
	_, why := backend.ResolveLowMemory(config.LowMemoryAuto, sv.status.Memory)
	return why
}

// comboRow is a list read from the server, ✓ on the configured value. A
// configured value the server does not offer is listed dim.
func comboRow(name, key string, options []string, current string) settingRow {
	row := settingRow{key: key, name: name, kind: rowPick, options: slices.Clone(options), muted: make([]bool, len(options)),
		current: slices.Index(options, current), applies: appliesNextRender, value: current,
		count: fmt.Sprintf("%d on this server", len(options)),
		help:  "Listed by the running ComfyUI server"}
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
