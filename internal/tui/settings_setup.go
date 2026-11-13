package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/setup"
)

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

func (a app) setupRows() []settingRow {
	width := a.settingsContentWidth() - 4
	var rows []settingRow
	if a.health.done {
		if b, blocked := blockerSummary(a.health.checks); blocked {
			rows = append(rows, styledReportRows("Blocked", "▲ "+b, width, &errorStyle)...)
		} else {
			rows = append(rows, styledReportRows("Blocked", "✓ Nothing blocks rendering", width, &statusStyle)...)
		}
	}
	if in, out := health.Missing(a.health.checks); a.health.done && len(in)+len(out) > 0 {
		text := "Missing: " + strings.Join(append(slices.Clone(in), out...), ", ")
		if len(in) > 0 {
			text += " · songsmith can install " + joinAnd(in)
		}
		if len(out) > 0 {
			text += " · get " + joinAnd(out) + " yourself"
		}
		rows = append(rows, reportRows("Missing", text, width)...)
	}
	if !a.health.done {
		rows = append(rows, settingRow{name: "Checks", kind: rowInfo, value: "checking ComfyUI, the music model files, Ollama and ffprobe…"})
	}
	for _, c := range a.setupChecks() {
		rows = append(rows, a.requirementRows(c, width)...)
	}
	rows = append(rows, a.installAllRows(width)...)
	rows = append(rows, a.rerunRow())
	if a.health.done {
		rows = append(rows, reportRows("Report", "Check report, as songsmith doctor prints it:", width)...)
		for _, c := range a.setupChecks() {
			rows = append(rows, checkReportRows(c, width)...)
		}
	}
	return rows
}

// setupChecks is the latest run's checks, with the node check marked skipped
// when the server was not running to answer it.
func (a app) setupChecks() []health.Check {
	checks := a.health.checks
	if !a.health.done {
		return nil
	}
	if _, ok := health.Find(checks, health.IDNodes); ok {
		return checks
	}
	if _, ok := health.Find(checks, health.IDServer); !ok {
		return checks
	}
	out := make([]health.Check, 0, len(checks)+1)
	for _, c := range checks {
		out = append(out, c)
		if c.ID == health.IDServer {
			out = append(out, health.Check{ID: health.IDNodes, Name: "ComfyUI nodes", Result: skipped,
				Detail: "checked while the ComfyUI server runs; start it in Settings → Music engine"})
		}
	}
	return out
}

// skipped is a check that was not run this time.
const skipped health.Result = -1

func checkState(r health.Result) string {
	switch r {
	case health.OK:
		return statusStyle.Render("OK     ")
	case health.Warn:
		return warningStyle.Render("WARN   ")
	case health.Fail:
		return errorStyle.Render("FAIL   ")
	default:
		return dimStyle.Render("skipped")
	}
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

// requirementRows is one setup check: its state and who fixes it. When
// songsmith can fix it, enter runs the install; while that runs, the row
// shows its progress and a Cancel row follows it. A reason the install
// cannot run here, and the error of a failed run, are drawn in full below.
func (a app) requirementRows(c health.Check, width int) []settingRow {
	row := settingRow{name: c.Name, kind: rowInfo, value: checkState(c.Result), help: c.Detail}
	act, hasAct := a.planAction(actionFor[c.ID])
	if c.Result == health.OK || c.Result == skipped || c.Fix == "" {
		hasAct = false
	}
	var t installTarget
	if hasAct {
		t, hasAct = a.targetOf(act)
	}
	var below []settingRow
	if j, ok := a.job(t.key()); hasAct && ok {
		row.value = warningStyle.Render(padRight(jobStatus(j), 7))
		row.detail = jobDetail(j)
		row.help = fmt.Sprintf("Installing %s; it keeps going while you use the rest of songsmith", t.name)
		return append([]settingRow{row, a.cancelRow(j)}, a.failureRows(t, width)...)
	}
	switch {
	case c.Result == health.OK || c.Result == skipped:
		row.detail = c.Detail
	case hasAct && installable(act):
		row.kind, row.verb, row.applies = rowAction, "install", appliesNow
		row.detail = installOffer(act)
		row.help = installOffer(act) + ". " + act.Detail
		id := act.ID
		row.run = func(a *app) tea.Cmd { return a.installAction(id) }
	case hasAct && act.Pending():
		row.detail = "songsmith cannot install this here; the reason is below"
		below = styledReportRows(c.Name+" · why", "Cannot install: "+act.Detail, width, &warningStyle)
	case c.Fix != "" && !a.health.planned:
		row.detail = "songsmith can install this"
		if a.d.Installer != nil {
			row.detail += "; checking what it needs…"
		}
	case c.ID == health.IDWriter && c.Fix == "":
		row.detail = "get Ollama from ollama.com; lyrics can still be written by hand"
	case c.Missing != "" && c.Fix == "":
		row.detail = "songsmith cannot install this; get it yourself"
	default:
		row.detail = c.Detail
	}
	rows := append([]settingRow{row}, below...)
	if hasAct {
		rows = append(rows, a.failureRows(t, width)...)
	}
	return rows
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

// cancelRow stops a running job. Its name is the job's own, so the cursor
// never confuses two Cancel rows.
func (a app) cancelRow(j *job) settingRow {
	row := settingRow{name: "  Cancel " + j.target.name, kind: rowAction, verb: "cancel", applies: appliesNow,
		value: "stop this install"}
	if j.target.resumable() {
		row.value = "stop; what is downloaded is kept"
		row.help = "Stops the download. The partial file stays, and installing again resumes it"
	} else {
		row.help = "Stops the install. Installing again carries on from the steps already done"
	}
	if j.stopping {
		row.kind, row.value = rowInfo, "stopping…"
		return row
	}
	key := j.target.key()
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
		cancel := settingRow{name: "  Cancel everything", kind: rowAction, verb: "cancel", applies: appliesNow,
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

// checkReportRows is one check as songsmith doctor prints it, wrapped with
// a hanging indent.
func checkReportRows(c health.Check, width int) []settingRow {
	label := map[health.Result]string{health.OK: "OK  ", health.Warn: "WARN", health.Fail: "FAIL"}[c.Result]
	if c.Result == skipped {
		label = "SKIP"
	}
	text := fmt.Sprintf("%s %s: %s", label, c.Name, c.Detail)
	wrapped := strings.Split(lipgloss.NewStyle().Width(max(width-5, 10)).Render(text), "\n")
	rows := make([]settingRow, 0, len(wrapped))
	for i, l := range wrapped {
		if i > 0 {
			l = "     " + l
		}
		rows = append(rows, settingRow{name: "Report", kind: rowReport, value: strings.TrimRight(l, " ")})
	}
	return rows
}
