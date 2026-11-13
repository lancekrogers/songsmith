package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/setup"
)

// ComfyServer is the ComfyUI server Settings shows, starts and stops.
type ComfyServer interface {
	Status(ctx context.Context) (server.Status, error)
	Start(ctx context.Context) (server.Status, error)
	// Stop stops only a server songsmith started; for any other it returns
	// server.ErrNotManaged.
	Stop(ctx context.Context) error
	LogPath() string
	ObjectInfo(ctx context.Context, class string) (comfy.NodeInfo, bool, error)
	// InstallDir is the folder a running server says it runs from, or "".
	InstallDir(ctx context.Context) (string, error)
}

// NewComfyServer adapts a server manager to ComfyServer.
func NewComfyServer(m *server.Manager) ComfyServer { return managedServer{m} }

type managedServer struct{ *server.Manager }

func (s managedServer) ObjectInfo(ctx context.Context, class string) (comfy.NodeInfo, bool, error) {
	return s.Client().ObjectInfo(ctx, class)
}

func (a app) comfyServer() ComfyServer {
	if a.d.Server != nil {
		return a.d.Server
	}
	cfg := a.cfg()
	return NewComfyServer(server.New(cfg.ComfyUI, cfg.StateDir))
}

type serverState struct {
	seq    int
	known  bool
	status server.Status
	// busy is "starting" or "stopping" while an action runs; confirming is
	// set from its result until the status check after it lands.
	busy       string
	confirming bool
	actionSeq  int
}

type serverStatusMsg struct {
	seq    int
	status server.Status
}

type serverActionMsg struct {
	seq    int
	verb   string
	status server.Status
	err    error
}

func (serverStatusMsg) settingsAsync() {}
func (serverActionMsg) settingsAsync() {}

func (a *app) loadServerStatus() tea.Cmd {
	sv := &a.settings.server
	sv.seq++
	seq, srv, ctx := sv.seq, a.comfyServer(), a.ctx
	return a.events.run(func(send func(tea.Msg)) {
		ctx, cancel := context.WithTimeout(ctx, settingsLoadTimeout)
		defer cancel()
		st, _ := srv.Status(ctx)
		send(serverStatusMsg{seq: seq, status: st})
	})
}

// serverAction starts or stops the server in the background; the outcome
// lands on the status line.
func (a *app) serverAction(verb string) tea.Cmd {
	sv := &a.settings.server
	sv.actionSeq++
	sv.busy = verb + "ing"
	if verb == "stop" {
		sv.busy = "stopping"
	}
	a.settingsNote(capitalize(sv.busy) + " the ComfyUI server…")
	seq, srv, ctx := sv.actionSeq, a.comfyServer(), a.ctx
	return a.events.run(func(send func(tea.Msg)) {
		msg := serverActionMsg{seq: seq, verb: verb}
		if verb == "start" {
			msg.status, msg.err = srv.Start(ctx)
		} else {
			msg.err = srv.Stop(ctx)
		}
		send(msg)
	})
}

func (a app) receiveServer(msg settingsAsync) (app, tea.Cmd) {
	sv := &a.settings.server
	switch msg := msg.(type) {
	case serverStatusMsg:
		if msg.seq == sv.seq {
			sv.known, sv.status, sv.confirming = true, msg.status, false
		}
		return a, nil
	case serverActionMsg:
		if msg.seq != sv.actionSeq {
			return a, nil
		}
		sv.busy, sv.confirming = "", true
		if msg.err == nil {
			sv.known, sv.status = true, msg.status
		}
		switch {
		case errors.Is(msg.err, server.ErrNotManaged):
			a.settingsError("This server was not started by songsmith, so songsmith leaves it running; stop it where it was started")
		case msg.err != nil:
			a.settingsError(fmt.Sprintf("Could not %s the server: %v", msg.verb, msg.err))
		case msg.verb == "start":
			a.settingsNote("Server running on " + cmp.Or(msg.status.Device, "an unknown device"))
		default:
			a.settingsNote("Server stopped")
		}
		status := a.loadServerStatus()
		next, health := a.startHealth()
		return next, tea.Batch(status, health)
	}
	return a, nil
}

func (a *app) refreshComfyFound() {
	_, err := os.Stat(filepath.Join(a.cfg().ComfyUI.Dir, "main.py"))
	a.settings.comfyFound = err == nil
}

// serverRows are the ComfyUI server songs render on: its state with start
// and stop, the install when it is missing, its folder and its log.
func (a app) serverRows() []settingRow {
	cfg, saved := a.cfg(), a.settings.saved
	rows := []settingRow{{name: "Engine", kind: rowNote, value: "Engine: ComfyUI · MiniMax Music 3 on this machine"}}
	server := a.serverRow()
	server.gap = true
	rows = append(rows, server)
	rows = append(rows, a.installComfyRows()...)
	dir := settingRow{key: "comfyui.dir", name: "Install folder", kind: rowText,
		desc: "Where ComfyUI is, or where setup installs it",
		help: "Where ComfyUI is, or where setup installs it. Have your own ComfyUI? Point this at its folder",
		run:  func(a *app) tea.Cmd { return a.editInstallFolder() }}
	restartValue(&dir, saved.ComfyUI.Dir, cfg.ComfyUI.Dir)
	log := settingRow{name: "Log", kind: rowInfo, value: a.comfyServer().LogPath(),
		help: "The output of a server songsmith started"}
	return append(rows, dir, log)
}

func (a app) serverRow() settingRow {
	cfg := a.cfg()
	sv := a.settings.server
	row := settingRow{name: "Server", kind: rowAction, applies: appliesNow,
		desc: "songsmith starts it on the first render and stops only one it started"}
	switch {
	case sv.busy != "":
		row.value = sv.busy + "…"
		row.help = "Wait for the server to finish " + sv.busy
	case !sv.known:
		row.value = "checking…"
	case sv.status.OtherPort != 0 && !sv.status.Running && !a.rendering():
		row.value = "songsmith's server still runs on port " + strconv.Itoa(sv.status.OtherPort)
		row.detail = "this session uses port " + strconv.Itoa(cfg.ComfyUI.Port)
		row.verb = "stop"
		row.help = "enter stops the server songsmith started on port " + strconv.Itoa(sv.status.OtherPort) + "; the next render starts one on port " + strconv.Itoa(cfg.ComfyUI.Port)
		row.run = func(a *app) tea.Cmd { return a.serverAction("stop") }
	case sv.status.Running && !sv.status.Managed:
		row.kind, row.applies = rowInfo, ""
		row.value, row.detail = "running", "started outside songsmith · "+cfg.ComfyUI.BaseURL()
		row.help = "songsmith leaves a server it did not start running; stop it where it was started"
	case sv.status.Running && a.rendering():
		row.kind, row.applies = rowInfo, ""
		row.value = "running"
		row.detail = "rendering — stop is unavailable until the song finishes or is cancelled"
		row.help = "Rendering — stop is unavailable until the song finishes or is cancelled"
	case sv.status.Running:
		row.value = "running"
		row.detail = "started by songsmith · " + cfg.ComfyUI.BaseURL()
		row.verb = "stop"
		row.help = "enter stops the server"
		row.run = func(a *app) tea.Cmd { return a.serverAction("stop") }
	default:
		row.value, row.detail = "stopped", cfg.ComfyUI.BaseURL()
		row.verb = "start"
		row.help = "enter starts ComfyUI from the install folder; it also starts by itself on the first render"
		row.run = func(a *app) tea.Cmd { return a.serverAction("start") }
	}
	if sv.confirming && sv.busy == "" {
		row.kind, row.verb, row.run = rowInfo, "", nil
		row.help = "Checking the server after that change…"
	}
	return row
}

// rendering reports whether a song is rendering, here or in another process.
func (a app) rendering() bool {
	if a.d.Queue != nil && a.d.Queue.Current() != "" {
		return true
	}
	_, _, ok := a.renderingSong()
	return ok
}

// managedServerRunning reports whether the last check found a server
// songsmith started running.
func (a app) managedServerRunning() bool {
	sv := a.settings.server
	return sv.known && sv.status.Running && sv.status.Managed
}

// keepsRunningNote is added to a saved port or state folder while a server
// songsmith started runs on the old one.
func (a app) keepsRunningNote() string {
	if !a.managedServerRunning() {
		return ""
	}
	return " · the running server keeps port " + strconv.Itoa(a.cfg().ComfyUI.Port) + " until you stop it here, in this session"
}

// stopFirstHelp tells to stop a server songsmith started before restarting
// with a changed port or state folder, while one runs.
func (a app) stopFirstHelp(row *settingRow, saved, running string) {
	if saved != running && a.managedServerRunning() {
		row.help += " · Stop the server in Settings → Server before restarting, or songsmith will not recognise it as its own"
	}
}

// commitFolder takes an absolute folder or one under ~.
func (a *app) commitFolder(key, name, v string) (tea.Cmd, error) {
	save, _, err := folderValue(name, v)
	if err != nil {
		return nil, err
	}
	note := ""
	if key == "state_dir" {
		note = a.keepsRunningNote()
	}
	if !a.saveSetting(key, save) {
		return nil, errSaveFailed
	}
	a.settingsNote(capitalize(appliesRestart) + " · " + name + ": " + save + note)
	return nil, nil
}

// errSaveFailed keeps the one-line editor open after a save that failed;
// the reason is already on the status line.
var errSaveFailed = errors.New("not saved")

// commitHost takes a host name or an IP address, without a port.
func (a *app) commitHost(v string) (tea.Cmd, error) {
	if net.ParseIP(v) == nil && !validHostname(v) {
		return nil, errors.New("Type a host name or IP address without a port, such as 127.0.0.1")
	}
	if !a.saved("comfyui.host", v, "Host: "+v, appliesRestart) {
		return nil, errSaveFailed
	}
	return nil, nil
}

func validHostname(v string) bool {
	if v == "" || len(v) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(v, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func (a *app) commitPort(v string) (tea.Cmd, error) {
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("Port must be a whole number from 1 to 65535")
	}
	note := a.keepsRunningNote()
	if !a.saveSetting("comfyui.port", port) {
		return nil, errSaveFailed
	}
	a.settingsNote(capitalize(appliesRestart) + " · Port: " + v + note)
	return nil, nil
}

// installComfyRows offer to install ComfyUI into the install folder while it
// is missing or unfinished there, with progress, the latest line of the
// install log and a Cancel row while it runs.
func (a app) installComfyRows() []settingRow {
	width := a.settingsContentWidth() - rowIndent
	t := comfyTarget(a.cfg())
	if j, ok := a.job(t.key()); ok {
		row := settingRow{name: "Install ComfyUI", kind: rowInfo, value: jobStatus(j), detail: jobDetail(j),
			help: "Installing ComfyUI; the full output goes to " + t.logPath}
		return append([]settingRow{row, a.cancelRow(j)}, a.failureRows(t, width)...)
	}
	act, planned := a.planAction(setup.ActionComfyUI)
	if planned && !act.Pending() || !planned && a.settings.comfyFound {
		return nil
	}
	row := settingRow{name: "Install ComfyUI", kind: rowInfo, value: "no ComfyUI in the install folder"}
	var below []settingRow
	switch {
	case planned && installable(act) && !a.canInstall(act):
		row.detail = a.withheldNote()
	case planned && installable(act):
		row.kind, row.verb, row.applies = rowAction, "install", appliesNow
		row.detail = installOffer(act)
		row.help = "Installs ComfyUI into the install folder, with its own Python environment; the output goes to " + t.logPath
		row.run = func(a *app) tea.Cmd { return a.installAction(setup.ActionComfyUI) }
	case planned:
		row.detail = "cannot install here; the reason is below"
		below = styledReportRows("Install ComfyUI · why", "Cannot install: "+act.Detail, width, &warningStyle)
	default:
		row.detail = "checking what the install needs…"
	}
	rows := append([]settingRow{row}, below...)
	return append(rows, a.failureRows(t, width)...)
}
