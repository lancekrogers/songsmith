package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/setup"
)

func hasComfyUI(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "main.py"))
	return err == nil
}

// runningServerDir is the folder the server on the configured port runs
// from, when that folder holds a ComfyUI and is not dir.
func runningServerDir(ctx context.Context, srv ComfyServer, dir string) string {
	found, err := srv.InstallDir(ctx)
	if err != nil || found == "" || filepath.Clean(found) == filepath.Clean(dir) || !hasComfyUI(found) {
		return ""
	}
	return found
}

// folderValue checks a typed folder: absolute or under ~. save is what the
// config keeps (a ~/ path as typed, so the config stays portable); abs is
// the folder it names.
func folderValue(name, v string) (save, abs string, err error) {
	if v == "" {
		return "", "", errors.New(name + " cannot be empty; type a folder")
	}
	abs = config.ExpandHome(v)
	if !filepath.IsAbs(abs) {
		return "", "", errors.New(name + " must be a full path, such as /Users/you/Music or ~/Music")
	}
	abs = filepath.Clean(abs)
	save = abs
	if strings.HasPrefix(v, "~/") {
		save = strings.TrimRight(v, "/")
	}
	return save, abs, nil
}

// volumesRoot is where macOS mounts other disks; a folder under a volume
// that is not mounted cannot be created.
var volumesRoot = "/Volumes"

// cannotCreate says why ComfyUI could not be installed in dir, or "".
func cannotCreate(dir string) string {
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return dir + " is a file, not a folder. Type a folder."
	}
	if rest, ok := strings.CutPrefix(dir, volumesRoot+"/"); ok {
		vol, _, _ := strings.Cut(rest, "/")
		if _, err := os.Stat(filepath.Join(volumesRoot, vol)); err != nil {
			return "Cannot create " + dir + ": no volume named " + vol + " is mounted. Correct the path."
		}
	}
	p := dir
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		up := filepath.Dir(p)
		if up == p {
			break
		}
		p = up
	}
	if syscall.Access(p, 2) != nil {
		if p == dir {
			return "Cannot install into " + dir + ": it is not writable. Correct the path."
		}
		return "Cannot create " + dir + ": " + p + " is not writable. Correct the path."
	}
	return ""
}

// comfyDirHint says what the typed install folder holds.
func comfyDirHint(v string) string {
	abs := config.ExpandHome(strings.TrimSpace(v))
	if !filepath.IsAbs(abs) {
		return "type a full path"
	}
	if hasComfyUI(abs) {
		return "✓ ComfyUI found"
	}
	fi, err := os.Stat(abs)
	switch {
	case err != nil:
		return "folder does not exist"
	case !fi.IsDir():
		return "a file, not a folder"
	}
	return "no ComfyUI here"
}

// completeDir completes the last part of a typed path to the longest
// prefix the folders it could name share. matches are those folders' names.
func completeDir(v string) (next string, matches []string) {
	i := strings.LastIndex(v, "/")
	if i < 0 {
		return v, nil
	}
	head, base := v[:i+1], v[i+1:]
	dir := config.ExpandHome(head)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return v, nil
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || !fi.IsDir() {
			continue
		}
		matches = append(matches, name)
	}
	switch len(matches) {
	case 0:
		return v, nil
	case 1:
		return head + matches[0] + "/", matches
	}
	return head + commonPrefix(matches), matches
}

func commonPrefix(names []string) string {
	p := []rune(names[0])
	for _, n := range names[1:] {
		r := []rune(n)
		k := 0
		for k < len(p) && k < len(r) && p[k] == r[k] {
			k++
		}
		p = p[:k]
	}
	return string(p)
}

// completionHint is the line under the editor after tab: the folders that
// match when there are several.
func completionHint(matches []string, hint string) string {
	switch len(matches) {
	case 0:
		return "no folder matches"
	case 1:
		return hint
	}
	shown := matches[:min(len(matches), 5)]
	line := strings.Join(shown, "/  ") + "/"
	if more := len(matches) - len(shown); more > 0 {
		line += fmt.Sprintf("  +%d more", more)
	}
	return line
}

// editInstallFolder opens the editor on the install folder, with what the
// typed folder holds under it and tab completing folder names.
func (a *app) editInstallFolder() tea.Cmd {
	initial := a.settings.saved.ComfyUI.Dir
	cmd := a.beginEdit("Install folder", initial, func(a *app, v string) (tea.Cmd, error) {
		return a.commitComfyDir(v)
	})
	e := &a.settings.edit
	e.live, e.complete, e.hint = comfyDirHint, completeDir, comfyDirHint(initial)
	return cmd
}

// commitComfyDir saves a folder with a ComfyUI in it at once. A folder
// without one is saved for a new install only on a second enter with the
// same text, and never where it could not be created.
func (a *app) commitComfyDir(v string) (tea.Cmd, error) {
	save, abs, err := folderValue("Install folder", v)
	if err != nil {
		return nil, err
	}
	e := &a.settings.edit
	if !hasComfyUI(abs) {
		if why := cannotCreate(abs); why != "" {
			e.note, e.noteFailed, e.confirm = why, true, ""
			a.clearSettingsNote()
			return nil, errSaveFailed
		}
		if e.confirm != v {
			e.note, e.noteFailed, e.confirm = "No ComfyUI in "+abs+". enter again to use this folder for a new install, or correct the path.", false, v
			a.clearSettingsNote()
			return nil, errSaveFailed
		}
	}
	if !a.saveComfyDir(save) {
		return nil, errSaveFailed
	}
	return a.checkAfterRestart(), nil
}

// saveComfyDir saves the install folder and says what it holds. The running
// server and checks keep the folder they started with, so another folder
// waits for a restart.
func (a *app) saveComfyDir(save string) bool {
	if !a.saveSetting("comfyui.dir", save) {
		return false
	}
	abs := filepath.Clean(config.ExpandHome(save))
	a.settings.restartDir = ""
	if abs != filepath.Clean(a.cfg().ComfyUI.Dir) {
		a.settings.restartDir = abs
	}
	a.settingsNote("Install folder: " + save + " · " + folderSummary(a.settings.saved.ComfyUI, abs))
	return true
}

// folderSummary says whether dir holds ComfyUI and how many of the music
// model files c names are in it.
func folderSummary(c config.ComfyUI, dir string) string {
	comfy := "no ComfyUI"
	switch st := setup.InspectComfyUI(dir); {
	case st.Status == setup.ComfyInstalled:
		comfy = "ComfyUI found"
	case st.HasCheckout:
		comfy = "ComfyUI found, install not finished"
	}
	slots := setup.ModelSlots(c)
	n := 0
	for _, s := range slots {
		if _, err := os.Stat(filepath.Join(dir, "models", s.Dir, s.Name)); err == nil {
			n++
		}
	}
	return fmt.Sprintf("%s · model files: %d of %d present", comfy, n, len(slots))
}

// afterRestartState is the checks run against the install folder saved for
// the next start, for display only.
type afterRestartState struct {
	seq    int
	done   bool
	checks []health.Check
}

type afterRestartMsg struct {
	seq    int
	checks []health.Check
}

func (afterRestartMsg) settingsAsync() {}

func (a *app) checkAfterRestart() tea.Cmd {
	ar := &a.settings.after
	ar.seq++
	ar.done, ar.checks = false, nil
	if a.settings.restartDir == "" {
		return nil
	}
	cfg := a.cfg()
	cfg.ComfyUI.Dir = a.settings.restartDir
	seq, run, ctx := ar.seq, a.d.Checks, a.ctx
	return a.events.run(func(send func(tea.Msg)) {
		ctx, cancel := context.WithTimeout(ctx, healthTimeout)
		defer cancel()
		send(afterRestartMsg{seq: seq, checks: run(ctx, cfg)})
	})
}

func (a *app) receiveAfterRestart(msg afterRestartMsg) {
	if ar := &a.settings.after; msg.seq == ar.seq {
		ar.done, ar.checks = true, msg.checks
	}
}

// afterRestart sums up the checks of the folder saved for the next start.
func (a app) afterRestart() string {
	ar := a.settings.after
	if !ar.done {
		return "checking…"
	}
	comfy := "no working ComfyUI"
	if c, ok := health.Find(ar.checks, health.IDComfyUI); ok && c.Result == health.OK {
		comfy = "ComfyUI found"
	}
	n := 0
	for _, id := range modelChecks {
		if c, ok := health.Find(ar.checks, id); ok && c.Result == health.OK {
			n++
		}
	}
	return fmt.Sprintf("%s · model files: %d of %d present", comfy, n, len(modelChecks))
}

const restartNote = "Restart songsmith to use this folder"

// serverFolder is the folder a ComfyUI server on the configured port runs
// from, offered while the install folder has no ComfyUI until the user takes
// it, saves another folder or chooses a separate install.
func (a app) serverFolder() (string, bool) {
	d := a.health.serverDir
	return d, d != "" && a.settings.restartDir == "" && !a.settings.separateCopy
}

// installsWithheld: ComfyUI and model installs go into the running install
// folder, so they wait while a running server's folder is offered or a new
// folder waits for a restart.
func (a app) installsWithheld() bool {
	_, offered := a.serverFolder()
	return offered || a.settings.restartDir != ""
}

func (a app) canInstall(act setup.Action) bool {
	return installable(act) && (act.Kind == setup.KindWriter || !a.installsWithheld())
}

func (a app) withheldNote() string {
	if a.settings.restartDir != "" {
		return "restart songsmith to use the new install folder"
	}
	return "use the running server's folder, or choose a separate copy, in Setup"
}

// serverFolderRows offer the running server's folder, above any install.
func (a app) serverFolderRows(dir string, width int) []settingRow {
	cur := a.cfg().ComfyUI.Dir
	rows := styledReportRows("Running server",
		"A ComfyUI server is already running from "+dir+". The install folder, "+cur+", has no ComfyUI.", width, &warningStyle)
	use := settingRow{name: "Use that folder", kind: rowAction, verb: "use", applies: appliesRestart,
		value: shortPath(dir), desc: "Make it the install folder", help: "Sets the install folder (config key comfyui.dir) to " + dir,
		run: func(a *app) tea.Cmd { return a.useServerFolder(dir) }}
	separate := settingRow{name: "Install a separate copy", kind: rowAction, verb: "choose", applies: appliesNow,
		value: "in " + shortPath(cur), desc: "Keep the install folder and install ComfyUI there", help: "Keeps the install folder and offers to install ComfyUI and the model files into " + cur,
		run: func(a *app) tea.Cmd {
			a.settings.separateCopy = true
			a.settingsNote("Installs go into " + cur)
			return nil
		}}
	return append(rows, use, separate)
}

func (a *app) useServerFolder(dir string) tea.Cmd {
	if !a.saveComfyDir(dir) {
		return nil
	}
	return a.checkAfterRestart()
}

// useServerFolderMsg takes the running server's folder from the launcher.
type useServerFolderMsg struct{ dir string }

func (a app) receiveUseServerFolder(msg useServerFolderMsg) (app, tea.Cmd) {
	cmd := a.useServerFolder(msg.dir)
	if a.settings.failed {
		a.err = a.settings.message
	} else {
		a.notice = a.settings.message
	}
	a.clearSettingsNote()
	return a, cmd
}
