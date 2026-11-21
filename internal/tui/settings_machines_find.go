package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
)

// machineFind is Find on Tailscale: the computers found and what asking
// each one found so far, by index. Whether one is added already is worked
// out from the running config each time it is drawn.
type machineFind struct {
	seq     int
	looking bool
	err     error
	found   []machines.Candidate
	results map[int]machines.Found
	cancel  context.CancelFunc
}

type tailnetReadMsg struct {
	seq   int
	found []machines.Candidate
	err   error
}

type machineAskedMsg struct {
	seq, i int
	found  machines.Found
}

func (tailnetReadMsg) settingsAsync()  {}
func (machineAskedMsg) settingsAsync() {}

// foundNameMax is the longest computer name drawn in the name column; a
// longer one leads its value instead, so it is never cut.
const foundNameMax = 24

// findMachines reads the tailnet in the background, then asks each computer
// on it whether its ComfyUI answers, results arriving one by one. A run
// started before is stopped and its results dropped.
func (a *app) findMachines() tea.Cmd {
	ms := &a.settings.machines
	if f := ms.find; f != nil && f.cancel != nil {
		f.cancel()
	}
	ms.seq++
	seq := ms.seq
	ctx, cancel := context.WithCancel(a.ctx)
	ms.find = &machineFind{seq: seq, looking: true, cancel: cancel}
	a.settings.marks[sectionMachines] = lineAnchor{set: true, name: "find machines", option: -1}
	cfg := a.cfg()
	find := a.d.FindMachines
	if find == nil {
		find = machines.TailscaleFinder(cfg.ComfyUI.Port)
	}
	p := machines.Prober{Check: a.d.CheckMachine}
	return a.events.run(func(send func(tea.Msg)) {
		defer cancel()
		found, err := find(ctx)
		send(tailnetReadMsg{seq: seq, found: found, err: err})
		if err != nil {
			return
		}
		p.ProbeAll(ctx, cfg, cfg.ComfyUI, found, func(i int, f machines.Found) {
			send(machineAskedMsg{seq: seq, i: i, found: f})
		})
	})
}

func (a *app) receiveTailnet(msg tailnetReadMsg) {
	f := a.settings.machines.find
	if f == nil || f.seq != msg.seq {
		return
	}
	f.looking, f.found, f.err = false, msg.found, msg.err
	f.results = map[int]machines.Found{}
	if msg.err == nil && len(msg.found) > 0 {
		n := len(msg.found)
		a.settingsNote(fmt.Sprintf("%d %s on Tailscale · asking each whether its ComfyUI answers", n, plural(n, "computer", "computers")))
	}
	a.revealFound()
}

// revealFound scrolls so what Find on Tailscale found shows under it, down
// to the row after it, as far as Find itself stays on screen.
func (a *app) revealFound() {
	if a.screen != screenSettings || a.settings.section != sectionMachines {
		return
	}
	if row, _, ok := a.focusedSetting(); !ok || row.anchorName() != "find machines" {
		return
	}
	rows, lines := a.settingsLayout()
	disp, at := a.settingsDisplay(rows, lines)
	end := -1
	for i := at + 1; i < len(disp) && end < 0; i++ {
		if d := disp[i]; d.kind == displayRow && lines[d.line].focusable() {
			if name := rows[lines[d.line].row].anchorName(); !strings.HasPrefix(name, "find ") && !strings.HasPrefix(name, "found ") {
				end = i
			}
		}
	}
	if end < 0 {
		end = len(disp) - 1
	}
	s := &a.settings.rowCursor
	if visible := a.settingsFrame().height; end >= s.offset+visible {
		s.offset = min(end-visible+1, at)
	}
}

func (a *app) receiveAsked(msg machineAskedMsg) {
	f := a.settings.machines.find
	if f == nil || f.seq != msg.seq || msg.i < 0 || msg.i >= len(f.found) {
		return
	}
	f.results[msg.i] = msg.found
}

// findRows are Find on Tailscale and, once asked, every computer found, or
// why none could be listed.
func (a app) findRows(width int) []settingRow {
	f := a.settings.machines.find
	row := settingRow{name: "Find on Tailscale", kind: rowAction, verb: "find", anchor: "find machines", gap: true,
		value: "list the computers on your tailnet", wrap: true, applies: appliesNow,
		help: "Asks each one whether its ComfyUI answers; adds nothing", run: (*app).findMachines}
	if f == nil {
		return []settingRow{row}
	}
	if f.looking {
		row.mark = warningStyle.Render(spinnerFrames[a.ticks%len(spinnerFrames)])
		row.value, row.kind, row.verb, row.applies = "asking Tailscale…", rowInfo, "", ""
		row.desc = "Reading the computers Tailscale knows; the rest of Settings works meanwhile"
		return []settingRow{row}
	}
	row.value, row.verb = "look again", "look again"
	row.help = "Reads the tailnet and asks each computer again"
	rows := []settingRow{row}
	var why []settingRow
	switch {
	case f.err != nil:
		why = styledReportRows("find why", capitalize(strings.TrimSuffix(f.err.Error(), "."))+".", width, &warningStyle)
	case len(f.found) == 0:
		why = reportRows("find why", "Tailscale lists no other computers that could run ComfyUI; add one by its address below.", width)
	}
	for i := range why {
		why[i].indent = true
	}
	rows = append(rows, why...)
	cfg := a.cfg()
	for i, c := range f.found {
		rows = append(rows, a.foundRow(f, i, c, cfg))
	}
	return rows
}

// foundRow is one computer found and what asking it found; its detail is
// the line under it while the cursor is on it.
func (a app) foundRow(f *machineFind, i int, c machines.Candidate, cfg config.Config) settingRow {
	res, done := f.results[i]
	url := res.URL
	if url == "" && len(c.URLs) > 0 {
		url = c.URLs[0]
	}
	row := settingRow{name: c.Name, kind: rowAction, verb: "add", anchor: "found " + c.Address, wrap: true, applies: appliesNextSent}
	row.run = func(a *app) tea.Cmd { return a.beginAddFound(c, url) }
	info := func() { row.kind, row.verb, row.run, row.applies = rowInfo, "", nil, "" }
	st := res.Status
	switch name, added := c.AddedAs(cfg); {
	case added:
		row.mark, row.value = statusStyle.Render("✓"), "already added as "+name
		row.desc = "Added already as " + name + "; its status is under its name above"
		info()
	case !c.Online:
		row.mark, row.value = dimStyle.Render("·"), "offline on Tailscale"
		row.desc = "Offline on Tailscale: it can still be added, and songs go to it once it answers"
	case !done:
		row.mark = warningStyle.Render(spinnerFrames[a.ticks%len(spinnerFrames)])
		row.value = "asking…"
		row.desc = "Asking whether its ComfyUI answers at " + strings.Join(c.URLs, " or ")
		info()
	case !res.Asked:
		row.mark, row.value = dimStyle.Render("·"), "not asked"
		row.desc = "It was added already when songsmith looked; look again to ask it"
	case !res.Answered:
		row.mark, row.value = errorStyle.Render("✕"), "ComfyUI not answering"
		row.desc = c.NotAnswering()
	case st.AnsweredWithError:
		row.mark, row.value = errorStyle.Render("✕"), "answered with an error"
		row.desc, _ = statusText(st, false, url)
	case st.State == machines.Ready:
		row.mark, row.value = statusStyle.Render("✓"), strings.Join(append([]string{"ready"}, st.Hardware()...), " · ")
		row.desc = "ComfyUI answers at " + url + "; enter names it, then checks it again before saving it"
		if len(st.Unchecked) > 0 {
			row.mark = warningStyle.Render("!")
			row.value = "ready · model files not checked"
			row.desc, _ = statusText(st, false, url)
		}
	default:
		row.mark, row.value = warningStyle.Render("!"), string(st.State)
		row.desc, _ = statusText(st, false, url)
	}
	if lipgloss.Width(c.Name) > foundNameMax {
		row.name, row.value = "", c.Name+" · "+row.value
	}
	return row
}

// beginAddFound asks for a name for computer c, starting from its own, then
// checks it at url and shows what it found before it is saved.
func (a *app) beginAddFound(c machines.Candidate, url string) tea.Cmd {
	back := "found " + c.Address
	cmd := a.beginMachineName(c.Name, func(a *app, name string) (tea.Cmd, error) {
		cmd, err := a.commitMachineAddress(name, url)
		if err == nil && a.settings.machines.add != nil {
			a.settings.machines.add.back = back
		}
		return cmd, err
	})
	a.settings.edit.hint = "what songsmith calls it; it is checked before it is saved"
	return cmd
}
