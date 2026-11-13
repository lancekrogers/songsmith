package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/setup"
)

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

const (
	requirementsGroup = "Requirements"
	readyGroup        = "Ready to render"
)

// setupRows are one line per requirement in plain words with its state, and
// the install and check actions. A requirement that is not met shows the
// check's full text under it; one that is met shows only its state.
func (a app) setupRows() []settingRow {
	width := a.settingsContentWidth() - rowIndent - 2
	var rows []settingRow
	if !a.health.done {
		rows = append(rows, settingRow{name: "Checks", kind: rowInfo, group: requirementsGroup,
			value: "checking ComfyUI, the music model files, Ollama and ffprobe…"})
	} else {
		if names := a.blockingNames(); len(names) > 0 {
			rows = append(rows, styledReportRows("Blocked", "▲ Rendering is blocked by "+joinAnd(names), width, &errorStyle)...)
		}
		reqs := a.requirementRows(width)
		if len(rows) == 0 {
			for i := range reqs {
				if reqs[i].group == requirementsGroup {
					reqs[i].group = readyGroup
				}
			}
		}
		rows = append(rows, reqs...)
	}
	actions := a.installAllRows(width)
	actions = append(actions, a.rerunRow())
	actions[0].gap = true
	return append(rows, actions...)
}

// requirementName is a check's name on the Setup section.
var requirementName = map[string]string{
	health.IDComfyUI:          "ComfyUI",
	health.IDServer:           "ComfyUI server",
	health.IDNodes:            "ComfyUI nodes",
	health.IDServerModelFiles: "ComfyUI model list",
	health.IDWriter:           "Writer model",
	health.IDInstructions:     "Writing instructions",
	health.IDFFprobe:          "ffprobe",
}

// requirementHelp says what each requirement is for, in a few words and in
// full.
var requirementHelp = map[string][2]string{
	health.IDComfyUI:          {"The ComfyUI install that renders songs", "Its folder is set in Settings → Server"},
	health.IDServer:           {"The server songs render on", "Start or stop it in Settings → Server"},
	health.IDNodes:            {"The MiniMax Music 3 nodes in ComfyUI", ""},
	health.IDServerModelFiles: {"The model files the running server can see", ""},
	health.IDWriter:           {"The Ollama model that drafts lyrics", "Choose it in Settings → Writer"},
	health.IDInstructions:     {"The writing instructions drafts use", "Edit them in Settings → Writer"},
	health.IDFFprobe:          {"Records each song's length; it comes with ffmpeg", ""},
}

// modelChecks are the three music model file checks, in check order.
var modelChecks = []string{health.IDDiffusionModel, health.IDTextEncoder, health.IDVAE}

// optionalChecks are shown only when they are not OK.
var optionalChecks = []string{health.IDNodes, health.IDServerModelFiles, health.IDInstructions}

func (a app) blockingNames() []string {
	var names []string
	for _, c := range a.health.checks {
		if c.Result != health.Fail || !c.Blocking {
			continue
		}
		name := c.Name
		if slices.Contains(modelChecks, c.ID) {
			name = "the music model files"
		} else if n, ok := requirementName[c.ID]; ok {
			name = n
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func (a app) requirementRows(width int) []settingRow {
	var rows []settingRow
	models := false
	for _, c := range a.health.checks {
		switch {
		case slices.Contains(modelChecks, c.ID):
			if !models {
				models = true
				rows = append(rows, a.musicFileRows(width)...)
			}
		case slices.Contains(optionalChecks, c.ID) && c.Result == health.OK:
		default:
			rows = append(rows, a.checkRows(c, width, false)...)
		}
	}
	return rows
}

func checkMark(r health.Result) string {
	switch r {
	case health.OK:
		return statusStyle.Render("✓")
	case health.Warn:
		return warningStyle.Render("!")
	}
	return errorStyle.Render("✕")
}

// checkState is a check's state in a few words, and a dim detail after it.
func (a app) checkState(c health.Check, act setup.Action, planned bool) (string, string) {
	cfg := a.cfg()
	ok := c.Result == health.OK
	switch c.ID {
	case health.IDComfyUI:
		switch {
		case ok:
			return "installed", shortPath(cfg.ComfyUI.Dir)
		case planned && act.Status == setup.StatusPartial:
			return "not finished", ""
		case c.Fix == "":
			return "cannot be used", ""
		}
		return "not installed", ""
	case health.IDServer:
		switch {
		case ok:
			return "running", strings.TrimPrefix(c.Detail, "running ")
		case c.Result == health.Warn:
			return "stopped", "starts on the first render"
		}
		return "running on the CPU", ""
	case health.IDNodes:
		return "missing nodes", ""
	case health.IDServerModelFiles:
		return "needs a restart", ""
	case health.IDWriter:
		switch {
		case ok:
			return cfg.Ollama.Model, "through Ollama"
		case c.Fix != "":
			return cfg.Ollama.Model + " not pulled", ""
		}
		return "Ollama not reachable", ""
	case health.IDInstructions:
		return "not usable", ""
	case health.IDFFprobe:
		if ok {
			return "found", ""
		}
		return "not found", ""
	}
	if ok {
		return "ready", ""
	}
	return "not ready", ""
}

// checkRows is one requirement: its state and who fixes it. When songsmith
// can fix it, enter runs the install; while that runs, the row shows its
// progress and a Cancel row follows it. Under a requirement that is not met
// go the check's full text, a reason the install cannot run here and the
// error of a failed run; a sub-row with an install offer leaves the check's
// text to its description.
func (a app) checkRows(c health.Check, width int, sub bool) []settingRow {
	act, hasAct := a.planAction(actionFor[c.ID])
	planned := hasAct
	name := cmp.Or(requirementName[c.ID], c.Name)
	h := requirementHelp[c.ID]
	desc, help := h[0], cmp.Or(h[1], h[0], c.Detail)
	if sub {
		name, desc, help = a.slotRole(c.ID), "One of the music model files", "Choose which in Settings → Music model"
	}
	value, detail := a.checkState(c, act, planned)
	if sub {
		value, detail = a.fileState(c, act, planned)
	}
	row := settingRow{name: name, kind: rowInfo, mark: checkMark(c.Result), value: value, detail: detail,
		group: requirementsGroup, indent: sub, desc: desc, help: help, anchor: "check " + c.ID}
	if c.Result == health.OK || c.Fix == "" {
		hasAct = false
	}
	var t installTarget
	if hasAct {
		t, hasAct = a.targetOf(act)
	}
	if j, ok := a.job(t.key()); hasAct && ok {
		row.mark = warningStyle.Render(spinnerFrames[a.ticks%len(spinnerFrames)])
		row.value, row.detail = jobStatus(j), jobDetail(j)
		row.help = fmt.Sprintf("Installing %s; it keeps going while you use the rest of songsmith", t.name)
		cancel := a.cancelRow(j)
		cancel.indent = sub
		return append([]settingRow{row, cancel}, a.failureRows(t, width)...)
	}
	notes := c.Result != health.OK
	var below []settingRow
	switch {
	case c.Result == health.OK:
	case hasAct && installable(act):
		row.kind, row.verb, row.applies = rowAction, "install", appliesNow
		row.detail = installOffer(act)
		row.help = capitalize(installOffer(act)) + " · " + cmp.Or(act.Detail, c.Detail)
		if sub {
			row.desc, notes = c.Detail, false
		}
		id := act.ID
		row.run = func(a *app) tea.Cmd { return a.installAction(id) }
	case hasAct && act.Pending():
		row.detail = "songsmith cannot install this here"
		below = styledReportRows(name+" · why", "Cannot install: "+act.Detail, width, &warningStyle)
	case c.Fix != "" && !a.health.planned:
		row.detail = "songsmith can install this"
		if a.d.Installer != nil {
			row.detail += "; checking what it needs…"
		}
	case c.Missing != "" && c.Fix == "" && c.ID != health.IDWriter:
		row.detail = "get it yourself"
	}
	rows := []settingRow{row}
	if notes && c.Detail != "" {
		for _, n := range noteRows(name+" · detail", c.Detail, width, nil) {
			n.indent = sub
			rows = append(rows, n)
		}
	}
	rows = append(rows, below...)
	if hasAct {
		rows = append(rows, a.failureRows(t, width)...)
	}
	return rows
}

// slotRole is the music model file a check is about, as Settings names it.
func (a app) slotRole(id string) string {
	for _, s := range setup.ModelSlots(a.cfg().ComfyUI) {
		if actionFor[id] == s.ID {
			return s.Role
		}
	}
	return id
}

// fileState is one music model file's state.
func (a app) fileState(c health.Check, act setup.Action, planned bool) (string, string) {
	if c.Result == health.OK {
		return "installed", ""
	}
	switch {
	case planned && act.Status == setup.StatusPartial:
		return fmt.Sprintf("%.0f%% downloaded", act.Percent), ""
	case c.Fix == "":
		return "missing", "not a file songsmith can download"
	}
	return "missing", ""
}

// musicFileRows sum up the three music model files on one line, and list
// them one by one only while one is not installed.
func (a app) musicFileRows(width int) []settingRow {
	cfg := a.cfg()
	var checks []health.Check
	worst, missing := health.OK, 0
	for _, id := range modelChecks {
		if c, ok := health.Find(a.health.checks, id); ok {
			checks = append(checks, c)
			worst = max(worst, c.Result)
			if c.Result != health.OK {
				missing++
			}
		}
	}
	var size int64
	for _, s := range setup.ModelSlots(cfg.ComfyUI) {
		if f, ok := setup.Lookup(s.Dir, s.Name); ok {
			size += f.Size
		}
	}
	row := settingRow{name: "Music model files", kind: rowInfo, mark: checkMark(worst), group: requirementsGroup,
		anchor: "check music files", desc: "The MiniMax Music 3 files ComfyUI loads",
		help: "Choose which in Settings → Music model"}
	switch {
	case missing == 0:
		row.value = fmt.Sprintf("%d files", len(checks))
		if size > 0 {
			row.value += " · " + setup.GB(size)
		}
		return []settingRow{row}
	case missing == len(checks):
		row.value = "missing"
	default:
		row.value = fmt.Sprintf("%d of %d missing", missing, len(checks))
	}
	rows := []settingRow{row}
	for _, c := range checks {
		rows = append(rows, a.checkRows(c, width, true)...)
	}
	return rows
}

// actionFor maps a setup check to the install action that fixes it.
var actionFor = map[string]setup.ActionID{
	health.IDComfyUI:        setup.ActionComfyUI,
	health.IDDiffusionModel: setup.ActionDiffusion,
	health.IDTextEncoder:    setup.ActionTextEncoder,
	health.IDVAE:            setup.ActionVAE,
	health.IDWriter:         setup.ActionWriter,
}

// planAction is id's action in the latest plan.
func (a app) planAction(id setup.ActionID) (setup.Action, bool) {
	if !a.health.planned {
		return setup.Action{}, false
	}
	i := slices.IndexFunc(a.health.plan.Actions, func(act setup.Action) bool { return act.ID == id })
	if i < 0 {
		return setup.Action{}, false
	}
	return a.health.plan.Actions[i], true
}

// installable reports whether songsmith can run act here and now.
func installable(act setup.Action) bool {
	return act.Pending() && act.Status != setup.StatusUnavailable && (act.Kind != setup.KindModel || act.File != nil)
}

// targetOf is what act installs with the running settings.
func (a app) targetOf(act setup.Action) (installTarget, bool) {
	cfg := a.cfg()
	switch act.Kind {
	case setup.KindComfyUI:
		return comfyTarget(cfg), true
	case setup.KindModel:
		if act.File == nil {
			return installTarget{}, false
		}
		return modelTarget(cfg, *act.File), true
	case setup.KindWriter:
		return writerTarget(cfg, act.Model), true
	}
	return installTarget{}, false
}

// installOffer says what enter on an install row does.
func installOffer(act setup.Action) string {
	switch act.Kind {
	case setup.KindComfyUI:
		if act.Status == setup.StatusPartial {
			return "enter finishes the install (" + act.Detail + ")"
		}
		return fmt.Sprintf("enter installs ComfyUI (about %s on disk)", setup.GB(act.DownloadBytes))
	case setup.KindWriter:
		return "enter pulls " + act.Model + " through Ollama"
	}
	if act.Status == setup.StatusPartial {
		return fmt.Sprintf("enter resumes the download (%.0f%% done, %s left)", act.Percent, setup.GB(act.DownloadBytes))
	}
	return "enter downloads " + setup.GB(act.DownloadBytes)
}

// failureRows show the full error of t's last failed run.
func (a app) failureRows(t installTarget, width int) []settingRow {
	msg, ok := a.jobs.failed[t.key()]
	if !ok {
		return nil
	}
	return styledReportRows(t.name+" · error", "Last try failed: "+msg, width, &errorStyle)
}

// cancelRow stops a running job. Its anchor is the job's own, so the cursor
// never confuses two Cancel rows.
func (a app) cancelRow(j *job) settingRow {
	key := j.target.key()
	row := settingRow{name: "Cancel", anchor: "cancel " + key, kind: rowAction, verb: "cancel", applies: appliesNow,
		value: "stop " + j.target.name, indent: true}
	if j.target.resumable() {
		row.detail = "what is downloaded is kept"
		row.help = "Stops downloading " + j.target.name + ". The partial file stays, and installing again resumes it"
	} else {
		row.help = "Stops installing " + j.target.name + ". Installing again carries on from the steps already done"
	}
	if j.stopping {
		row.kind, row.value, row.detail = rowInfo, "stopping…", ""
		return row
	}
	row.run = func(a *app) tea.Cmd {
		a.cancelJob(key)
		a.settingsNote("Stopping " + j.target.name + "…")
		return nil
	}
	return row
}

// installAction runs one plan action. A model file needs ComfyUI's folder
// first, so while ComfyUI is missing it installs that, then the file.
func (a *app) installAction(id setup.ActionID) tea.Cmd {
	act, ok := a.planAction(id)
	if !ok || !installable(act) {
		return nil
	}
	t, _ := a.targetOf(act)
	if act.Kind == setup.KindModel {
		if comfy, ok := a.planAction(setup.ActionComfyUI); ok && comfy.Pending() && !setup.InspectComfyUI(t.dir).HasCheckout {
			if !installable(comfy) {
				a.settingsError("ComfyUI has to be installed first, and it cannot be installed here: " + comfy.Detail)
				return nil
			}
			if a.jobs.seqn.active() {
				a.settingsNote("Install everything missing is running; it installs this too")
				return nil
			}
			if _, running := a.job(comfyTarget(a.cfg()).key()); running {
				a.settingsNote("ComfyUI is still installing; download " + t.name + " once it is done")
				return nil
			}
			a.settingsNote("Installing ComfyUI first, then " + t.name)
			return a.startSequence([]installTarget{comfyTarget(a.cfg()), t})
		}
	}
	a.settingsNote("Started " + t.name)
	return a.startJob(t, nil)
}

// missingTargets are the plan's installable actions in plan order.
func (a app) missingTargets() ([]installTarget, int64) {
	var out []installTarget
	var bytes int64
	for _, act := range a.health.plan.Actions {
		if !installable(act) {
			continue
		}
		if t, ok := a.targetOf(act); ok {
			out = append(out, t)
			bytes += act.DownloadBytes
		}
	}
	return out, bytes
}

// installAllRows is "Install everything missing" while anything installable
// is missing, and its progress and Cancel row while it runs.
func (a app) installAllRows(width int) []settingRow {
	s := a.jobs.seqn
	if s.active() {
		done := s.total - len(s.left)
		row := settingRow{name: "Install everything missing", kind: rowInfo,
			value: fmt.Sprintf("%d of %d", done, s.total), help: "Installs what is missing in order, one at a time"}
		if j, ok := a.job(s.current); ok {
			row.detail = j.target.name + " · " + jobStatus(j)
		}
		cancel := settingRow{name: "Cancel everything", indent: true, kind: rowAction, verb: "cancel", applies: appliesNow,
			value: "stop now; the rest does not start",
			help:  "Stops the running install and the ones after it. Downloads keep what they fetched",
			run: func(a *app) tea.Cmd {
				a.cancelJob(a.jobs.seqn.current)
				a.jobs.seqn.left = nil
				a.settingsNote("Stopping…")
				return nil
			}}
		return []settingRow{row, cancel}
	}
	targets, bytes := a.missingTargets()
	if len(targets) == 0 {
		return nil
	}
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.name
	}
	row := settingRow{name: "Install everything missing", kind: rowAction, verb: "install", applies: appliesNow,
		value: fmt.Sprintf("%d %s · about %s", len(targets), plural(len(targets), "item", "items"), setup.GB(bytes)),
		help:  "Installs, in this order: " + strings.Join(names, ", ") + ". Each one keeps going while you use the rest of songsmith",
		run: func(a *app) tea.Cmd {
			targets, _ := a.missingTargets()
			a.settingsNote("Installing everything missing…")
			return a.startSequence(targets)
		}}
	return []settingRow{row}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (a app) rerunRow() settingRow {
	row := settingRow{name: "Re-run checks", kind: rowAction, verb: "re-run", applies: appliesNow,
		help: "Checks ComfyUI, the music model files, the server, Ollama and ffprobe again"}
	if a.health.running {
		row.value = "checking… " + spinnerFrames[a.ticks%len(spinnerFrames)]
	}
	row.run = func(a *app) tea.Cmd {
		if a.health.running {
			a.settingsNote("The checks are already running")
			return nil
		}
		next, cmd := a.startHealth()
		*a = next
		a.settingsNote("Checking…")
		return cmd
	}
	return row
}
