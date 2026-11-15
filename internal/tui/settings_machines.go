package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/machines"
)

const appliesNextSent = "applies to the next song sent"

const (
	otherComputerSteps = "On the other computer, once: install ComfyUI and the model files (songsmith setup --install there is the easy way), " +
		"set comfyui.host to an address this computer can reach, and run songsmith server start."
	privateNetworkNote = "ComfyUI has no login: anyone who can reach a machine's address can use it. " +
		"Use an address on a private network, such as the computer's Tailscale name, never a public one."
)

// machinesState is what Settings → Machines knows about each machine: the
// latest check of each, by name, and the speeds measured so far. add is a
// machine being added; remove names one waiting for y.
type machinesState struct {
	seq    int
	checks map[string]machineCheck
	speeds map[string]machines.Speed
	add    *machineDraft
	remove string
	// question is what removing remove does to the songs that use it.
	question string
}

type machineCheck struct {
	seq      int
	url      string
	checking bool
	status   machines.Status
}

// machineDraft is a machine typed in Add a machine: checked, not saved yet.
type machineDraft struct {
	machine config.Machine
	check   machineCheck
}

type machineCheckedMsg struct {
	name, url string
	seq       int
	status    machines.Status
}

type machineSpeedsMsg struct {
	speeds map[string]machines.Speed
}

func (machineCheckedMsg) settingsAsync() {}
func (machineSpeedsMsg) settingsAsync()  {}

// draftKey keys the check of the machine being added.
const draftKey = "\x00add"

func machineKey(name string) string { return strings.ToLower(name) }

// checkMachine asks the machine at url, in the background, whether it can render.
func (a *app) checkMachine(key, url string, models config.ComfyUI) tea.Cmd {
	ms := &a.settings.machines
	ms.seq++
	seq := ms.seq
	c := machineCheck{seq: seq, url: url, checking: true}
	if key == draftKey {
		if ms.add == nil {
			return nil
		}
		ms.add.check = c
	} else {
		if ms.checks == nil {
			ms.checks = map[string]machineCheck{}
		}
		prev := ms.checks[key]
		if prev.checking && prev.url == url {
			return nil
		}
		c.status = prev.status
		ms.checks[key] = c
	}
	check, ctx := a.d.CheckMachine, a.ctx
	if check == nil {
		check = machines.Check
	}
	return a.events.run(func(send func(tea.Msg)) {
		send(machineCheckedMsg{name: key, url: url, seq: seq, status: check(ctx, url, models)})
	})
}

// checkAllMachines reads the config file again, so a machine added in
// another terminal shows, then checks this computer and every added machine
// and reads the speeds measured so far.
func (a *app) checkAllMachines() tea.Cmd {
	if a.d.Config.Reload() == nil {
		a.reloadSaved()
	}
	cfg := a.cfg()
	cmds := []tea.Cmd{a.checkMachine(machineKey(config.ThisComputer), cfg.ComfyUI.BaseURL(), cfg.ComfyUI)}
	for _, m := range cfg.Machines {
		cmds = append(cmds, a.checkMachine(machineKey(m.Name), m.URL, m.Models(cfg.ComfyUI)))
	}
	stateDir := cfg.StateDir
	cmds = append(cmds, a.events.run(func(send func(tea.Msg)) {
		send(machineSpeedsMsg{speeds: machines.NewSpeeds(stateDir).All()})
	}))
	return tea.Batch(cmds...)
}

// checkUnchecked checks each machine that has no check yet, such as one a
// save just found in the config file.
func (a *app) checkUnchecked() tea.Cmd {
	cfg := a.cfg()
	var cmds []tea.Cmd
	for _, m := range cfg.Machines {
		if _, ok := a.settings.machines.checks[machineKey(m.Name)]; !ok {
			cmds = append(cmds, a.checkMachine(machineKey(m.Name), m.URL, m.Models(cfg.ComfyUI)))
		}
	}
	return tea.Batch(cmds...)
}

func (a *app) receiveMachine(msg machineCheckedMsg) {
	ms := &a.settings.machines
	if msg.name == draftKey {
		if ms.add == nil || ms.add.check.seq != msg.seq {
			return
		}
		ms.add.check.checking, ms.add.check.status = false, msg.status
		name := ms.add.machine.Name
		if msg.status.State == machines.Ready {
			a.settingsNote(name + " is ready · Save adds it")
		} else {
			a.settingsNote(name + ": " + string(msg.status.State) + " · Save adds it anyway, Discard drops it")
		}
		// Waiting on the result, the cursor goes on to Save.
		if m := &a.settings.marks[sectionMachines]; m.name == "draft status" {
			*m = lineAnchor{set: true, name: "draft save", option: -1}
		}
		return
	}
	if c, ok := ms.checks[msg.name]; ok && c.seq == msg.seq && c.url == msg.url {
		c.checking, c.status = false, msg.status
		ms.checks[msg.name] = c
	}
}

// machineRows are this computer, each added machine and Add a machine.
func (a app) machineRows() []settingRow {
	cfg := a.cfg()
	width := a.settingsContentWidth() - rowIndent - 2
	var rows []settingRow
	local := a.machineBlock(config.ThisComputer, cfg.ComfyUI.BaseURL(), cfg.Local.Enabled, true, width)
	rows = append(rows, local...)
	for _, m := range cfg.Machines {
		rows = append(rows, a.machineBlock(m.Name, m.URL, m.Enabled, false, width)...)
	}
	return append(rows, a.addMachineRows(width)...)
}

// machineBlock is one machine: its status, address, memory and speed, the
// toggle that sends it new songs and, for an added machine, Remove.
func (a app) machineBlock(name, url string, enabled, local bool, width int) []settingRow {
	ms := a.settings.machines
	check, checked := ms.checks[machineKey(name)]
	group := name
	if !enabled {
		group += " · gets no new songs"
	}
	anchor := func(row string) string { return "machine " + machineKey(name) + " " + row }

	status := a.statusRow(check, checked, local)
	status.group, status.anchor = group, anchor("status")
	status.run = func(a *app) tea.Cmd {
		cfg := a.cfg()
		models := cfg.ComfyUI
		if m, ok := cfg.Machine(name); ok && !local {
			models = m.Models(cfg.ComfyUI)
		}
		return a.checkMachine(machineKey(name), url, models)
	}
	rows := []settingRow{status}
	rows = append(rows, statusReport(check.status, checked && !check.checking, local, url, width, anchor("why"))...)

	addr := settingRow{name: "Address", kind: rowInfo, value: url, wrap: true, group: group, anchor: anchor("address"),
		help: "The ComfyUI server songsmith sends this machine's songs to"}
	if local {
		addr.help = "This computer's ComfyUI server: comfyui.host and comfyui.port in Settings → Advanced"
	}
	rows = append(rows, addr, a.memoryRow(check, checked, group, anchor("memory")), a.speedRow(name, group, anchor("speed")))

	toggle := settingRow{name: "Renders new songs", kind: rowToggle, group: group, anchor: anchor("toggle"),
		options: []string{"OFF", "ON"}, applies: appliesNextSent,
		desc: "Turn it off to keep new songs off this machine; a song rendering there finishes there"}
	if enabled {
		toggle.current = 1
	}
	toggle.set = func(a *app, i int) tea.Cmd { return a.setMachineEnabled(name, i == 1) }
	rows = append(rows, toggle)
	if local {
		toggle := &rows[len(rows)-1]
		toggle.key = "local.enabled"
		toggle.desc = "Turn it off to render only on the machines added here"
		return rows
	}
	remove := settingRow{name: "Remove", kind: rowAction, verb: "remove", group: group, anchor: anchor("remove"),
		value: "stop rendering on " + name, wrap: true, applies: appliesNextSent,
		help: "Asks first. The config keeps no trace of it; add it again any time",
		run:  func(a *app) tea.Cmd { a.askRemoveMachine(name); return nil }}
	if ms.remove != name {
		return append(rows, remove)
	}
	remove.value = "y removes it, any other key keeps it"
	remove.kind, remove.run = rowInfo, nil
	ask := styledReportRows(anchor("ask"), ms.question, width, &warningStyle)
	for i := range ask {
		ask[i].indent, ask[i].group, ask[i].anchor = true, group, anchor("ask")
	}
	return append(append(rows, remove), ask...)
}

// statusRow says whether a machine can render; enter checks it again.
func (a app) statusRow(check machineCheck, checked, local bool) settingRow {
	row := settingRow{name: "Status", kind: rowAction, verb: "check again", wrap: true, applies: appliesNow,
		desc: "enter checks it again"}
	st := check.status
	switch {
	case !checked || check.checking && st.State == "":
		row.mark = warningStyle.Render(spinnerFrames[a.ticks%len(spinnerFrames)])
		row.value = "checking…"
		row.kind, row.verb = rowInfo, ""
		row.desc = "Asking the machine; the rest of Settings works meanwhile"
		return row
	case st.State == machines.Ready:
		row.mark, row.value = statusStyle.Render("✓"), "ready"
		if st.Version != "" {
			row.value += " · ComfyUI " + st.Version
		}
		if len(st.Unchecked) > 0 {
			row.mark = warningStyle.Render("!")
			row.value += " · model files not checked"
		}
	case st.AnsweredWithError:
		row.mark, row.value = errorStyle.Render("✕"), "answered with an error"
	case st.State == machines.Offline && local:
		row.mark, row.value = dimStyle.Render("·"), "stopped"
	case st.State == machines.Offline:
		row.mark, row.value = errorStyle.Render("✕"), "offline"
	default:
		row.mark, row.value = warningStyle.Render("!"), string(st.State)
	}
	if check.checking {
		row.mark = warningStyle.Render(spinnerFrames[a.ticks%len(spinnerFrames)])
		row.detail = "checking again…"
		row.kind, row.verb = rowInfo, ""
	}
	return row
}

// statusReport says, wrapped under the status, what is wrong and what to do.
func statusReport(st machines.Status, done, local bool, url string, width int, anchor string) []settingRow {
	if !done || st.State == "" || st.State == machines.Ready && len(st.Unchecked) == 0 {
		return nil
	}
	text, style := st.Detail, &warningStyle
	missing := strings.Join(st.Missing, ", ")
	switch {
	case st.State == machines.Ready:
		text = "Could not read which model files ComfyUI at " + url + " lists, so songsmith did not check that it sees " +
			strings.Join(st.Unchecked, ", ") + ". A render there fails at once if it does not."
	case st.AnsweredWithError:
		text = "ComfyUI at " + url + " " + st.Reason + "."
	case st.State == machines.Offline && local:
		text, style = "ComfyUI is not running at "+url+"; songsmith starts it on the first render here.", nil
	case st.State == machines.Offline:
		text = "Not answering at " + url + " (" + st.Reason + "). Check that the computer is awake, that ComfyUI runs there " +
			"(songsmith server start on that computer), and that its comfyui.host is an address this computer can reach."
	case st.State == machines.ModelsMissing && local:
		text = "This computer's ComfyUI cannot see " + missing + ". Setup says how to fix it."
	case st.State == machines.ModelsMissing:
		text = "Missing model files: " + missing + ". Install them on that computer (songsmith setup --install there), " +
			"or give this machine its own model file names in the config, then restart ComfyUI there."
	case st.State == machines.TooOld:
		text = "Missing nodes: " + missing + ". Update ComfyUI on that computer to a version with MiniMax Music 3 support."
	}
	rows := styledReportRows(anchor, text, width, style)
	for i := range rows {
		rows[i].indent, rows[i].anchor = true, anchor
	}
	return rows
}

func (a app) memoryRow(check machineCheck, checked bool, group, anchor string) settingRow {
	row := settingRow{name: "Memory", kind: rowInfo, group: group, anchor: anchor, wrap: true,
		help: "What the machine's ComfyUI reports; below " + backend.GB(backend.LowMemoryBelow) + " its renders free memory between steps"}
	st := check.status
	switch {
	case st.Memory > 0:
		row.value = backend.GB(st.Memory)
		if st.Device != "" {
			row.value += " · " + st.Device
		}
	case !checked || check.checking:
		row.value = "checking…"
	case st.State == machines.Offline:
		row.value = "unknown until it answers"
	default:
		row.value = "not reported by its ComfyUI"
	}
	return row
}

func (a app) speedRow(name, group, anchor string) settingRow {
	row := settingRow{name: "Speed", kind: rowInfo, group: group, anchor: anchor, value: "not measured yet", wrap: true,
		help: "Measured from the renders that finish there; the fastest free machine gets the next song, and one not measured yet is tried first"}
	if sp, ok := a.settings.machines.speeds[name]; ok && sp.PerAudioSecond() > 0 {
		row.value = fmt.Sprintf("%.1f s per second of audio", sp.PerAudioSecond())
		row.help = fmt.Sprintf("Render seconds per second of audio, from %d %s there; the fastest free machine gets the next song",
			sp.Renders, plural(sp.Renders, "render", "renders"))
	}
	return row
}

// addMachineRows are what the other computer needs and that its address
// belongs on a private network, then Add a machine, or the machine being
// added with what its check found.
func (a app) addMachineRows(width int) []settingRow {
	const group = "Add a machine"
	rows := styledReportRows("add machine steps", otherComputerSteps, width, nil)
	note := styledReportRows("add machine note", privateNetworkNote, width, &warningStyle)
	note[0].gap = true
	rows = append(rows, note...)
	for i := range rows {
		rows[i].group = group
	}
	if d := a.settings.machines.add; d != nil {
		return append(rows, a.draftRows(d, width)...)
	}
	return append(rows, settingRow{name: "Add", kind: rowAction, verb: "add", group: group, anchor: "add machine", gap: true,
		value: "type a name, then its address", wrap: true, applies: appliesNextSent,
		help: "Checks the machine and shows what it found before saving it",
		run:  (*app).beginAddMachine})
}

func (a app) draftRows(d *machineDraft, width int) []settingRow {
	group := "Add a machine · " + d.machine.Name
	done := !d.check.checking
	status := a.statusRow(d.check, true, false)
	status.group, status.anchor = group, "draft status"
	status.run = func(a *app) tea.Cmd { return a.checkDraft() }
	rows := []settingRow{status}
	rows = append(rows, statusReport(d.check.status, done, false, d.machine.URL, width, "draft why")...)
	addr := settingRow{name: "Address", kind: rowInfo, value: d.machine.URL, wrap: true, group: group, anchor: "draft address"}
	rows = append(rows, addr, a.memoryRow(d.check, true, group, "draft memory"))
	rows = append(rows, settingRow{name: "Discard", kind: rowAction, verb: "discard", group: group, anchor: "draft discard",
		value: "don't add it", applies: appliesNow,
		run: func(a *app) tea.Cmd {
			name := a.settings.machines.add.machine.Name
			a.settings.machines.add = nil
			a.settingsNote("Did not add " + name)
			a.settings.marks[sectionMachines] = lineAnchor{set: true, name: "add machine", option: -1}
			return nil
		}})
	if done {
		save := settingRow{name: "Save", kind: rowAction, verb: "save", group: group, anchor: "draft save",
			value: "add " + d.machine.Name, wrap: true, applies: appliesNextSent,
			help: "Saves it to the config; queued songs can go to it from the next song sent"}
		if d.check.status.State != machines.Ready {
			save.value += " anyway"
			save.help = "Saves it although the check found a problem; songs go to it once it passes the check"
		}
		save.run = (*app).saveDraftMachine
		rows = append(rows, save)
	}
	return rows
}

// beginAddMachine asks for the new machine's name, then its address.
func (a *app) beginAddMachine() tea.Cmd {
	cmd := a.beginEdit("Name", "", func(a *app, v string) (tea.Cmd, error) {
		if err := config.CheckMachineName(v); err != nil {
			return nil, a.editorProblem(capitalize(err.Error()))
		}
		if m, ok := a.cfg().Machine(v); ok {
			return nil, a.editorProblem("There is already a machine named " + m.Name + "; type another name")
		}
		return a.beginAddress(v), nil
	})
	e := &a.settings.edit
	e.input.CharLimit = 0
	e.hint = "what songsmith calls that computer, such as Studio Mac"
	return cmd
}

func (a *app) beginAddress(name string) tea.Cmd {
	cmd := a.beginEdit("Address of "+name, "", func(a *app, v string) (tea.Cmd, error) {
		return a.commitMachineAddress(name, v)
	})
	e := &a.settings.edit
	e.input.CharLimit = 0
	e.live = func(v string) string {
		u, err := config.NormalizeURL(v)
		if err != nil {
			return "its ComfyUI address, such as studio.tailnet.ts.net:8188"
		}
		return "checks " + u + " before saving"
	}
	e.hint = e.live("")
	return cmd
}

// commitMachineAddress checks what was typed, then the machine itself in
// the background; it is saved only once the user has seen what was found.
func (a *app) commitMachineAddress(name, v string) (tea.Cmd, error) {
	list, err := a.cfg().WithMachine(config.Machine{Name: name, URL: v, Enabled: true})
	if err != nil {
		return nil, a.editorProblem(capitalize(strings.TrimPrefix(err.Error(), fmt.Sprintf("machine %q: ", name))))
	}
	m := list[len(list)-1]
	a.settings.machines.add = &machineDraft{machine: m}
	a.settings.marks[sectionMachines] = lineAnchor{set: true, name: "draft status", option: -1}
	a.settingsNote("Checking " + m.Name + " at " + m.URL + "…")
	return a.checkDraft(), nil
}

// editorProblem shows why the typed value cannot be used under the editor,
// wrapped whole, and keeps the editor open.
func (a *app) editorProblem(msg string) error {
	e := &a.settings.edit
	e.note, e.noteFailed, e.confirm = msg, true, ""
	a.clearSettingsNote()
	return errSaveFailed
}

func (a *app) checkDraft() tea.Cmd {
	d := a.settings.machines.add
	if d == nil {
		return nil
	}
	return a.checkMachine(draftKey, d.machine.URL, d.machine.Models(a.cfg().ComfyUI))
}

// changeMachines applies one change to the machines in the config file as
// the file is now, so a machine added in another terminal is kept, then
// reloads the running settings from the file.
func (a *app) changeMachines(change func(path string) error) error {
	if err := a.d.Config.Update(change); err != nil {
		return err
	}
	a.reloadSaved()
	return nil
}

func (a *app) saveDraftMachine() tea.Cmd {
	ms := &a.settings.machines
	d := ms.add
	added := d.machine
	err := a.changeMachines(func(path string) error {
		var err error
		added, err = config.AddMachine(path, d.machine)
		return err
	})
	if err != nil {
		a.settingsError("Not saved: " + err.Error())
		return nil
	}
	if ms.checks == nil {
		ms.checks = map[string]machineCheck{}
	}
	ms.checks[machineKey(added.Name)] = d.check
	ms.add = nil
	a.wakeQueue()
	a.settingsNote(capitalize(appliesNextSent) + " · Added " + added.Name + " at " + added.URL)
	a.settings.marks[sectionMachines] = lineAnchor{set: true, name: "machine " + machineKey(added.Name) + " status", option: -1}
	return a.checkUnchecked()
}

func (a *app) setMachineEnabled(name string, on bool) tea.Cmd {
	err := a.changeMachines(func(path string) error { return config.SetMachineEnabled(path, name, on) })
	if err != nil {
		a.settingsError("Not saved: " + err.Error())
		return nil
	}
	a.settingsNote(capitalize(appliesNextSent) + " · " + name + " renders new songs: " + onOff(on))
	a.wakeQueue()
	return a.checkUnchecked()
}

func onOff(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

// askRemoveMachine waits for y before removing name, saying what happens
// to the songs that use it.
func (a *app) askRemoveMachine(name string) {
	if _, err := a.cfg().WithoutMachine(name); err != nil {
		a.settingsError("Not removed: " + err.Error())
		return
	}
	a.settings.machines.remove = name
	var pinned, here, held []string
	for _, s := range a.library.songs {
		switch {
		case s.Status == library.StatusRendering && strings.EqualFold(s.RenderingOn, name):
			here = append(here, s.Title)
		case s.Status == library.StatusQueued && s.JobID != "" && strings.EqualFold(s.RenderingOn, name):
			held = append(held, s.Title)
		case s.Status == library.StatusQueued && strings.EqualFold(s.Machine, name):
			pinned = append(pinned, s.Title)
		}
	}
	q := "Remove " + name + "?"
	if len(here) > 0 {
		q += " " + joinAnd(here) + " keeps rendering there until " + plural(len(here), "it finishes", "they finish") + "."
	}
	if len(held) > 0 {
		q += " " + joinAnd(held) + " " + plural(len(held), "is", "are") + " queued again for another machine, and " +
			plural(len(held), "its render", "their renders") + " there " + plural(len(held), "is", "are") + " left alone."
	}
	if len(pinned) > 0 {
		q += fmt.Sprintf(" %d %s set to render on it %s until you choose another machine for %s.",
			len(pinned), plural(len(pinned), "song", "songs"), plural(len(pinned), "waits", "wait"), plural(len(pinned), "it", "them"))
	}
	a.settings.machines.question = q
	a.settingsNote("y removes " + name + ", any other key keeps it")
}

// answerRemove removes the machine on y; any other key keeps it.
func (a app) answerRemove(key tea.KeyMsg) (app, tea.Cmd) {
	ms := &a.settings.machines
	name := ms.remove
	ms.remove = ""
	if key.String() != "y" {
		a.settingsNote("Kept " + name)
		return a, nil
	}
	err := a.changeMachines(func(path string) error {
		_, err := config.RemoveMachine(path, name)
		return err
	})
	if err != nil {
		a.settingsError("Not removed: " + err.Error())
		return a, nil
	}
	a.settingsNote(capitalize(appliesNextSent) + " · Removed " + name)
	delete(ms.checks, machineKey(name))
	a.wakeQueue()
	a.syncSettings()
	cmd := a.checkUnchecked()
	return a, cmd
}

func (a *app) wakeQueue() {
	if a.d.Queue != nil {
		a.d.Queue.Wake()
	}
}

// machineKeys are the footer while a removal waits for y.
func (a app) machineKeys() []string {
	return []string{"y remove " + a.settings.machines.remove, "any other key keeps it"}
}
