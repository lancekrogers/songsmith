package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/health"
)

func TestSettingsSwitchesSectionsWithEveryKey(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionSetup)
	saves := recordSaves(&a)
	steps := []struct {
		key  string
		want settingsSection
	}{
		{"right", sectionWriter}, {"l", sectionMusicModel}, {"tab", sectionRender},
		{"left", sectionMusicModel}, {"h", sectionWriter}, {"shift+tab", sectionSetup},
		{"left", sectionLibrary}, {"right", sectionSetup},
		{"5", sectionServer}, {"6", sectionAdvanced}, {"7", sectionLibrary}, {"1", sectionSetup}, {"3", sectionMusicModel},
		{"8", sectionMusicModel}, {"0", sectionMusicModel},
	}
	for _, s := range steps {
		a = pressSettings(t, a, s.key)
		if a.settings.section != s.want {
			t.Fatalf("%q: section %s, want %s", s.key, sectionNames[a.settings.section], sectionNames[s.want])
		}
		if want := "  " + string(rune('1'+s.want)) + " " + sectionNames[s.want]; !strings.Contains(plain(a), want) {
			t.Fatalf("sidebar missing %q", want)
		}
	}
	if len(*saves) != 0 {
		t.Fatalf("switching sections saved %v", *saves)
	}
}

func TestSettingsCursorSkipsHeadingsAndBlanks(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	for sec := range settingsSectionCount {
		a = openSettingsOn(t, a, sec)
		_, lines := a.settingsLayout()
		for range len(lines) + 2 {
			if !lines[a.settings.cursor].focusable() {
				t.Fatalf("%s: cursor on line %+v", sectionNames[sec], lines[a.settings.cursor])
			}
			a = pressSettings(t, a, "down")
		}
		for range len(lines) + 2 {
			a = pressSettings(t, a, "up")
			if !lines[a.settings.cursor].focusable() {
				t.Fatalf("%s: cursor on line %+v", sectionNames[sec], lines[a.settings.cursor])
			}
		}
	}
}

func TestSettingsFirstVisitLandsOnTheCheckedOptionOfTheFirstList(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	if name, opt := focusedName(a); name != "Model" || opt != 1 {
		t.Fatalf("Writer opened on %q option %d, want Model option 1 (%s)", name, opt, testModel)
	}
	a = pressSettings(t, a, "4")
	if name, _ := focusedName(a); name != "Steps" {
		t.Fatalf("Render opened on %q", name)
	}
}

func TestSettingsRemembersEachSectionsCursorAndLastSection(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	a = pressSettings(t, a, "down", "down")
	a = pressSettings(t, a, "7", "down", "4")
	if name, _ := focusedName(a); name != "Top-k" {
		t.Fatalf("back in Render on %q, want Top-k", name)
	}
	a = pressSettings(t, a, "esc")
	if a.screen != screenLauncher {
		t.Fatalf("esc went to %v", a.screen)
	}
	a, cmd := send(t, a, switchScreenMsg(screenSettings))
	a = settle(t, a, cmd)
	if a.settings.section != sectionRender {
		t.Fatalf("Settings reopened on %s, want the last used section", sectionNames[a.settings.section])
	}
	a = pressSettings(t, a, "7")
	if name, _ := focusedName(a); name != "State folder" {
		t.Fatalf("back in Library on %q, want State folder", name)
	}
}

func TestFinishSetupOpensSettingsOnSetup(t *testing.T) {
	a, _ := newSettingsApp(t, missingModelChecks())
	a = openSettingsOn(t, a, sectionRender)
	a = pressSettings(t, a, "esc")
	if selectedLabel(a) != "Finish setup" {
		t.Fatalf("selected %q", selectedLabel(a))
	}
	a = nav(t, a, "enter")
	if a.screen != screenSettings || a.settings.section != sectionSetup {
		t.Fatalf("Finish setup opened screen %v section %s", a.screen, sectionNames[a.settings.section])
	}
}

func TestNoSectionHoldsASingleRow(t *testing.T) {
	for _, checks := range [][]health.Check{readyChecks(), nothingInstalled()} {
		a, _ := newSettingsApp(t, checks)
		for sec := range settingsSectionCount {
			a = openSettingsOn(t, a, sec)
			if n := len(a.sectionRows()); n < 2 {
				t.Errorf("%s holds %d rows", sectionNames[sec], n)
			}
		}
	}
}

func TestSettingsStatusLineSaysWhenAChangeApplies(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	for sec := range settingsSectionCount {
		a = openSettingsOn(t, a, sec)
		rows, lines := a.settingsLayout()
		for i, l := range lines {
			if !l.focusable() {
				continue
			}
			row := rows[l.row]
			if row.key == "" && row.kind != rowAction {
				continue
			}
			if row.applies == "" {
				t.Errorf("%s: %q does not say when it applies", sectionNames[sec], row.name)
				continue
			}
			a.placeSettingsCursor(rows, lines, i)
			if status := plain(a); !strings.Contains(status, capitalize(row.applies)) {
				t.Errorf("%s: %q status line lacks %q:\n%s", sectionNames[sec], row.name, row.applies, status)
			}
		}
	}
}

func TestSettingsFooterListsOnlyKeysForTheFocusedLine(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	cases := []struct {
		sec  settingsSection
		name string
		opt  int
		want string
	}{
		{sectionWriter, "Model", 0, "↑/↓ move   ←/→ section   enter choose   esc back"},
		{sectionWriter, "Temperature", -1, "↑/↓ move   ←/→ section   +/- adjust   enter type   esc back"},
		{sectionAdvanced, "Ollama URL", -1, "↑/↓ move   ←/→ section   enter edit   esc back"},
		{sectionWriter, "Writing instructions", -1, "↑/↓ move   ←/→ section   enter edit   esc back"},
		{sectionRender, "Default length", -1, "↑/↓ move   ←/→ section   space/+/- step   enter type   esc back"},
		{sectionAdvanced, "Unload the writer before rendering", -1, "↑/↓ move   ←/→ section   enter/space toggle   esc back"},
		{sectionSetup, "Writer model", -1, "↑/↓ move   ←/→ section   esc back"},
		{sectionSetup, "Re-run checks", -1, "↑/↓ move   ←/→ section   enter re-run   esc back"},
		{sectionLibrary, "Config file", -1, "↑/↓ move   ←/→ section   esc back"},
	}
	for _, c := range cases {
		a = openSettingsOn(t, a, c.sec)
		a = focus(t, a, c.name, c.opt)
		lines := strings.Split(plain(a), "\n")
		if got := strings.TrimSpace(lines[len(lines)-1]); got != c.want {
			t.Errorf("%s: footer %q, want %q", c.name, got, c.want)
		}
	}
	a = focus(t, a, "Library folder", -1)
	a = pressSettings(t, a, "enter")
	lines := strings.Split(plain(a), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "enter save   esc cancel" {
		t.Errorf("editor footer %q", got)
	}
}

func TestSliderStepsAndClampsAtItsBounds(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	saves := recordSaves(&a)
	a = focus(t, a, "Temperature", -1)
	a = pressSettings(t, a, "+")
	if got := a.cfg().Ollama.Temperature; got != 0.9 {
		t.Fatalf("temperature %v after +", got)
	}
	a = pressSettings(t, a, " ")
	if got := a.cfg().Ollama.Temperature; got != 0.9 || len(*saves) != 1 {
		t.Fatalf("space moved the slider: %v, saves %v", got, *saves)
	}
	for range 10 {
		a = pressSettings(t, a, "+")
	}
	if got := a.cfg().Ollama.Temperature; got != 1.5 {
		t.Fatalf("temperature %v, want clamped at 1.5", got)
	}
	n := len(*saves)
	a = pressSettings(t, a, "=")
	if len(*saves) != n || !strings.Contains(plain(a), "Temperature is at the end of the slider, 1.5; enter types another value") {
		t.Fatalf("a press at the limit saved or said nothing: %v\n%s", *saves, plain(a))
	}
	for range 20 {
		a = pressSettings(t, a, "-")
	}
	if got := a.cfg().Ollama.Temperature; got != 0 {
		t.Fatalf("temperature %v, want clamped at 0.0", got)
	}
	data, _ := os.ReadFile(a.d.Config.Path())
	if !strings.Contains(string(data), "temperature: 0\n") {
		t.Fatalf("config file:\n%s", data)
	}
}

func TestChoiceStepsWithSpaceAndPlusMinusAndStopsAtTheEnds(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	saves := recordSaves(&a)
	a = focus(t, a, "Default length", -1)
	a = pressSettings(t, a, " ", " ")
	if got := a.cfg().Render.DefaultSeconds; got != 180 {
		t.Fatalf("space: %d", got)
	}
	a = pressSettings(t, a, "-", "_")
	if got := a.cfg().Render.DefaultSeconds; got != 120 {
		t.Fatalf("- -: %d", got)
	}
	for range 10 {
		a = pressSettings(t, a, "+")
	}
	if got := a.cfg().Render.DefaultSeconds; got != 300 || !strings.Contains(plain(a), "Default length is at its last option") {
		t.Fatalf("+ x10: %d\n%s", got, plain(a))
	}
	last := (*saves)[len(*saves)-1]
	if last.key != "render.default_seconds" || last.value != 300 {
		t.Fatalf("last save %+v", last)
	}
}

func TestTextRowEditorCommitsCancelsAndOwnsKeys(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionLibrary)
	saves := recordSaves(&a)
	a = focus(t, a, "State folder", -1)
	a = pressSettings(t, a, "enter")
	if !a.settings.edit.active || a.settings.edit.input.Value() != a.cfg().StateDir || !strings.Contains(plain(a), "State folder: ") {
		t.Fatalf("editor not open:\n%s", plain(a))
	}
	a = pressSettings(t, a, "esc")
	if a.settings.edit.active || a.screen != screenSettings || len(*saves) != 0 || !strings.Contains(plain(a), "Cancelled; nothing saved") {
		t.Fatalf("esc: %v %v\n%s", a.screen, *saves, plain(a))
	}

	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	dir := filepath.Join(t.TempDir(), "state 2")
	a = typeIntoSettings(t, a, dir)
	if a.settings.section != sectionLibrary {
		t.Fatal("typing a digit or l switched sections")
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"state_dir", dir}) {
		t.Fatalf("saves %v", *saves)
	}
}

func TestFailedSaveChangesNothingAndSaysWhy(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	dir := filepath.Dir(a.d.Config.Path())
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	before, _ := os.ReadFile(a.d.Config.Path())

	a = focus(t, a, "Steps", -1)
	a = pressSettings(t, a, "+")
	if a.cfg().Render.Steps != 30 || !a.settings.failed || !strings.Contains(plain(a), "Not saved:") || !strings.Contains(plain(a), "Steps         30") {
		t.Fatalf("steps %d failed %v:\n%s", a.cfg().Render.Steps, a.settings.failed, plain(a))
	}
	a = pressSettings(t, a, "7")
	a = focus(t, a, "Library folder", -1)
	running := a.cfg().LibraryDir
	a = pressSettings(t, a, "enter")
	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, "/tmp/songsmith-test-elsewhere")
	a = pressSettings(t, a, "enter")
	if a.cfg().LibraryDir != running || a.settings.saved.LibraryDir != running || !strings.Contains(plain(a), "Not saved:") {
		t.Fatalf("library %q saved %q:\n%s", a.cfg().LibraryDir, a.settings.saved.LibraryDir, plain(a))
	}
	after, _ := os.ReadFile(a.d.Config.Path())
	if string(after) != string(before) {
		t.Fatalf("config file changed:\n%s", after)
	}
}

func TestSettingsFitsCommonSizes(t *testing.T) {
	a, _ := newSettingsApp(t, nothingInstalled())
	sizes := [][2]int{{80, 24}, {120, 40}, {130, 34}, {200, 60}, {59, 24}, {40, 16}, {80, 11}, {80, 8}}
	for _, size := range sizes {
		a, _ = send(t, a, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for sec := range settingsSectionCount {
			a = openSettingsOn(t, a, sec)
			view := plain(a)
			lines := strings.Split(view, "\n")
			if len(lines) != size[1] {
				t.Errorf("%dx%d %s: %d lines", size[0], size[1], sectionNames[sec], len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%dx%d %s: line %d wide: %q", size[0], size[1], sectionNames[sec], w, l)
				}
			}
			strip := strings.Contains(view, "‹ ")
			if narrow := size[0] < 60; strip != narrow || !narrow && !strings.Contains(view, "│ ") {
				t.Errorf("%dx%d: strip %v:\n%s", size[0], size[1], strip, view)
			}
			if compact := size[1] < 12; compact == strings.Contains(view, "────") {
				t.Errorf("%dx%d: compact %v but rule shown %v", size[0], size[1], compact, !compact)
			}
		}
	}
}

// The cursor stays on screen when the section is longer than the terminal.
func TestSettingsScrollsToTheCursor(t *testing.T) {
	a, _ := newSettingsApp(t, nothingInstalled())
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 14})
	a = openSettingsOn(t, a, sectionSetup)
	if strings.Contains(plain(a), "Re-run checks") {
		t.Fatalf("whole section fits; the test needs a taller one:\n%s", plain(a))
	}
	for range 60 {
		a = pressSettings(t, a, "down")
	}
	view := plain(a)
	if !strings.Contains(view, "▸") || !strings.Contains(view, "Re-run checks") {
		t.Fatalf("bottom of Setup:\n%s", view)
	}
	a = pressSettings(t, a, "pgup", "pgup", "pgup", "pgup")
	if !strings.Contains(plain(a), "▸") || !strings.Contains(plain(a), "▲ Rendering is blocked") {
		t.Fatalf("top of Setup:\n%s", plain(a))
	}
}

func TestSettingsResultsAfterLeavingDoNotCrash(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a, open := send(t, a, switchScreenMsg(screenSettings))
	a = pressSettings(t, a, "2")
	a, write := send(t, a, settingsKey("6"))
	a, engine := send(t, a, settingsKey("5"))
	a = pressSettings(t, a, "esc")
	runCmds(tea.Batch(open, write, engine))
	for msg, ok := a.events.next(); ok; msg, ok = a.events.next() {
		a, _ = send(t, a, msg)
	}
	if a.screen != screenLauncher || !a.settings.models.loaded || !a.settings.samplers.loaded || !a.settings.server.known {
		t.Fatalf("late results: screen %v models %+v samplers %+v server %+v", a.screen, a.settings.models, a.settings.samplers, a.settings.server)
	}
	_ = a.View()
}
