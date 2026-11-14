package tui

import (
	"errors"
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
	sectionMaster
	sectionServer
	sectionAdvanced
	sectionLibrary
	settingsSectionCount
)

var sectionNames = [settingsSectionCount]string{
	sectionSetup:      "Setup",
	sectionWriter:     "Writer",
	sectionMusicModel: "Music model",
	sectionRender:     "Render",
	sectionMaster:     "Master",
	sectionServer:     "Server",
	sectionAdvanced:   "Advanced",
	sectionLibrary:    "Library",
}

// settingsModel is the Settings screen's state. It lives on the app for the
// whole run, so the screen reopens on the section last used and results that
// arrive after leaving it still land.
type settingsModel struct {
	section settingsSection
	rowCursor
	// marks remembers the line each section was on; visited says which
	// sections have one rather than their home line.
	marks   [settingsSectionCount]lineAnchor
	visited [settingsSectionCount]bool

	// message is the latest outcome; failed draws it as an error.
	message string
	failed  bool
	edit    settingsEditor
	// text is a file open in the text editor, which then fills the screen.
	text *settingsText

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

	// restartDir is an install folder saved for the next start, and after
	// its checks; separateCopy is set once the user chose to install apart
	// from the folder a running server uses.
	restartDir   string
	after        afterRestartState
	separateCopy bool
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
	a.settings.edit, a.settings.text = settingsEditor{}, nil
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
	case sectionServer:
		a.refreshComfyFound()
		return a.loadServerStatus()
	case sectionRender:
		return a.loadServerStatus()
	case sectionAdvanced:
		return a.loadSamplers()
	}
	return nil
}

// One key, one verb: ←/→ (h/l, tab, digits) switch sections, ↑/↓ move
// between lines (home/end to either end), enter acts on the line, space
// steps a choice, +/- adjust, esc leaves.
func (a app) updateSettings(msg tea.Msg) (app, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	if a.settings.text != nil {
		return a.updateSettingsText(key)
	}
	if a.settings.edit.active {
		return a.updateSettingsEditor(key)
	}
	a.syncSettings()
	if a.settings.filter.on {
		cmd, done := a.updateSettingsFilter(key)
		if done {
			a.syncSettings()
			return a, cmd
		}
	}
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
	case "home", "g", "end", "G":
		a.clearSettingsNote()
		a.moveSettingsCursorToEnd(k == "end" || k == "G")
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
	case "/":
		a.startSettingsFilter()
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

// startSettingsFilter opens a filter on the long list under the cursor.
func (a *app) startSettingsFilter() {
	row, line, ok := a.focusedSetting()
	if !ok || line.option < 0 || !row.long() {
		return
	}
	a.clearSettingsNote()
	a.settings.filter = settingsFilter{on: true, row: row.anchorName()}
}

// updateSettingsFilter gives a typed filter the keys it owns: every
// printable key types, so digits and letters never switch sections. ↑/↓
// move among the matches, enter chooses one and closes the filter, esc
// closes it. done is false for a key the filter leaves to Settings.
func (a *app) updateSettingsFilter(key tea.KeyMsg) (cmd tea.Cmd, done bool) {
	f := &a.settings.filter
	switch k := key.String(); k {
	case "esc":
		a.settings.filter = settingsFilter{}
		a.clearSettingsNote()
		return nil, true
	case "enter":
		row, line, ok := a.focusedSetting()
		if !ok || line.option < 0 || row.anchorName() != f.row {
			a.settingsNote("Nothing matches " + quoted(f.text) + "; backspace to change it, esc to clear it")
			return nil, true
		}
		a.settings.filter = settingsFilter{}
		a.syncSettings()
		return a.activateSetting(row, line), true
	case "backspace":
		if r := []rune(f.text); len(r) > 0 {
			f.text = string(r[:len(r)-1])
			a.refilter()
		}
		return nil, true
	case "up", "down", "pgup", "pgdown", "home", "end", "left", "right", "tab", "shift+tab":
		if k == "left" || k == "right" || k == "tab" || k == "shift+tab" {
			a.settings.filter = settingsFilter{}
		}
		return nil, false
	}
	if key.Type == tea.KeyRunes || key.Type == tea.KeySpace {
		f.text += string(key.Runes)
		a.refilter()
	}
	return nil, true
}

// refilter puts the cursor on the first option the filter matches.
func (a *app) refilter() {
	rows, lines := a.settingsLayout()
	a.settings.window = listTop{}
	at := slices.IndexFunc(lines, func(l settingLine) bool {
		return l.focusable() && rows[l.row].anchorName() == a.settings.filter.row
	})
	if at < 0 {
		return
	}
	a.placeSettingsCursor(rows, lines, at)
}

func quoted(s string) string { return "“" + s + "”" }

func (a *app) settingsNote(msg string)  { a.settings.message, a.settings.failed = msg, false }
func (a *app) settingsError(msg string) { a.settings.message, a.settings.failed = msg, true }
func (a *app) clearSettingsNote()       { a.settings.message, a.settings.failed = "", false }

// saveSetting writes key to the config file first; the running settings
// change only when that worked. A failure is shown and nothing changes.
func (a *app) saveSetting(key string, value any) bool {
	if err := a.saveConfig(key, value); err != nil {
		a.settingsError("Not saved: " + err.Error())
		return false
	}
	return true
}

// saveConfig writes key to the config file, then to the running settings.
// Settings and the song form both save through it.
func (a *app) saveConfig(key string, value any) error {
	save := a.settings.save
	if save == nil {
		save = a.d.Config.Set
	}
	if err := save(key, value); err != nil {
		return err
	}
	a.reloadSaved()
	if slices.Contains(checkedKeys, key) {
		a.settings.recheck = true
	}
	return nil
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

	// live says what the typed value is, on the line under it; complete
	// completes it on tab. Either may be nil.
	live     func(value string) string
	complete func(value string) (string, []string)
	hint     string
	// note stands in for the hint after enter: why the value was not saved.
	// confirm is a value a second enter saves as typed.
	note       string
	noteFailed bool
	confirm    string
}

func (a *app) beginEdit(label, initial string, commit func(a *app, value string) (tea.Cmd, error)) tea.Cmd {
	a.settings.edit = newEditor(label, initial, a.settingsContentWidth(), commit)
	a.clearSettingsNote()
	return textinput.Blink
}

// newEditor is an open one-line editor on initial, in a column width wide.
func newEditor(label, initial string, width int, commit func(a *app, value string) (tea.Cmd, error)) settingsEditor {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 1024
	in.Width = max(width-len(label)-6, 10)
	in.SetValue(initial)
	in.CursorEnd()
	in.Focus()
	return settingsEditor{active: true, label: label, input: in, commit: commit}
}

// updateSettingsEditor owns every key while the editor is open, so typing
// "l" or a digit edits the value instead of switching sections.
func (a app) updateSettingsEditor(key tea.KeyMsg) (app, tea.Cmd) {
	switch key.String() {
	case "esc":
		a.settings.edit = settingsEditor{}
		a.settingsNote("Cancelled; nothing saved")
		return a, nil
	case "tab":
		if e := &a.settings.edit; e.complete != nil {
			before := e.input.Value()
			next, matches := e.complete(before)
			if next != before {
				e.input.SetValue(next)
				e.input.CursorEnd()
				a.editorChanged()
			}
			hint := ""
			if e.live != nil {
				hint = e.live(next)
			}
			e.hint = completionHint(matches, hint)
			return a, nil
		}
	case "enter":
		value := strings.TrimSpace(a.settings.edit.input.Value())
		cmd, err := a.settings.edit.commit(&a, value)
		if errors.Is(err, errSaveFailed) {
			// The editor stays open with the typed value; the reason is shown.
			return a, cmd
		}
		if err != nil {
			a.settingsError(err.Error())
			return a, nil
		}
		a.settings.edit = settingsEditor{}
		a.syncSettings()
		return a, cmd
	}
	var cmd tea.Cmd
	before := a.settings.edit.input.Value()
	a.settings.edit.input, cmd = a.settings.edit.input.Update(key)
	if a.settings.edit.input.Value() != before {
		a.editorChanged()
	}
	return a, cmd
}

// editorChanged drops what was said about the value before it changed.
func (a *app) editorChanged() {
	e := &a.settings.edit
	e.note, e.noteFailed, e.confirm = "", false, ""
	if e.live != nil {
		e.hint = e.live(e.input.Value())
	}
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
	case afterRestartMsg:
		a.receiveAfterRestart(msg)
	}
	return a, nil
}
