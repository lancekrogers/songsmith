package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/setup"
)

// comfyFolder makes a finished ComfyUI install in a new temp folder with
// the music model files cfg names.
func comfyFolder(t *testing.T, a app) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ComfyUI")
	sparseFile(t, filepath.Join(dir, "main.py"), 1)
	sparseFile(t, filepath.Join(dir, ".venv", "bin", "python"), 1)
	if err := os.MkdirAll(filepath.Join(dir, ".venv", "lib", "python3.12", "site-packages", "torch"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range setup.ModelSlots(a.cfg().ComfyUI) {
		sparseFile(t, filepath.Join(dir, "models", s.Dir, s.Name), 1)
	}
	return dir
}

// shows reports whether the screen holds text, however it is wrapped: the
// sidebar and every space are left out of both.
func shows(a app, text string) bool {
	var b strings.Builder
	for _, l := range strings.Split(plain(a), "\n") {
		if _, rest, ok := strings.Cut(l, "│"); ok {
			l = rest
		}
		b.WriteString(l)
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	return strings.Contains(squash(b.String()), squash(text))
}

// editInstallFolder opens the Install folder editor on Server, empty.
func editInstallFolder(t *testing.T, a app) app {
	t.Helper()
	a = openSettingsOn(t, a, sectionServer)
	a = focus(t, a, "Install folder", -1)
	a = pressSettings(t, a, "enter")
	if !a.settings.edit.active {
		t.Fatalf("editor not open:\n%s", plain(a))
	}
	return clearEditor(t, a)
}

func TestInstallFolderWithComfyUISavesAtOnceAndSaysWhatItFound(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	dir := comfyFolder(t, a)
	a = editInstallFolder(t, a)
	saves := recordSaves(&a)
	a = typeIntoSettings(t, a, dir)
	if !strings.Contains(plain(a), "✓ ComfyUI found") {
		t.Fatalf("no live line:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if a.settings.edit.active || len(*saves) != 1 || (*saves)[0] != (savedKV{"comfyui.dir", dir}) {
		t.Fatalf("saves %v, editor open %v", *saves, a.settings.edit.active)
	}
	if !strings.Contains(a.settings.message, "ComfyUI found · model files: 3 of 3 present") {
		t.Fatalf("message %q", a.settings.message)
	}
	view := plain(a)
	if !strings.Contains(view, "↻ Restart songsmith to use this folder · after restart: ComfyUI found · model files: 3 of 3 present") {
		t.Fatalf("no restart line:\n%s", view)
	}
	if a.cfg().ComfyUI.Dir == dir {
		t.Fatal("the running folder changed before a restart")
	}
	a = focus(t, a, "Log", -1)
	if !strings.Contains(plain(a), restartNote) {
		t.Fatalf("restart line gone after moving:\n%s", plain(a))
	}
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 40})
	a = pressSettings(t, a, "esc")
	notice := ansi.Strip(strings.Join(a.launcherState().noticeLines(80), ""))
	if !strings.Contains(notice, "↻ Restart songsmith to use") || !strings.Contains(strings.ReplaceAll(notice, " ", ""), strings.ReplaceAll(dir, " ", "")) ||
		!strings.Contains(notice, "After restart: ComfyUI found · model files: 3 of 3 present") {
		t.Fatalf("launcher notice %q", notice)
	}
}

func TestInstallFolderWithoutComfyUINeedsASecondEnterOnTheSameText(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = editInstallFolder(t, a)
	saves := recordSaves(&a)
	dir := filepath.Join(t.TempDir(), "Typo", "ComfyUI")
	a = typeIntoSettings(t, a, dir)
	if !strings.Contains(plain(a), "folder does not exist") {
		t.Fatalf("live line:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	want := "No ComfyUI in " + dir + ". enter again to use this folder for a new install, or correct the path."
	if len(*saves) != 0 || !a.settings.edit.active || a.settings.edit.note != want || !shows(a, want) {
		t.Fatalf("first enter: saves %v note %q\n%s", *saves, a.settings.edit.note, plain(a))
	}

	// Typing undoes the confirmation, even when the text ends up the same.
	a = typeIntoSettings(t, a, "x")
	if a.settings.edit.note != "" {
		t.Fatalf("note kept after typing: %q", a.settings.edit.note)
	}
	a = pressSettings(t, a, "backspace", "enter")
	if len(*saves) != 0 || a.settings.edit.note != want {
		t.Fatalf("enter after a change saved: %v", *saves)
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"comfyui.dir", dir}) || a.settings.edit.active {
		t.Fatalf("second enter: saves %v", *saves)
	}
	if !strings.Contains(a.settings.message, "no ComfyUI · model files: 0 of 3 present") {
		t.Fatalf("message %q", a.settings.message)
	}

	// An existing folder without ComfyUI asks too.
	empty := t.TempDir()
	a = editInstallFolder(t, a)
	a = typeIntoSettings(t, a, empty)
	if !strings.Contains(plain(a), "no ComfyUI here") {
		t.Fatalf("live line:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || !strings.HasPrefix(a.settings.edit.note, "No ComfyUI in "+empty) {
		t.Fatalf("empty folder: saves %v note %q", *saves, a.settings.edit.note)
	}
}

func TestInstallFolderIsRefusedWhereItCannotBeCreated(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	a, _ := newSettingsApp(t, readyChecks())
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	vols := t.TempDir()
	old := volumesRoot
	volumesRoot = vols
	t.Cleanup(func() { volumesRoot = old })

	for _, c := range []struct{ dir, want string }{
		{filepath.Join(locked, "sub", "ComfyUI"), "Cannot create " + filepath.Join(locked, "sub", "ComfyUI") + ": " + locked + " is not writable"},
		{filepath.Join(vols, "NoSuchDisk", "ComfyUI"), "no volume named NoSuchDisk is mounted"},
	} {
		a = editInstallFolder(t, a)
		saves := recordSaves(&a)
		a = typeIntoSettings(t, a, c.dir)
		a = pressSettings(t, a, "enter", "enter")
		if len(*saves) != 0 || !a.settings.edit.active || !a.settings.edit.noteFailed || !strings.Contains(a.settings.edit.note, c.want) {
			t.Fatalf("%s: saves %v note %q", c.dir, *saves, a.settings.edit.note)
		}
		a = pressSettings(t, a, "esc")
	}
}

func TestInstallFolderTabCompletesFolderNames(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	base := t.TempDir()
	for _, d := range []string{"alpha", "albums", "beta", ".hidden", "m1", "m2", "m3", "m4", "m5", "m6"} {
		if err := os.Mkdir(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sparseFile(t, filepath.Join(base, "alfile"), 1)
	sparseFile(t, filepath.Join(base, "beta", "main.py"), 1)

	a = editInstallFolder(t, a)
	a = typeIntoSettings(t, a, base+"/b")
	a = pressSettings(t, a, "tab")
	if got := a.settings.edit.input.Value(); got != base+"/beta/" {
		t.Fatalf("one match: %q", got)
	}
	if a.settings.edit.hint != "✓ ComfyUI found" {
		t.Fatalf("hint after completing %q", a.settings.edit.hint)
	}

	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, base+"/a")
	a = pressSettings(t, a, "tab")
	if got := a.settings.edit.input.Value(); got != base+"/al" {
		t.Fatalf("two matches: %q", got)
	}
	if a.settings.edit.hint != "albums/  alpha/" || !strings.Contains(plain(a), "albums/  alpha/") {
		t.Fatalf("candidates %q\n%s", a.settings.edit.hint, plain(a))
	}

	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, base+"/m")
	a = pressSettings(t, a, "tab")
	if a.settings.edit.hint != "m1/  m2/  m3/  m4/  m5/  +1 more" {
		t.Fatalf("many matches %q", a.settings.edit.hint)
	}

	a = clearEditor(t, a)
	a = typeIntoSettings(t, a, base+"/zz")
	a = pressSettings(t, a, "tab")
	if got := a.settings.edit.input.Value(); got != base+"/zz" || a.settings.edit.hint != "no folder matches" {
		t.Fatalf("no match: %q, hint %q", got, a.settings.edit.hint)
	}
	if a.settings.section != sectionServer || !a.settings.edit.active {
		t.Fatal("tab left the editor")
	}
}

// newServerFolderApp is Setup with everything missing, the plan to install
// it, and a running server whose folder holds a ComfyUI.
func newServerFolderApp(t *testing.T) (app, *fakeInstaller, string) {
	t.Helper()
	a, srv := newSettingsApp(t, missingChecks())
	dir := comfyFolder(t, a)
	srv.installDir = dir
	fake := newFakeInstaller(missingPlan(a.cfg()))
	a.d.Installer = fake
	a = finishHealth(t, a)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 140, Height: 60})
	return a, fake, dir
}

func TestARunningServersFolderIsOfferedBeforeAnyInstall(t *testing.T) {
	a, fake, dir := newServerFolderApp(t)
	if got := menuLabels(a); got[0] != "Use that folder" || selectedLabel(a) != "Use that folder" {
		t.Fatalf("menu %q, selected %q", got, selectedLabel(a))
	}

	a = openSettingsOn(t, a, sectionSetup)
	view := plain(a)
	use, sep := lineOf(a, "Use that folder", -1), lineOf(a, "Install a separate copy", -1)
	if !shows(a, "A ComfyUI server is already running from "+dir+".") || use < 0 || sep < 0 {
		t.Fatalf("no offer:\n%s", view)
	}
	if lineOf(a, "Install everything missing", -1) >= 0 {
		t.Fatalf("Install everything missing offered:\n%s", view)
	}
	for _, r := range a.setupRows() {
		if r.verb == "install" && r.name != "Writer model" {
			t.Fatalf("install row %q offered", r.name)
		}
	}
	a = focus(t, a, "ComfyUI", -1)
	a = pressSettings(t, a, "enter")
	fake.noneStarted(t)

	saves := recordSaves(&a)
	a = focus(t, a, "Use that folder", -1)
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"comfyui.dir", dir}) {
		t.Fatalf("saves %v", *saves)
	}
	if lineOf(a, "Use that folder", -1) >= 0 || lineOf(a, "Install everything missing", -1) >= 0 {
		t.Fatalf("after taking the folder:\n%s", plain(a))
	}
	if !strings.Contains(plain(a), "↻ Restart songsmith to use this folder") {
		t.Fatalf("no restart line:\n%s", plain(a))
	}
}

func TestInstallingASeparateCopyBringsTheInstallRowsBack(t *testing.T) {
	a, _, _ := newServerFolderApp(t)
	a = openSettingsOn(t, a, sectionSetup)
	saves := recordSaves(&a)
	a = focus(t, a, "Install a separate copy", -1)
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || lineOf(a, "Use that folder", -1) >= 0 || lineOf(a, "Install everything missing", -1) < 0 {
		t.Fatalf("saves %v:\n%s", *saves, plain(a))
	}
	i := slices.IndexFunc(a.setupRows(), func(r settingRow) bool { return r.name == "ComfyUI" })
	if r := a.setupRows()[i]; r.verb != "install" {
		t.Fatalf("ComfyUI row %+v", r)
	}
	a = pressSettings(t, a, "esc")
	if slices.Contains(menuLabels(a), "Use that folder") {
		t.Fatalf("menu %q", menuLabels(a))
	}
}

func TestUseThatFolderFromTheLauncher(t *testing.T) {
	a, _, dir := newServerFolderApp(t)
	a, cmd := send(t, a, key("enter"))
	a, cmd = send(t, a, cmd())
	a = settle(t, a, cmd)
	saved := a.settings.saved.ComfyUI.Dir
	if saved != dir || a.settings.restartDir != dir {
		t.Fatalf("saved %q, restart %q", saved, a.settings.restartDir)
	}
	if !strings.Contains(a.notice, "ComfyUI found · model files: 3 of 3 present") || slices.Contains(menuLabels(a), "Use that folder") {
		t.Fatalf("notice %q, menu %q", a.notice, menuLabels(a))
	}
	if !strings.Contains(plain(a), "↻ Restart songsmith to use") {
		t.Fatalf("launcher:\n%s", plain(a))
	}
}

func TestLauncherShowsTheWholeFolderAt80Columns(t *testing.T) {
	a, srv := newSettingsApp(t, missingChecks())
	long := filepath.Join(t.TempDir(), "a-rather-long-folder-name-for-music-tools", "another-long-part-of-the-path", "ComfyUI")
	sparseFile(t, filepath.Join(long, "main.py"), 1)
	srv.installDir = long
	a = finishHealth(t, a)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: 80, Height: 40})
	lines := a.launcherState().noticeLines(76)
	var joined string
	for _, l := range lines {
		l = ansi.Strip(l)
		if ansi.StringWidth(l) > 76 {
			t.Fatalf("line wider than the notice: %q", l)
		}
		joined += strings.TrimSpace(l)
	}
	if !strings.Contains(joined, long) || strings.Contains(joined, "…") {
		t.Fatalf("path %q not whole in %q", long, joined)
	}
	if view := plain(a); !strings.Contains(view, "A ComfyUI server is already running from") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestWrapTextBreaksALongPathAfterASlash(t *testing.T) {
	got := wrapText("No ComfyUI in /one/two/three/four. Set it.", 12)
	want := []string{"No ComfyUI", "in", "/one/two/", "three/four.", "Set it."}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}
