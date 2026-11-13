package tui

import tea "github.com/charmbracelet/bubbletea"

func (a app) libraryRows() []settingRow {
	cfg, saved := a.cfg(), a.settings.saved
	lib := settingRow{key: "library_dir", name: "Library folder", kind: rowText,
		help: "Where songs and their audio are kept",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("Library folder", a.settings.saved.LibraryDir, func(a *app, v string) (tea.Cmd, error) {
				return a.commitFolder("library_dir", "Library folder", v)
			})
		}}
	restartValue(&lib, saved.LibraryDir, cfg.LibraryDir)
	state := settingRow{key: "state_dir", name: "State folder", kind: rowText,
		help: "Where songsmith keeps logs and the server's state",
		run: func(a *app) tea.Cmd {
			return a.beginEdit("State folder", a.settings.saved.StateDir, func(a *app, v string) (tea.Cmd, error) {
				return a.commitFolder("state_dir", "State folder", v)
			})
		}}
	restartValue(&state, saved.StateDir, cfg.StateDir)
	a.stopFirstHelp(&state, saved.StateDir, cfg.StateDir)
	file := settingRow{name: "Config file", kind: rowInfo, value: a.d.Config.Path(),
		help: "Every change here is saved to this file; its comments and other keys are kept"}
	return []settingRow{lib, state, file}
}
