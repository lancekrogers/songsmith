package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
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

// A musicEngine renders songs. The Music engine section lists every engine
// and shows the rows of the one in use, so another engine is a new entry
// with its own rows.
type musicEngine struct {
	id, label, help string
	rows            func(a app) []settingRow
}

var musicEngines = []musicEngine{
	{id: "comfyui", label: "ComfyUI · MiniMax Music 3 on a local ComfyUI server",
		help: "songsmith sends each song to a ComfyUI server it starts and stops for you", rows: app.comfyRows},
}

// currentEngine is the engine in use; ComfyUI is the only one so far.
func (a app) currentEngine() musicEngine { return musicEngines[0] }

func (a app) engineRows() []settingRow {
	cur := a.currentEngine()
	row := settingRow{name: "Engine", kind: rowPick, current: -1, applies: appliesNextRender,
		help: "What renders your songs"}
	for i, e := range musicEngines {
		row.options = append(row.options, e.label)
		row.details = append(row.details, e.help)
		if e.id == cur.id {
			row.current = i
		}
	}
	row.set = func(a *app, i int) tea.Cmd {
		a.settingsNote(musicEngines[i].label + " is the engine in use")
		return nil
	}
	return append([]settingRow{row}, cur.rows(a)...)
}

type serverState struct {
	seq    int
	known  bool
	status server.Status
	// busy is "starting" or "stopping" while an action runs.
	busy      string
	actionSeq int
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
			sv.known, sv.status = true, msg.status
		}
		return a, nil
	case serverActionMsg:
		if msg.seq != sv.actionSeq {
			return a, nil
		}
		sv.busy = ""
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

func (a app) comfyRows() []settingRow {
	cfg, saved := a.cfg(), a.settings.saved
	rows := []settingRow{a.serverRow()}
	if !a.settings.comfyFound {
		rows = append(rows, settingRow{name: "Install ComfyUI", kind: rowInfo,
			value: "no ComfyUI in the install folder",
			help:  "songsmith can install ComfyUI into the install folder; that action arrives with in-app setup (for now see docs/setup.md)"})
	}
	dir := settingRow{key: "comfyui.dir", name: "Install folder", kind: rowText,
		help: "Where ComfyUI is, or where setup installs it. Have your own ComfyUI? Point this at its folder",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("Install folder", a.settings.saved.ComfyUI.Dir, func(a *app, v string) (tea.Cmd, error) {
				return a.commitFolder("comfyui.dir", "Install folder", v)
			})
		}}
	restartValue(&dir, saved.ComfyUI.Dir, cfg.ComfyUI.Dir)
	host := settingRow{key: "comfyui.host", name: "Host", kind: rowText,
		help: "The address the server listens on and songsmith connects to",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("Host", a.settings.saved.ComfyUI.Host, (*app).commitHost)
		}}
	restartValue(&host, saved.ComfyUI.Host, cfg.ComfyUI.Host)
	port := settingRow{key: "comfyui.port", name: "Port", kind: rowText,
		help: "The port the server listens on",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("Port", strconv.Itoa(a.settings.saved.ComfyUI.Port), (*app).commitPort)
		}}
	restartValue(&port, strconv.Itoa(saved.ComfyUI.Port), strconv.Itoa(cfg.ComfyUI.Port))
	log := settingRow{name: "Log", kind: rowInfo, value: a.comfyServer().LogPath(),
		help: "The output of a server songsmith started"}
	return append(rows, dir, host, port, log)
}

func (a app) serverRow() settingRow {
	cfg := a.cfg()
	sv := a.settings.server
	row := settingRow{name: "Server", kind: rowAction, applies: appliesNow}
	switch {
	case sv.busy != "":
		row.value = sv.busy + "…"
		row.help = "Wait for the server to finish " + sv.busy
	case !sv.known:
		row.value = "checking…"
	case sv.status.Running:
		row.value = "running at " + cfg.ComfyUI.BaseURL()
		row.detail = "not started by songsmith"
		if sv.status.Managed {
			row.detail = "started by songsmith"
		}
		row.verb = "stop"
		row.help = "enter stops the server; songsmith only stops a server it started"
		row.run = func(a *app) tea.Cmd { return a.serverAction("stop") }
	default:
		row.value = "stopped · " + cfg.ComfyUI.BaseURL()
		row.verb = "start"
		row.help = "enter starts ComfyUI from the install folder; it also starts by itself on the first render"
		row.run = func(a *app) tea.Cmd { return a.serverAction("start") }
	}
	return row
}

func (a *app) commitFolder(key, name, v string) (tea.Cmd, error) {
	if v == "" {
		return nil, errors.New(name + " cannot be empty; type a folder")
	}
	a.saved(key, v, name+": "+v, appliesRestart)
	return nil, nil
}

func (a *app) commitHost(v string) (tea.Cmd, error) {
	if v == "" || strings.ContainsAny(v, " \t/") {
		return nil, errors.New("Type a host name or address, such as 127.0.0.1")
	}
	a.saved("comfyui.host", v, "Host: "+v, appliesRestart)
	return nil, nil
}

func (a *app) commitPort(v string) (tea.Cmd, error) {
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("Port must be a whole number from 1 to 65535")
	}
	a.saved("comfyui.port", port, "Port: "+v, appliesRestart)
	return nil, nil
}
