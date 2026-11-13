package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
)

type launcherAction int

const (
	actionNew launcherAction = iota
	actionSetup
	actionNowRendering
	actionLibrary
	actionSettings
	actionQuit
)

type launcherItem struct {
	action launcherAction
	glyph  string
	label  string
	hint   string
}

// launcherState is what the launcher shows, gathered by the app from the
// health checks, the library and the queue.
type launcherState struct {
	// checking is true while the checks run; chips then say so rather than
	// show a stale or guessed state.
	checking bool
	// checked is true once any run has finished.
	checked bool
	checks  []health.Check

	songs     int
	queued    int
	rendering *library.Song
	progress  backend.Progress

	writerModel string
	workerNote  string
	notice, err string
}

func (a app) launcherState() launcherState {
	st := launcherState{
		checking:    a.health.running || !a.health.done,
		checked:     a.health.done,
		checks:      a.health.checks,
		songs:       len(a.library.songs),
		writerModel: a.cfg().Ollama.Model,
		notice:      a.notice,
		err:         a.err,
	}
	for _, s := range a.library.songs {
		if s.Status == library.StatusQueued {
			st.queued++
		}
	}
	if s, p, ok := a.renderingSong(); ok {
		st.rendering, st.progress = &s, p
	}
	if a.d.WorkerNote != nil {
		st.workerNote = a.d.WorkerNote()
	}
	return st
}

// blocker is the first failed check that stops rendering, from the latest
// finished run.
func (st launcherState) blocker() (health.Check, bool) {
	return health.Blocker(st.checks)
}

func (st launcherState) items() []launcherItem {
	var items []launcherItem
	if _, ok := st.blocker(); ok {
		items = append(items, launcherItem{actionSetup, "▲", "Finish setup", "See what is missing and how to fix it"})
	}
	newHint := fmt.Sprintf("Describe a song; %s drafts lyrics and a style for you to edit", st.writerModel)
	if w, ok := health.Find(st.checks, health.IDWriter); ok && w.Result != health.OK {
		newHint = "Write lyrics and a style yourself; the writer model is unavailable"
	}
	items = append(items, launcherItem{actionNew, "✦", "New song", newHint})
	if st.rendering != nil {
		items = append(items, launcherItem{actionNowRendering, "◐", "Now rendering",
			fmt.Sprintf("%s · %s · open it in the library", st.rendering.Title, progressLabel(st.progress))})
	}
	items = append(items,
		launcherItem{actionLibrary, "▤", fmt.Sprintf("Library (%d)", st.songs), "Play, edit and re-render your songs"},
		launcherItem{actionSettings, "⚙", "Settings", "Setup checks and the config file"},
	)
	quitHint := "Exit songsmith"
	if st.rendering != nil {
		quitHint = "Exit songsmith; ComfyUI finishes the current song and songsmith collects it next time"
	}
	return append(items, launcherItem{actionQuit, "✕", "Quit", quitHint})
}

type launcherModel struct {
	selected launcherAction
	// setupShown remembers whether Finish setup was listed, so it is
	// preselected only when it appears.
	setupShown bool
}

func newLauncher() launcherModel { return launcherModel{selected: actionNew} }

// sync keeps the selection on a listed item and selects Finish setup when it
// first appears.
func (m launcherModel) sync(st launcherState) launcherModel {
	_, blocked := st.blocker()
	if blocked && !m.setupShown {
		m.selected = actionSetup
	}
	m.setupShown = blocked
	if !slices.ContainsFunc(st.items(), func(it launcherItem) bool { return it.action == m.selected }) {
		m.selected = actionNew
	}
	return m
}

func (m launcherModel) cursor(items []launcherItem) int {
	return max(0, slices.IndexFunc(items, func(it launcherItem) bool { return it.action == m.selected }))
}

func (a app) updateLauncher(msg tea.Msg) (app, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	a.notice, a.err = "", ""
	var cmd tea.Cmd
	a.launcher, cmd = a.launcher.update(key, a.launcherState())
	return a, cmd
}

func (m launcherModel) update(key tea.KeyMsg, st launcherState) (launcherModel, tea.Cmd) {
	items := st.items()
	i := m.cursor(items)
	switch key.String() {
	case "up", "k":
		m.selected = items[max(0, i-1)].action
	case "down", "j":
		m.selected = items[min(len(items)-1, i+1)].action
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "enter":
		switch items[i].action {
		case actionSetup:
			return m, func() tea.Msg { return switchScreenMsg(screenSetup) }
		case actionNew:
			return m, func() tea.Msg { return openComposeMsg{} }
		case actionNowRendering:
			id := st.rendering.ID
			return m, func() tea.Msg { return openLibraryMsg{songID: id} }
		case actionLibrary:
			return m, func() tea.Msg { return switchScreenMsg(screenLibrary) }
		case actionSettings:
			return m, func() tea.Msg { return switchScreenMsg(screenSettings) }
		case actionQuit:
			return m, func() tea.Msg { return quitMsg{} }
		}
	}
	return m, nil
}

// view draws the full launcher, or the compact one when the full one would
// not fit the terminal.
func (m launcherModel) view(st launcherState, width, height int) string {
	full := m.fullView(st, width)
	if height > 0 && lipgloss.Height(full) > height {
		return m.compactView(st, width, height)
	}
	return full
}

func (m launcherModel) fullView(st launcherState, width int) string {
	inner := max(20, width-4)
	var b strings.Builder
	b.WriteString(brandPlate() + "\n\n")
	b.WriteString(st.chips(inner) + "\n\n")
	b.WriteString(strings.Join(st.noticeLines(inner), "\n") + "\n\n")

	menuW := min(64, inner)
	sel := lipgloss.NewStyle().Bold(true).Foreground(colorBg).Background(colorAccent).Width(menuW).Padding(0, 1)
	idle := lipgloss.NewStyle().Foreground(colorNormal).Width(menuW).Padding(0, 1)
	hint := lipgloss.NewStyle().Foreground(colorDim).Width(menuW).PaddingLeft(4)
	items := st.items()
	cur := m.cursor(items)
	for i, it := range items {
		label := ansi.Truncate(it.glyph+"  "+it.label, menuW-2, "…")
		if i == cur {
			b.WriteString(sel.Render(label) + "\n")
			b.WriteString(hint.Render(ansi.Truncate(it.hint, menuW-4, "…")) + "\n")
		} else {
			b.WriteString(idle.Render(label) + "\n")
		}
	}
	b.WriteString("\n")
	if msg := st.message(inner); msg != "" {
		b.WriteString(msg + "\n")
	}
	b.WriteString(footer(inner, " ", "↑/↓ navigate", "enter select", "q quit") + "\n\n")
	b.WriteString(dimStyle.Render(" " + credit))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

// compactView fits short terminals: a title, the notice, a window of the menu
// around the selection, and the footer.
func (m launcherModel) compactView(st launcherState, width, height int) string {
	var lines []string
	lines = append(lines, ansi.Truncate(headerStyle.Render(" ♪ SONGSMITH"), width, "…"))
	lines = append(lines, st.noticeLines(width)[0])
	items := st.items()
	cur := m.cursor(items)
	visible := max(1, height-len(lines)-1)
	start := min(max(cur-visible/2, 0), max(len(items)-visible, 0))
	for i := start; i < min(start+visible, len(items)); i++ {
		if i == cur {
			bar := lipgloss.NewStyle().Bold(true).Foreground(colorBg).Background(colorAccent).Render(" ▸ " + items[i].label + " ")
			lines = append(lines, ansi.Truncate(bar, width, "…"))
		} else {
			lines = append(lines, ansi.Truncate(dimStyle.Render("   "+items[i].label), width, "…"))
		}
	}
	lines = append(lines, footer(width, "  ", "↑/↓ · enter · q"))
	return strings.Join(lines, "\n")
}

// credit is shown because the MiniMax Music 3 licence asks products built on
// it to credit the model.
const credit = "Music by MiniMax-Music3"

func brandPlate() string {
	word := headerStyle.Render("♪ SONGSMITH")
	tag := subtitleStyle.Italic(true).Render("write · render · listen")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(0, 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, word, tag))
}

type chipTone int

const (
	toneDim chipTone = iota
	toneOK
	toneWarn
	toneBad
)

func chip(name, value string, tone chipTone) string {
	bg := lipgloss.NewStyle().Background(colorRaised)
	fg := map[chipTone]lipgloss.Color{toneDim: colorDim, toneOK: colorStatus, toneWarn: colorWarning, toneBad: colorError}[tone]
	return bg.Render(" ") + bg.Foreground(colorDim).Render(name+" ") + bg.Foreground(fg).Bold(true).Render(value) + bg.Render(" ")
}

// chips lays the status chips out in rows that fit width.
func (st launcherState) chips(width int) string {
	var rows []string
	row := ""
	for _, c := range []struct {
		name string
		get  func() (string, chipTone)
	}{
		{"Writer", st.writerChip},
		{"Music model", st.musicChip},
		{"Server", st.serverChip},
		{"Queue", st.queueChip},
	} {
		v, tone := c.get()
		ch := ansi.Truncate(chip(c.name, v, tone), width, "…")
		if row != "" && ansi.StringWidth(row)+1+ansi.StringWidth(ch) > width {
			rows = append(rows, row)
			row = ""
		}
		if row != "" {
			row += " "
		}
		row += ch
	}
	return strings.Join(append(rows, row), "\n")
}

const checkingLabel = "checking…"

func (st launcherState) writerChip() (string, chipTone) {
	if st.checking {
		return checkingLabel, toneDim
	}
	w, ok := health.Find(st.checks, health.IDWriter)
	switch {
	case !ok:
		return "unknown", toneDim
	case w.Result == health.OK:
		return st.writerModel, toneOK
	case w.Fix == health.FixWriterModel:
		return st.writerModel + " not pulled", toneWarn
	default:
		return "Ollama not reachable", toneWarn
	}
}

func (st launcherState) musicChip() (string, chipTone) {
	if st.checking {
		return checkingLabel, toneDim
	}
	for _, id := range []string{health.IDDiffusionModel, health.IDTextEncoder, health.IDVAE} {
		c, ok := health.Find(st.checks, id)
		if !ok {
			return "unknown", toneDim
		}
		if c.Result != health.OK {
			return "files missing", toneBad
		}
	}
	return "ready", toneOK
}

func (st launcherState) serverChip() (string, chipTone) {
	if st.checking {
		return checkingLabel, toneDim
	}
	if c, ok := health.Find(st.checks, health.IDComfyUI); ok && c.Result == health.Fail {
		return "not installed", toneBad
	}
	c, ok := health.Find(st.checks, health.IDServer)
	switch {
	case !ok:
		return "unknown", toneDim
	case c.Result == health.OK:
		return "running", toneOK
	case c.Result == health.Fail:
		return "CPU only", toneBad
	default:
		return "stopped", toneDim
	}
}

func (st launcherState) queueChip() (string, chipTone) {
	switch {
	case st.rendering != nil:
		v := fmt.Sprintf("rendering %q %s", ansi.Truncate(st.rendering.Title, 24, "…"), progressLabel(st.progress))
		if st.queued > 0 {
			v += fmt.Sprintf(" · %d queued", st.queued)
		}
		return v, toneWarn
	case st.queued > 0 && st.workerNote != "":
		return fmt.Sprintf("%d waiting", st.queued), toneWarn
	case st.queued > 0:
		return fmt.Sprintf("%d queued", st.queued), toneWarn
	default:
		return "idle", toneDim
	}
}

// progressLabel is a percentage when the stage has steps, else the stage.
func progressLabel(p backend.Progress) string {
	switch {
	case p.Max > 0:
		return fmt.Sprintf("%d%%", p.Value*100/p.Max)
	case p.Stage != "":
		return string(p.Stage)
	default:
		return "starting"
	}
}

// noticeLines name what stops rendering and who can fix it: while setup is
// incomplete, everything missing and whether songsmith can install it;
// otherwise the first problem and its fix.
func (st launcherState) noticeLines(width int) []string {
	var lines []string
	switch {
	case !st.checked:
		lines = []string{dimStyle.Render("Checking ComfyUI, the music model files, Ollama and ffprobe…")}
	default:
		b, blocked := st.blocker()
		if !blocked {
			// A stopped server is normal (it starts on the first render) and
			// its chip already says so; only failures and missing pieces count.
			i := slices.IndexFunc(st.checks, func(c health.Check) bool {
				return c.Result == health.Fail || c.Result != health.OK && c.Missing != ""
			})
			if i < 0 {
				lines = []string{statusStyle.Render("✓ Ready to render")}
			} else {
				lines = []string{warningStyle.Render("! " + st.checks[i].Name + ": " + st.checks[i].Detail)}
			}
			break
		}
		if head, fix, ok := missingSummary(st.checks); ok {
			// The fix line wraps: it says who installs what, and none of it may be cut.
			wrapped := lipgloss.NewStyle().Width(max(10, width-2)).Render(fix)
			lines = []string{ansi.Truncate(errorStyle.Render("▲ "+head), width, "…")}
			for _, l := range strings.Split(wrapped, "\n") {
				lines = append(lines, dimStyle.Render("  "+strings.TrimRight(l, " ")))
			}
			return lines
		} else {
			lines = []string{errorStyle.Render("▲ " + b.Name + ": " + b.Detail)}
		}
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return lines
}

// missingSummary lists what is missing and splits it into what Finish setup
// installs and what the user gets elsewhere.
func missingSummary(checks []health.Check) (head, fix string, ok bool) {
	in, out := health.Missing(checks)
	if len(in)+len(out) == 0 {
		return "", "", false
	}
	head = "Missing: " + strings.Join(append(slices.Clone(in), out...), ", ")
	var parts []string
	if len(in) > 0 {
		parts = append(parts, "songsmith can install "+joinAnd(in)+" (Finish setup)")
	}
	if len(out) > 0 {
		parts = append(parts, "get "+joinAnd(out)+" yourself")
	}
	return head, strings.Join(parts, " · "), true
}

func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// message is the latest outcome, such as a queued song or a failed render.
func (st launcherState) message(width int) string {
	switch {
	case st.err != "":
		return ansi.Truncate(errorStyle.Render(st.err), width, "…")
	case st.notice != "":
		return ansi.Truncate(normalStyle.Render(st.notice), width, "…")
	}
	return ""
}
