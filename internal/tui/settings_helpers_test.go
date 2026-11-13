package tui

import (
	"context"
	"errors"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/queue"
)

// fakeServer stands in for the ComfyUI server, so no test starts or stops one.
type fakeServer struct {
	mu       sync.Mutex
	status   server.Status
	startErr error
	stopErr  error
	starts   int
	stops    int
	// objectInfo answers ObjectInfo; nil means the server is unreachable.
	objectInfo func(ctx context.Context, class string) (comfy.NodeInfo, bool, error)
}

func (f *fakeServer) Status(context.Context) (server.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeServer) Start(context.Context) (server.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.startErr != nil {
		return server.Status{}, f.startErr
	}
	f.status = server.Status{Running: true, Managed: true, Device: "mps", GPUOnly: true}
	return f.status, nil
}

func (f *fakeServer) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.stopErr != nil {
		return f.stopErr
	}
	f.status = server.Status{}
	return nil
}

func (f *fakeServer) LogPath() string { return "/tmp/songsmith-test-state/comfyui.log" }

func (f *fakeServer) ObjectInfo(ctx context.Context, class string) (comfy.NodeInfo, bool, error) {
	if f.objectInfo == nil {
		return comfy.NodeInfo{}, false, errors.New("connection refused")
	}
	return f.objectInfo(ctx, class)
}

// fakeModels is an Ollama with names installed; nil names means unreachable.
func fakeModels(names ...string) func(context.Context, string) ([]ollama.ModelInfo, error) {
	infos := make([]ollama.ModelInfo, len(names))
	for i, n := range names {
		infos[i] = ollama.ModelInfo{Name: n}
	}
	return fakeModelInfos(names == nil, infos...)
}

// fakeModelInfos is an Ollama with infos installed, or an unreachable one.
func fakeModelInfos(unreachable bool, infos ...ollama.ModelInfo) func(context.Context, string) ([]ollama.ModelInfo, error) {
	return func(context.Context, string) ([]ollama.ModelInfo, error) {
		if unreachable {
			return nil, errors.New("connection refused")
		}
		return infos, nil
	}
}

const testModel = "songwriter:7b"

func newStoreQueue(t *testing.T) (*library.Store, *queue.Queue) {
	store := library.NewStore(t.TempDir())
	return store, queue.New(store, nopBackend{})
}

// newSettingsApp is the launcher after a check run, with fakes for
// everything Settings reads: no test reaches a real Ollama or ComfyUI.
func newSettingsApp(t *testing.T, checks []health.Check) (app, *fakeServer) {
	t.Helper()
	store := library.NewStore(t.TempDir())
	q := queue.New(store, nopBackend{})
	updates, stop := q.Subscribe()
	t.Cleanup(stop)
	deps := testDeps(t, store, q, fakeDrafter{})
	deps.Checks = func(context.Context, config.Config) []health.Check { return checks }
	srv := &fakeServer{}
	deps.Server = srv
	writeTestConfig(t, deps.Config)
	a := newTestApp(t, deps, updates)
	a.settings.models.list = fakeModels("alpha:1b", testModel, "zeta:3b")
	return finishHealth(t, a), srv
}

// writeTestConfig saves the running config's folders and port to its file,
// as a real config file would hold them, with a made-up writer model and an
// Ollama URL nothing listens on.
func writeTestConfig(t *testing.T, live *config.Live) {
	t.Helper()
	cfg := live.Get()
	for _, kv := range []savedKV{
		{"library_dir", cfg.LibraryDir}, {"state_dir", cfg.StateDir},
		{"comfyui.dir", cfg.ComfyUI.Dir}, {"comfyui.port", cfg.ComfyUI.Port},
		{"ollama.url", "http://127.0.0.1:1"}, {"ollama.model", testModel},
	} {
		if err := live.Set(kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
}

// openSettingsOn shows Settings on sec and lets its loads finish.
func openSettingsOn(t *testing.T, a app, sec settingsSection) app {
	t.Helper()
	a, cmd := send(t, a, switchScreenMsg(screenSettings))
	a = settle(t, a, cmd)
	if a.settings.section != sec {
		a = pressSettings(t, a, string(rune('1'+sec)))
	}
	return a
}

// settle runs cmd and feeds every message it and the background work it
// starts produce back into a, directly rather than through the bridge.
func settle(t *testing.T, a app, cmd tea.Cmd) app {
	t.Helper()
	for range 20 {
		var cmds []tea.Cmd
		for _, msg := range runCmds(cmd) {
			var c tea.Cmd
			a, c = send(t, a, msg)
			cmds = append(cmds, c)
		}
		for {
			msg, ok := a.events.next()
			if !ok {
				break
			}
			var c tea.Cmd
			a, c = send(t, a, msg)
			cmds = append(cmds, c)
		}
		cmd = tea.Batch(cmds...)
		if cmd == nil {
			return a
		}
	}
	t.Fatal("settle: commands kept coming")
	return a
}

// runCmds runs cmd and the commands of any batch, returning the messages
// for the app. Blink messages are left out.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmds(c)...)
		}
		return out
	}
	switch msg.(type) {
	case nil:
		return nil
	case settingsAsync, healthDoneMsg, switchScreenMsg:
		return []tea.Msg{msg}
	}
	return nil
}

func settingsKey(name string) tea.KeyMsg {
	switch name {
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return key(name)
}

// pressSettings presses each key and settles what it starts.
func pressSettings(t *testing.T, a app, keys ...string) app {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		a, cmd = send(t, a, settingsKey(k))
		a = settle(t, a, cmd)
	}
	return a
}

func typeIntoSettings(t *testing.T, a app, text string) app {
	t.Helper()
	for _, r := range text {
		a, _ = send(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return a
}

// clearEditor deletes what the one-line editor holds.
func clearEditor(t *testing.T, a app) app {
	t.Helper()
	for range len(a.settings.edit.input.Value()) {
		a, _ = send(t, a, settingsKey("backspace"))
	}
	return a
}

type savedKV struct {
	key   string
	value any
}

// recordSaves records each save and still writes it to the config file.
func recordSaves(a *app) *[]savedKV {
	var got []savedKV
	live := a.d.Config
	a.settings.save = func(k string, v any) error {
		got = append(got, savedKV{k, v})
		return live.Set(k, v)
	}
	return &got
}

// lineOf is the line of the row named name, on option (-1 for the row
// itself); -1 when there is none.
func lineOf(a app, name string, option int) int {
	rows, lines := a.settingsLayout()
	for i, l := range lines {
		if l.focusable() && rows[l.row].name == name && l.option == option {
			return i
		}
	}
	return -1
}

// focus moves the cursor onto the named line, clearing the message as the
// arrow keys do.
func focus(t *testing.T, a app, name string, option int) app {
	t.Helper()
	a.clearSettingsNote()
	i := lineOf(a, name, option)
	if i < 0 {
		t.Fatalf("no line %q option %d in %s:\n%s", name, option, sectionNames[a.settings.section], plain(a))
	}
	rows, lines := a.settingsLayout()
	a.placeSettingsCursor(rows, lines, i)
	return a
}

func focusedName(a app) (string, int) {
	row, line, ok := a.focusedSetting()
	if !ok {
		return "", -2
	}
	return row.name, line.option
}
