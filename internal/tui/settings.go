package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
)

type settingsSection int

const (
	sectionSetup settingsSection = iota
	sectionWriter
	sectionMusicModel
	sectionRender
	sectionEngine
	sectionLibrary
	settingsSectionCount
)

var sectionNames = [settingsSectionCount]string{
	sectionSetup:      "Setup",
	sectionWriter:     "Writer",
	sectionMusicModel: "Music model",
	sectionRender:     "Render",
	sectionEngine:     "Music engine",
	sectionLibrary:    "Library",
}

// settingsModel is the Settings screen's state. It lives on the app for the
// whole run, so the screen reopens on the section last used and results that
// arrive after leaving it still land.
type settingsModel struct {
	section settingsSection
	cursor  int
	offset  int
	// marks remembers the line each section was on; visited says which
	// sections have one rather than their home line.
	marks   [settingsSectionCount]lineAnchor
	visited [settingsSectionCount]bool

	// message is the latest outcome; failed draws it as an error.
	message string
	failed  bool
	edit    settingsEditor

	// saved is the config file as last read. Rows that apply after a restart
	// show it, since the running settings keep the values from the start.
	saved config.Config
	// save writes one key; nil means the running config's Set.
	save func(key string, value any) error

	models   modelsState
	samplers samplersState
	server   serverState
	// variants is the on-disk state of the music model files, read when
	// the Music model section opens.
	variants map[string][]ModelVariant
	// comfyFound is whether the running ComfyUI folder has a ComfyUI in it.
	comfyFound bool
	// recheck is set when a saved change affects the setup checks; they
	// run again on leaving Settings or opening Setup.
	recheck bool
}

// checkedKeys are the settings the setup checks read.
var checkedKeys = []string{"ollama.model", "ollama.url", "comfyui.diffusion_model", "comfyui.text_encoder", "comfyui.vae"}

// settingsAsync marks results of background work started from Settings. The
// app hands them to Settings whichever screen is showing.
type settingsAsync interface{ settingsAsync() }

// openSettings shows Settings on sec, or on the section last used when
// lastUsed is set.
func (a app) openSettings(sec settingsSection, lastUsed bool) (app, tea.Cmd) {
	a.screen = screenSettings
	a.reloadSaved()
	if lastUsed {
		sec = a.settings.section
	}
	a.settings.edit = settingsEditor{}
	cmd := a.gotoSection(sec)
	return a, cmd
}

// reloadSaved reads the config file for the restart-only rows.
func (a *app) reloadSaved() {
	cfg, err := config.Load(a.d.Config.Path())
	if err != nil {
		cfg = a.cfg()
	}
	a.settings.saved = cfg
}

// enterSection refreshes what sec shows from outside the config.
func (a *app) enterSection(sec settingsSection) tea.Cmd {
	switch sec {
	case sectionSetup:
		return a.recheckIfChanged()
	case sectionWriter:
		return a.loadModels()
	case sectionMusicModel:
		a.refreshVariants()
	case sectionRender:
		return a.loadSamplers()
	case sectionEngine:
		a.refreshComfyFound()
		return a.loadServerStatus()
	}
	return nil
}

// One key, one verb: ←/→ (h/l, tab, digits) switch sections, ↑/↓ move
// between lines, enter acts on the line, space flips, +/- adjust, esc leaves.
func (a app) updateSettings(msg tea.Msg) (app, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	if a.settings.edit.active {
		return a.updateSettingsEditor(key)
	}
	a.syncSettings()
	var cmd tea.Cmd
	switch k := key.String(); k {
	case "right", "l", "tab":
		cmd = a.switchSection(1)
	case "left", "h", "shift+tab":
		cmd = a.switchSection(-1)
	case "up", "k":
		a.clearSettingsNote()
		a.moveSettingsCursor(-1)
	case "down", "j":
		a.clearSettingsNote()
		a.moveSettingsCursor(1)
	case "pgup":
		a.clearSettingsNote()
		a.moveSettingsCursor(-a.settingsListRows())
	case "pgdown":
		a.clearSettingsNote()
		a.moveSettingsCursor(a.settingsListRows())
	case "enter", " ", "+", "=", "-", "_":
		row, line, ok := a.focusedSetting()
		if !ok {
			break
		}
		switch k {
		case "enter":
			cmd = a.activateSetting(row, line)
		case " ":
			cmd = a.toggleSetting(row, line)
		case "+", "=":
			cmd = a.adjustSetting(row, line, 1)
		default:
			cmd = a.adjustSetting(row, line, -1)
		}
	case "esc":
		recheck := a.recheckIfChanged()
		return a, tea.Batch(recheck, func() tea.Msg { return switchScreenMsg(screenLauncher) })
	default:
		if len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < int(settingsSectionCount) {
			cmd = a.gotoSection(settingsSection(k[0] - '1'))
		}
	}
	a.syncSettings()
	return a, cmd
}

func (a *app) settingsNote(msg string)  { a.settings.message, a.settings.failed = msg, false }
func (a *app) settingsError(msg string) { a.settings.message, a.settings.failed = msg, true }
func (a *app) clearSettingsNote()       { a.settings.message, a.settings.failed = "", false }

// saveSetting writes key to the config file first; the running settings
// change only when that worked. A failure is shown and nothing changes.
func (a *app) saveSetting(key string, value any) bool {
	save := a.settings.save
	if save == nil {
		save = a.d.Config.Set
	}
	if err := save(key, value); err != nil {
		a.settingsError("Not saved: " + err.Error())
		return false
	}
	a.reloadSaved()
	if slices.Contains(checkedKeys, key) {
		a.settings.recheck = true
	}
	return true
}

// recheckIfChanged re-runs the setup checks when a saved change affects them.
func (a *app) recheckIfChanged() tea.Cmd {
	if !a.settings.recheck {
		return nil
	}
	a.settings.recheck = false
	next, cmd := a.startHealth()
	*a = next
	return cmd
}

// saved writes key and, when that worked, says when the change applies and
// what changed, in that order so a long value cannot push the first off.
func (a *app) saved(key string, value any, what, applies string) bool {
	if !a.saveSetting(key, value) {
		return false
	}
	a.settingsNote(capitalize(applies) + " · " + what)
	return true
}

// restartValue is a restart-only row's value: the saved one, with a note
// while it differs from the one this process runs with.
func restartValue(row *settingRow, saved, running string) {
	row.value = saved
	row.applies = appliesRestart
	if saved != running {
		row.detail = "applies after restart · running " + running
		row.help += " · running now: " + running
	}
}

// settingsEditor is the one-line value editor shown above the section.
type settingsEditor struct {
	active bool
	label  string
	input  textinput.Model
	// commit applies the typed value. A non-nil error is a problem with the
	// value: it is shown and the editor stays open for a fix.
	commit func(a *app, value string) (tea.Cmd, error)
}

func (a *app) beginEdit(label, initial string, commit func(a *app, value string) (tea.Cmd, error)) tea.Cmd {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 1024
	in.Width = max(a.settingsContentWidth()-len(label)-6, 10)
	in.SetValue(initial)
	in.CursorEnd()
	in.Focus()
	a.settings.edit = settingsEditor{active: true, label: label, input: in, commit: commit}
	a.clearSettingsNote()
	return textinput.Blink
}

// updateSettingsEditor owns every key while the editor is open, so typing
// "l" or a digit edits the value instead of switching sections.
func (a app) updateSettingsEditor(key tea.KeyMsg) (app, tea.Cmd) {
	switch key.String() {
	case "esc":
		a.settings.edit = settingsEditor{}
		a.settingsNote("Cancelled; nothing saved")
		return a, nil
	case "enter":
		value := strings.TrimSpace(a.settings.edit.input.Value())
		cmd, err := a.settings.edit.commit(&a, value)
		if err != nil {
			a.settingsError(err.Error())
			return a, nil
		}
		a.settings.edit = settingsEditor{}
		a.syncSettings()
		return a, cmd
	}
	var cmd tea.Cmd
	a.settings.edit.input, cmd = a.settings.edit.input.Update(key)
	return a, cmd
}

// receiveSettings applies a background result started from Settings, on
// whichever screen is showing.
func (a app) receiveSettings(msg settingsAsync) (app, tea.Cmd) {
	switch msg := msg.(type) {
	case modelsLoadedMsg:
		a.receiveModels(msg)
	case samplersLoadedMsg:
		a.receiveSamplers(msg)
	case instructionsEditedMsg:
		a.receiveInstructions(msg)
	case serverStatusMsg, serverActionMsg:
		return a.receiveServer(msg)
	}
	return a, nil
}
