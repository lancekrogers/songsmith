// Package tui is songsmith's terminal interface: a launcher that shows what is
// ready, and screens to write, queue, watch and play songs.
package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/player"
	"github.com/lancekrogers/songsmith/internal/queue"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// Drafter drafts a song from an idea.
type Drafter interface {
	Write(ctx context.Context, idea string, opts writer.Options) (writer.Draft, error)
}

// Deps are the services the screens share.
type Deps struct {
	Store   *library.Store
	Queue   *queue.Queue
	Drafter Drafter
	Player  *player.Player
	// Config holds the running settings and the file they were loaded from.
	Config *config.Live
	// Checks runs the setup checks. It is called off the UI goroutine;
	// nil means health.Run.
	Checks func(context.Context, config.Config) []health.Check
	// WorkerNote explains why this process is not rendering, or "" when it is.
	WorkerNote func() string
	// Server is the ComfyUI server Settings starts and stops; nil means a
	// manager for the running config.
	Server ComfyServer
	// Variants lists the known files for a music model slot with their
	// on-disk state; nil means the built-in list.
	Variants func(cfg config.Config, slot string) []ModelVariant
	// Installer installs ComfyUI, the model files and the writer model; nil
	// means nothing can be installed from the TUI.
	Installer Installer
}

type screen int

const (
	screenLauncher screen = iota
	screenLibrary
	screenCompose
	screenSettings
	screenSetup
)

type (
	// switchScreenMsg shows another screen.
	switchScreenMsg screen
	// openComposeMsg opens the song form on song, or on a new idea when nil.
	// The form returns to the screen that opened it.
	// draft starts drafting from the song's idea as the form opens.
	openComposeMsg struct {
		song  *library.Song
		draft bool
	}
	// openLibraryMsg opens the library with songID selected.
	openLibraryMsg struct{ songID string }
	quitMsg        struct{}
	// runHealthMsg starts the setup checks; a newer run supersedes an older one.
	runHealthMsg struct{}

	songsMsg struct {
		songs []library.Song
		err   error
	}
	updateMsg   queue.Update
	playDoneMsg string
	tickMsg     time.Time
)

type app struct {
	d       Deps
	ctx     context.Context
	updates <-chan queue.Update
	events  *bridge

	// height is what the screens draw in: the terminal less the footer note
	// shown while a download runs.
	width, height, termHeight int
	screen                    screen

	health   healthState
	launcher launcherModel
	library  libraryModel
	compose  composeModel
	settings settingsModel
	jobs     jobsState

	// notice and err are the latest outcome, shown on the launcher and library.
	notice, err string
	ticks       int
}

func newApp(ctx context.Context, d Deps, updates <-chan queue.Update) app {
	if d.Checks == nil {
		d.Checks = health.Run
	}
	return app{
		d:        d,
		ctx:      ctx,
		updates:  updates,
		events:   newBridge(bridgeCapacity),
		launcher: newLauncher(),
		library:  newLibrary(),
		compose:  newCompose(),
		jobs:     newJobsState(),
	}
}

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, d Deps) error {
	forceColorProfile()
	updates, stop := d.Queue.Subscribe()
	defer stop()
	defer d.Player.Stop()
	m := newApp(ctx, d, updates)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := p.Run()
	if last, ok := final.(app); ok {
		last.cancelAllJobs()
	}
	m.jobs.waitJobs(15 * time.Second)
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

// Background subscriptions are armed here and re-armed only by the handler
// of their own message, so a key handler never returns a blocking command.
func (a app) Init() tea.Cmd {
	return tea.Batch(
		a.loadSongs(),
		waitUpdate(a.updates),
		waitPlayDone(a.d.Player),
		tick(),
		a.events.wait(a.ctx),
		func() tea.Msg { return runHealthMsg{} },
	)
}

func (a app) loadSongs() tea.Cmd {
	store := a.d.Store
	return func() tea.Msg {
		songs, err := store.List()
		return songsMsg{songs, err}
	}
}

func waitUpdate(ch <-chan queue.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return updateMsg(u)
	}
}

func waitPlayDone(p *player.Player) tea.Cmd {
	return func() tea.Msg { return playDoneMsg(<-p.Done) }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a app) cfg() config.Config { return a.d.Config.Get() }

func (a app) defaultSeconds() float64 {
	return float64(a.cfg().Render.DefaultSeconds)
}

// Update keeps the launcher's selection valid after every message, since
// health results and queue changes add and remove its items.
func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := a.update(msg)
	next.launcher = next.launcher.sync(next.launcherState())
	if next.screen == screenSettings {
		next.syncSettings()
	}
	return next, cmd
}

func (a app) update(msg tea.Msg) (app, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.termHeight = msg.Width, msg.Height
		a.relayout()
		return a, nil
	case bridgeMsg:
		next, cmd := a.update(msg.msg)
		return next, tea.Batch(cmd, a.events.wait(a.ctx))
	case runHealthMsg:
		return a.startHealth()
	case jobProgressMsg:
		return a.receiveJobProgress(msg), nil
	case jobDoneMsg:
		return a.receiveJobDone(msg)
	case healthDoneMsg:
		if msg.seq != a.health.seq {
			return a, nil
		}
		a.health.running = false
		a.health.done = true
		a.health.checks = msg.checks
		if msg.planned {
			a.health.plan, a.health.planned = msg.plan, true
		}
		return a, nil
	case songsMsg:
		a.applySongs(msg)
		return a, nil
	case updateMsg:
		cmds := []tea.Cmd{waitUpdate(a.updates)}
		if msg.Status == library.StatusRendering && msg.Progress.Stage != "" {
			a.library.progress[msg.SongID] = msg.Progress
			a.layoutDetail()
		} else {
			delete(a.library.progress, msg.SongID)
			if msg.Err != "" && msg.Status == library.StatusFailed {
				a.err = "render failed: " + msg.Err
			}
			cmds = append(cmds, a.loadSongs())
		}
		return a, tea.Batch(cmds...)
	case playDoneMsg:
		a.layoutDetail()
		return a, waitPlayDone(a.d.Player)
	case tickMsg:
		a.ticks++
		if a.d.Queue.Current() != "" {
			a.layoutDetail()
		}
		// Other processes can change the library; refresh now and then.
		if a.ticks%5 == 0 {
			return a, tea.Batch(tick(), a.loadSongs())
		}
		return a, tick()
	case switchScreenMsg:
		return a.show(screen(msg))
	case openComposeMsg:
		// A second open queued before the first arrived must not reset the form.
		if a.screen == screenCompose {
			return a, nil
		}
		a.err = ""
		a.compose.returnTo = a.screen
		a.screen = screenCompose
		var models tea.Cmd
		if !a.settings.models.loading {
			models = a.loadModels()
		}
		song := library.Song{}
		if msg.song != nil {
			song = *msg.song
		}
		edit := a.compose.startEdit(song, a.defaultSeconds())
		a.refreshFormSkips()
		if msg.draft {
			var draft tea.Cmd
			a, draft = a.draftKeepingTitle()
			edit = tea.Batch(edit, draft)
		}
		return a, tea.Batch(edit, models)
	case openLibraryMsg:
		a.library.selectID = msg.songID
		a.selectPending()
		return a.show(screenLibrary)
	case quitMsg:
		return a.quit()
	case settingsAsync:
		return a.receiveSettings(msg)
	case tea.KeyMsg:
		// The song form and the text editors handle ctrl+c themselves, so a
		// reflex copy or cancel there never loses work.
		if msg.String() == "ctrl+c" && a.screen != screenCompose && !(a.screen == screenSettings && a.settings.text != nil) {
			return a.quit()
		}
	}

	switch a.screen {
	case screenLauncher:
		return a.updateLauncher(msg)
	case screenLibrary:
		return a.updateLibrary(msg)
	case screenCompose:
		return a.updateCompose(msg)
	case screenSettings:
		return a.updateSettings(msg)
	}
	return a, nil
}

// show switches to s, preparing the screen it enters.
func (a app) show(s screen) (app, tea.Cmd) {
	switch s {
	case screenSettings:
		return a.openSettings(sectionSetup, true)
	case screenSetup:
		return a.openSettings(sectionSetup, false)
	}
	a.screen = s
	if s == screenLibrary {
		a.layoutDetail()
	}
	return a, nil
}

func (a app) quit() (app, tea.Cmd) {
	if a.compose.cancel != nil {
		a.compose.cancel()
	}
	a.cancelAllJobs()
	return a, tea.Quit
}

// back is the screen esc returns to.
func (a app) back() screen {
	switch a.screen {
	case screenCompose:
		return a.compose.returnTo
	default:
		return screenLauncher
	}
}

func (a app) View() string {
	if a.width == 0 {
		return ""
	}
	view := a.screenView()
	if a.height < a.termHeight {
		if pad := a.height - lipgloss.Height(view); pad > 0 {
			view += strings.Repeat("\n", pad)
		}
		view += "\n" + footer(a.width, "  ", downloadNote)
	}
	return view
}

func (a app) screenView() string {
	switch a.screen {
	case screenLibrary:
		return a.libraryView()
	case screenCompose:
		return a.composeView()
	case screenSettings:
		return a.settingsView()
	default:
		return a.launcher.view(a.launcherState(), a.width, a.height)
	}
}

// renderingSong is the song being rendered here or in another process.
func (a app) renderingSong() (library.Song, backend.Progress, bool) {
	for _, s := range a.library.songs {
		if s.Status == library.StatusRendering {
			return s, a.library.progress[s.ID], true
		}
	}
	return library.Song{}, backend.Progress{}, false
}
