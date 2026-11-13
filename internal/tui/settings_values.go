package tui

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/master"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// A slider's range is where +/- step, not a limit: enter types any value the
// render or the writer accepts, and a value outside the range steps on from
// where it is.

// stepWithin moves cur by delta steps. Inside [lo, hi] it stops at the ends;
// outside, it moves freely but never below floor. moved is false when it
// could not move.
func stepWithin(cur, lo, hi, floor, step, delta int) (next int, moved bool) {
	next = cur + delta*step
	if cur >= lo && cur <= hi {
		next = min(max(next, lo), hi)
	}
	if next < floor {
		next = cur
	}
	return next, next != cur
}

func atEnd(name, value string) string {
	return fmt.Sprintf("%s is at the end of the slider, %s; enter types another value", name, value)
}

// nudgeInt steps an integer setting.
func (a *app) nudgeInt(key, name string, cur, lo, hi, floor, step, delta int, applies string) tea.Cmd {
	next, moved := stepWithin(cur, lo, hi, floor, step, delta)
	if !moved {
		a.settingsNote(atEnd(name, strconv.Itoa(cur)))
		return nil
	}
	a.saved(key, next, fmt.Sprintf("%s: %d", name, next), applies)
	return nil
}

// nudgeTenths steps a setting kept to one decimal place, in tenths.
func (a *app) nudgeTenths(key, name string, cur float64, lo, hi, floor, delta int, applies string) tea.Cmd {
	c := int(math.Round(cur * 10))
	next, moved := stepWithin(c, lo, hi, floor, 1, delta)
	if !moved {
		a.settingsNote(atEnd(name, fmt.Sprintf("%.1f", cur)))
		return nil
	}
	v := float64(next) / 10
	a.saved(key, v, fmt.Sprintf("%s: %.1f", name, v), applies)
	return nil
}

func (a *app) nudgeTemperature(delta int) tea.Cmd {
	return a.nudgeTenths("ollama.temperature", "Temperature", a.cfg().Ollama.Temperature, 0, 15, 0, delta, appliesNextDraft)
}

func (a *app) nudgeCFG(delta int) tea.Cmd {
	return a.nudgeTenths("render.cfg_scale", "CFG scale", a.cfg().Render.CFGScale, 10, 40, 1, delta, appliesNextRender)
}

// typeInt opens the editor on an integer setting that takes any whole
// number of at least floor.
func (a *app) typeInt(key, name string, cur, floor int, applies string) tea.Cmd {
	return a.beginEdit(name, strconv.Itoa(cur), func(a *app, v string) (tea.Cmd, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < floor {
			return nil, fmt.Errorf("%s must be a whole number of %d or more", name, floor)
		}
		if !a.saved(key, n, fmt.Sprintf("%s: %d", name, n), applies) {
			return nil, errSaveFailed
		}
		return nil, nil
	})
}

// typeFloat opens the editor on a decimal setting that takes any number of
// at least floor, or above it when above is set.
func (a *app) typeFloat(key, name string, cur, floor float64, above bool, applies string) tea.Cmd {
	return a.beginEdit(name, strconv.FormatFloat(cur, 'f', -1, 64), func(a *app, v string) (tea.Cmd, error) {
		f, err := strconv.ParseFloat(v, 64)
		bad := err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < floor || above && f == floor
		if bad {
			rule := "%s must be a number of %g or more"
			if above {
				rule = "%s must be a number above %g"
			}
			return nil, fmt.Errorf(rule, name, floor)
		}
		if !a.saved(key, f, fmt.Sprintf("%s: %g", name, f), applies) {
			return nil, errSaveFailed
		}
		return nil, nil
	})
}

// parseLength reads a song length typed as seconds ("90", "90s") or as
// minutes and seconds ("1:30").
func parseLength(v string) (int, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "s")
	if m, sec, ok := strings.Cut(v, ":"); ok {
		mi, err1 := strconv.Atoi(m)
		si, err2 := strconv.Atoi(sec)
		if err1 != nil || err2 != nil || mi < 0 || si < 0 || si > 59 {
			return 0, errors.New("bad length")
		}
		return mi*60 + si, nil
	}
	return strconv.Atoi(v)
}

// typeLength opens the editor on the default length, which takes any
// length a render supports.
func (a *app) typeLength() tea.Cmd {
	lo, hi := int(writer.MinSeconds), int(writer.MaxSeconds)
	return a.beginEdit("Default length", minSec(a.cfg().Render.DefaultSeconds), func(a *app, v string) (tea.Cmd, error) {
		n, err := parseLength(v)
		if err != nil || n < lo || n > hi {
			return nil, fmt.Errorf("Type a length from %s to %s, such as 90 or 1:30", minSec(lo), minSec(hi))
		}
		if !a.saved("render.default_seconds", n, "Default length: "+minSec(n), appliesNewSong) {
			return nil, errSaveFailed
		}
		return nil, nil
	})
}

// parseTokens reads a context size typed as tokens ("8192") or thousands
// ("8k", "128K").
func parseTokens(v string) (int, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	mult := 1
	if rest, ok := strings.CutSuffix(v, "k"); ok {
		v, mult = rest, 1024
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > math.MaxInt32/mult {
		return 0, errors.New("bad context")
	}
	return n * mult, nil
}

// commitContext saves the writer's context; blank leaves it to Ollama.
func (a *app) commitContext(v string) (tea.Cmd, error) {
	n := 0
	if v != "" {
		var err error
		if n, err = parseTokens(v); err != nil {
			return nil, errors.New("Type a whole number of tokens, such as 8192 or 32k, or leave it blank for Ollama's setting")
		}
	}
	what := "Context: " + contextValue(n)
	if !a.saved("ollama.context", n, what, appliesNextDraft) {
		return nil, errSaveFailed
	}
	return nil, nil
}

func contextValue(n int) string {
	if n <= 0 {
		return "Ollama's setting (not overridden)"
	}
	return fmt.Sprintf("%d tokens", n)
}

// masterSettingsRows are the auto-master toggle and the saved default chain,
// which the Master screen's rows edit.
func (a app) masterSettingsRows() []settingRow {
	cfg := a.cfg().Master
	const group = "Mastering"
	auto := settingRow{key: "master.auto", name: "Master new renders automatically", kind: rowToggle, group: group,
		options: []string{"OFF", "ON"}, applies: appliesNextRender,
		desc: "Masters each new render with the default chain and makes it current; the raw render stays as its own take",
		help: "Mastering needs ffmpeg; if it fails, the raw render stays current and the song says why"}
	if cfg.Auto {
		auto.current = 1
	}
	auto.set = func(a *app, i int) tea.Cmd {
		a.saved("master.auto", i == 1, "Master new renders automatically: "+auto.options[i], appliesNextRender)
		return nil
	}
	summary := settingRow{name: "Default chain", kind: rowInfo, group: group, value: master.Name(cfg.Default),
		desc: "What the Master screen opens with, and what new renders are mastered with"}
	details := noteRows("Default chain · details", master.Summary(cfg.Default), a.settingsContentWidth()-rowIndent-2, nil)
	for i := range details {
		details[i].indent = true
	}
	edit := settingRow{name: "Edit the default", kind: rowAction, verb: "edit", group: group, value: "the Master screen's rows on the default chain",
		applies: appliesNextRender,
		help:    "Opens the Master screen without a song; changes there save as the default",
		run:     func(*app) tea.Cmd { return func() tea.Msg { return openMasterMsg{} } }}
	return append(append([]settingRow{auto, summary}, details...), edit)
}
