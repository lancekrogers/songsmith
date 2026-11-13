package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/setup"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// settingsLoadTimeout bounds a background read Settings shows the result of.
const settingsLoadTimeout = 5 * time.Second

const ollamaDownload = "https://ollama.com/download"

type modelsState struct {
	// seq identifies the newest load; older results are dropped.
	seq     int
	loading bool
	loaded  bool
	names   []string
	err     error
	// list replaces the Ollama client in tests.
	list func(ctx context.Context, url string) ([]string, error)
}

type modelsLoadedMsg struct {
	seq   int
	names []string
	err   error
}

func (modelsLoadedMsg) settingsAsync() {}

// loadModels asks Ollama which models are installed.
func (a *app) loadModels() tea.Cmd {
	m := &a.settings.models
	m.seq++
	m.loading = true
	list := m.list
	if list == nil {
		list = func(ctx context.Context, url string) ([]string, error) { return ollama.NewClient(url).Models(ctx) }
	}
	seq, url, ctx := m.seq, a.cfg().Ollama.URL, a.ctx
	return a.events.run(func(send func(tea.Msg)) {
		ctx, cancel := context.WithTimeout(ctx, settingsLoadTimeout)
		defer cancel()
		names, err := list(ctx, url)
		send(modelsLoadedMsg{seq: seq, names: names, err: err})
	})
}

func (a *app) receiveModels(msg modelsLoadedMsg) {
	m := &a.settings.models
	if msg.seq != m.seq {
		return
	}
	m.loading, m.loaded = false, true
	m.names, m.err = slices.Sorted(slices.Values(msg.names)), msg.err
}

func (a app) writerRows() []settingRow {
	cfg := a.cfg()
	width := a.settingsContentWidth() - 4
	rows := []settingRow{a.writerModelRow()}
	if a.settings.models.err != nil {
		text := fmt.Sprintf("Ollama is not reachable at %s, so its models cannot be listed (configured: %s). "+
			"Get Ollama from %s; lyrics can still be written by hand.", cfg.Ollama.URL, cfg.Ollama.Model, ollamaDownload)
		rows = reportRows("Model", text, width)
	}
	rows = append(rows, a.pullRows(width)...)

	instructions := "built-in"
	if cfg.Ollama.InstructionsFile != "" {
		instructions = cfg.Ollama.InstructionsFile
	}
	return append(rows,
		settingRow{
			key: "ollama.model", name: "Other model", kind: rowText, value: dimStyle.Render("a tag not in the list"),
			applies: appliesNextDraft,
			help:    "Type any Ollama model tag, such as name:tag. One Ollama does not have is pulled first, and used once the pull finishes",
			run: func(a *app) tea.Cmd {
				return a.beginEdit("Other model", "", (*app).commitOtherModel)
			},
		},
		settingRow{
			key: "ollama.url", name: "Ollama URL", kind: rowText, value: cfg.Ollama.URL,
			applies: appliesNextDraft,
			help:    "Where the Ollama server listens",
			run: func(a *app) tea.Cmd {
				return a.beginEdit("Ollama URL", a.cfg().Ollama.URL, (*app).commitOllamaURL)
			},
		},
		settingRow{
			key: "ollama.temperature", name: "Temperature", kind: rowSlider,
			value:   fmt.Sprintf("%.1f", cfg.Ollama.Temperature),
			applies: appliesNextDraft,
			help:    "How adventurous drafts are, from 0.0 (predictable) to 1.5 (loose)",
			nudge:   (*app).nudgeTemperature,
		},
		settingRow{
			key: "ollama.instructions_file", name: "Writing instructions", kind: rowAction, verb: "edit",
			value: instructions, applies: appliesNextDraft,
			help: "The system prompt the writer drafts with. The built-in one is copied to a file you can edit",
			run:  (*app).openInstructions,
		},
		settingRow{
			key: "ollama.instructions_file", name: "Reset instructions", kind: rowAction, verb: "reset",
			value: "use the built-in instructions", applies: appliesNextDraft,
			help: "Drafts go back to the built-in prompt; your instructions file is kept",
			run:  (*app).resetInstructions,
		},
	)
}

// writerModelRow lists the installed models with ✓ on the configured one. A
// configured model Ollama does not have is listed dim as not pulled.
func (a app) writerModelRow() settingRow {
	model := a.cfg().Ollama.Model
	m := a.settings.models
	row := settingRow{key: "ollama.model", name: "Model", kind: rowPick, applies: appliesNextDraft,
		help: "The Ollama model that drafts lyrics and the style"}
	if !m.loaded {
		row.kind, row.value = rowInfo, "loading installed models…"
		return row
	}
	names := m.names
	row.options = slices.Clone(names)
	row.muted = make([]bool, len(names))
	row.current = slices.IndexFunc(names, func(n string) bool { return setup.ModelTag(n) == setup.ModelTag(model) })
	if m.loading {
		row.detail = "refreshing…"
	}
	if row.current < 0 && model != "" {
		state := "not pulled"
		if j, ok := a.job(writerTarget(a.cfg(), model).key()); ok {
			state = jobStatus(j)
		}
		row.options = append(row.options, model+" · "+state)
		row.muted = append(row.muted, true)
		row.details = make([]string, len(row.options))
		row.details[len(names)] = notPulled(model)
		row.current = len(names)
	}
	if len(row.options) == 0 {
		row.kind, row.value = rowInfo, "Ollama has no models installed"
		return row
	}
	row.set = func(a *app, i int) tea.Cmd {
		if i >= len(names) {
			return a.pullWriter(model)
		}
		if names[i] == a.cfg().Ollama.Model {
			a.settingsNote(names[i] + " is already the writer model")
			return nil
		}
		a.saved("ollama.model", names[i], "Writer model: "+names[i], appliesNextDraft)
		return nil
	}
	return row
}

func notPulled(model string) string {
	return fmt.Sprintf("%s is not pulled into Ollama yet: enter pulls it, then drafts use it", model)
}

// pullWriter pulls model through Ollama and makes it the writer model once
// the pull has finished; a failed or cancelled pull changes nothing.
func (a *app) pullWriter(model string) tea.Cmd {
	t := writerTarget(a.cfg(), model)
	if j, ok := a.job(t.key()); ok {
		a.settingsNote(model + " is already " + jobStatus(j))
		return nil
	}
	if a.d.Installer == nil {
		a.settingsError(model + " cannot be pulled from here")
		return nil
	}
	a.settingsNote("Pulling " + model + " through Ollama; drafts use it once the pull finishes")
	return a.startJob(t, func(a *app, err error) {
		if err != nil || setup.ModelTag(a.cfg().Ollama.Model) == setup.ModelTag(model) {
			return
		}
		a.saved("ollama.model", model, "Writer model: "+model, appliesNextDraft)
	})
}

// pullRows show each running pull with its Cancel row, and the full error
// of a pull that failed.
func (a app) pullRows(width int) []settingRow {
	var rows []settingRow
	for _, j := range a.runningJobs() {
		if j.target.kind != setup.KindWriter {
			continue
		}
		rows = append(rows, settingRow{name: "Pulling " + j.target.model, kind: rowInfo,
			value: jobStatus(j), detail: jobDetail(j), help: "Ollama is pulling this model; it keeps going while you use the rest of songsmith"},
			a.cancelRow(j))
	}
	for _, key := range slices.Sorted(maps.Keys(a.jobs.failed)) {
		if tag, ok := strings.CutPrefix(key, "writer:"); ok {
			rows = append(rows, a.failureRows(writerTarget(a.cfg(), tag), width)...)
		}
	}
	return rows
}

func (a *app) commitOtherModel(tag string) (tea.Cmd, error) {
	if tag == "" || strings.ContainsAny(tag, " \t") {
		return nil, errors.New("Type one model tag, such as name:tag")
	}
	m := a.settings.models
	pulled := slices.ContainsFunc(m.names, func(n string) bool { return setup.ModelTag(n) == setup.ModelTag(tag) })
	if m.loaded && m.err == nil && !pulled {
		return a.pullWriter(tag), nil
	}
	if !a.saveSetting("ollama.model", tag) {
		return nil, nil
	}
	if m.err != nil {
		a.settingsNote(fmt.Sprintf("%s · Writer model: %s · Ollama is not reachable, so it was not checked or pulled", capitalize(appliesNextDraft), tag))
		return nil, nil
	}
	a.settingsNote(capitalize(appliesNextDraft) + " · Writer model: " + tag)
	return nil, nil
}

func (a *app) commitOllamaURL(raw string) (tea.Cmd, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("Type a URL such as http://127.0.0.1:11434")
	}
	if !a.saved("ollama.url", raw, "Ollama URL: "+raw, appliesNextDraft) {
		return nil, nil
	}
	return a.loadModels(), nil
}

// nudgeTemperature moves the temperature in tenths between 0.0 and 1.5.
func (a *app) nudgeTemperature(delta int) tea.Cmd {
	cur := int(math.Round(a.cfg().Ollama.Temperature * 10))
	next := min(max(cur+delta, 0), 15)
	if next == cur {
		a.settingsNote(fmt.Sprintf("Temperature is at its limit, %.1f", float64(cur)/10))
		return nil
	}
	v := float64(next) / 10
	a.saved("ollama.temperature", v, fmt.Sprintf("Temperature: %.1f", v), appliesNextDraft)
	return nil
}

// openInstructions edits the writing instructions file. With none set, the
// built-in prompt is copied into a new file beside the config file first.
func (a *app) openInstructions() tea.Cmd {
	path := a.cfg().Ollama.InstructionsFile
	if path == "" {
		p, err := newInstructionsFile(filepath.Dir(a.d.Config.Path()), writer.BuiltinInstructions)
		if err != nil {
			a.settingsError("Could not write the instructions file: " + err.Error())
			return nil
		}
		if !a.saveSetting("ollama.instructions_file", p) {
			return nil
		}
		path = p
	}
	return a.editInstructions(path)
}

// editInstructions opens the instructions file at path in an editor and
// reports back with instructionsEditedMsg. It is the one place that chooses
// the editor.
func (a *app) editInstructions(path string) tea.Cmd {
	return tea.ExecProcess(editorCommand(path), func(err error) tea.Msg {
		return instructionsEditedMsg{path: path, err: err}
	})
}

type instructionsEditedMsg struct {
	path string
	err  error
}

func (instructionsEditedMsg) settingsAsync() {}

func (a *app) receiveInstructions(msg instructionsEditedMsg) {
	if msg.err != nil {
		a.settingsError("The editor failed: " + msg.err.Error())
		return
	}
	if _, err := writer.Instructions(msg.path); err != nil {
		a.settingsError(err.Error() + "; drafts use the built-in instructions until it has text")
		return
	}
	a.settingsNote(capitalize(appliesNextDraft) + " · Writing instructions saved in " + msg.path)
}

// newInstructionsFile writes text to instructions.txt in dir, or to the
// first free instructions-N.txt, so an earlier file is never overwritten.
func newInstructionsFile(dir, text string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for n := 1; n < 100; n++ {
		name := "instructions.txt"
		if n > 1 {
			name = fmt.Sprintf("instructions-%d.txt", n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.WriteString(text); err != nil {
			f.Close()
			return "", err
		}
		return path, f.Close()
	}
	return "", fmt.Errorf("no free instructions file name in %s", dir)
}

func (a *app) resetInstructions() tea.Cmd {
	path := a.cfg().Ollama.InstructionsFile
	if path == "" {
		a.settingsNote("Drafts already use the built-in instructions")
		return nil
	}
	a.saved("ollama.instructions_file", "", "Writing instructions: built-in (your file stays at "+path+")", appliesNextDraft)
	return nil
}
