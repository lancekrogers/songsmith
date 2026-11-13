package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/health"
	"github.com/lancekrogers/songsmith/internal/ollama"
)

// manyModels is an Ollama library of n made-up models: the test writer
// model, two embedding models and one whose capabilities say it cannot chat.
func manyModels(n int) []ollama.ModelInfo {
	out := []ollama.ModelInfo{
		{Name: testModel, Family: "llama", ParameterSize: "7B", Quantization: "Q4_K_M"},
		{Name: "textvector-embed:latest", Family: "nomic-bert", ParameterSize: "137M", Quantization: "F16"},
		{Name: "tinyvec:22m", Family: "bert", Families: []string{"bert"}, ParameterSize: "22M"},
		{Name: "retriever:large", Family: "gemma", Capabilities: []string{"embedding"}},
	}
	words := []string{"ballad", "bard", "chorus", "ditty", "fable", "hymn", "lyric", "minstrel", "muse", "ode", "poet", "rhyme", "sonnet", "verse"}
	for i := 0; len(out) < n; i++ {
		out = append(out, ollama.ModelInfo{Name: fmt.Sprintf("%s:%db", words[i%len(words)], 3+i), Family: "qwen",
			ParameterSize: fmt.Sprintf("%dB", 3+i), Quantization: "Q4_K_M", Capabilities: []string{"completion"}})
	}
	return out
}

// manySamplers is a ComfyUI whose KSampler offers n samplers, the first
// "euler", and nine schedulers.
func manySamplers(n int) func(context.Context, string) (comfy.NodeInfo, bool, error) {
	samplers := []string{"euler"}
	for i := 1; len(samplers) < n; i++ {
		samplers = append(samplers, fmt.Sprintf("sampler_%02d", i))
	}
	schedulers := []string{"simple", "sgm_uniform", "karras", "exponential", "ddim_uniform", "beta", "normal", "linear_quadratic", "kl_optimal"}
	return func(context.Context, string) (comfy.NodeInfo, bool, error) {
		var info comfy.NodeInfo
		info.Input.Required = map[string]json.RawMessage{}
		for name, opts := range map[string][]string{"sampler_name": samplers, "scheduler": schedulers} {
			raw, _ := json.Marshal([]any{opts})
			info.Input.Required[name] = raw
		}
		return info, true, nil
	}
}

func readyWithFFprobe() []health.Check {
	return append(readyChecks(), health.Check{ID: health.IDFFprobe, Name: "ffprobe", Result: health.OK, Detail: "found"})
}

// busyApp is Settings on a machine with 34 models and 45 samplers.
func busyApp(t *testing.T, w, h int) app {
	t.Helper()
	a, srv := newSettingsApp(t, readyWithFFprobe())
	a.settings.models.list = fakeModelInfos(false, manyModels(34)...)
	srv.objectInfo = manySamplers(45)
	a, _ = send(t, a, tea.WindowSizeMsg{Width: w, Height: h})
	return a
}

func TestEverySectionFitsIn80x24WithALargeLibrary(t *testing.T) {
	a := busyApp(t, 80, 24)
	for sec := range settingsSectionCount {
		a = openSettingsOn(t, a, sec)
		rows, lines := a.settingsLayout()
		for i, l := range lines {
			if !l.focusable() {
				continue
			}
			a.placeSettingsCursor(rows, lines, i)
			disp, _ := a.settingsDisplay(rows, lines)
			if len(disp) > a.settingsBodyRows() {
				t.Fatalf("%s with the cursor on line %d draws %d lines in %d:\n%s", sectionNames[sec], i, len(disp), a.settingsBodyRows(),
					ansi.Strip(strings.Join(sectionText(a), "\n")))
			}
		}
	}
}

func TestNoSectionHasAListOfOne(t *testing.T) {
	a := busyApp(t, 80, 24)
	a.settings.models.list = fakeModels(testModel)
	a.d.Server.(*fakeServer).objectInfo = manySamplers(1)
	for sec := range settingsSectionCount {
		a = openSettingsOn(t, a, sec)
		for _, r := range a.sectionRows() {
			if r.kind == rowPick && len(r.options) == 1 && r.expands() {
				t.Errorf("%s: %q is a list of one", sectionNames[sec], r.name)
			}
		}
		for _, l := range sectionText(a) {
			if strings.Contains(ansi.Strip(l), "Engine ·") {
				t.Errorf("%s draws the one-engine list", sectionNames[sec])
			}
		}
	}
	a = openSettingsOn(t, a, sectionWriter)
	if !strings.Contains(plain(a), "Model         "+testModel) {
		t.Fatalf("one model is not a plain row:\n%s", plain(a))
	}
}

func TestALongListIsWindowedWithItsHeadingAndCount(t *testing.T) {
	a := busyApp(t, 80, 24)
	a = openSettingsOn(t, a, sectionWriter)
	view := plain(a)
	if name, opt := focusedName(a); name != "Model" || opt != slices.Index(a.writerModelRows()[0].options, testModel) {
		t.Fatalf("first visit on %q %d, want the current model", name, opt)
	}
	for _, s := range []string{"Model · " + testModel + " · 31 installed", "▸ ✓ " + testModel, "7B · Q4_K_M", "↓ ", "3 hidden: not chat models"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Writer missing %q:\n%s", s, view)
		}
	}
	for _, s := range []string{"textvector-embed", "tinyvec", "retriever"} {
		if strings.Contains(view, s) {
			t.Fatalf("embedding model %q listed:\n%s", s, view)
		}
	}
	rows, lines := a.settingsLayout()
	w := a.listWindows(rows, lines)[lines[a.settings.cursor].row]
	if w.count >= len(w.opts) || w.count < 6 || !w.up && !w.down {
		t.Fatalf("window %+v of %d", w, len(w.opts))
	}
	before := len(sectionText(a))
	for range 20 {
		a = pressSettings(t, a, "up")
		if n := len(sectionText(a)); n != before {
			t.Fatalf("the section changed height from %d to %d as the window moved", before, n)
		}
	}
	view = plain(a)
	if !strings.Contains(view, "Model · "+testModel+" · 31 installed") || !strings.Contains(view, "↑ ") || !strings.Contains(view, "↓ ") {
		t.Fatalf("after moving down the list:\n%s", view)
	}
	_, opt := focusedName(a)
	if opt < 0 || !strings.Contains(view, a.writerModelRows()[0].options[opt]) {
		t.Fatalf("the window did not follow the cursor to option %d:\n%s", opt, view)
	}
}

func TestFilterNarrowsAListAndOwnsTheKeys(t *testing.T) {
	a := busyApp(t, 80, 24)
	a = openSettingsOn(t, a, sectionWriter)
	if !strings.Contains(footerLine(a), "/ filter") {
		t.Fatalf("footer %q", footerLine(a))
	}
	saves := recordSaves(&a)
	a = pressSettings(t, a, "/")
	if !a.settings.filter.on || !strings.Contains(footerLine(a), "esc clear") {
		t.Fatalf("no filter: %q", footerLine(a))
	}
	a = typeIntoSettings(t, a, "ODE")
	view := plain(a)
	if a.settings.section != sectionWriter || !strings.Contains(view, "/ ODE") || !strings.Contains(view, "ode:12b") || strings.Contains(view, "ballad") {
		t.Fatalf("filtered:\n%s", view)
	}
	a = typeIntoSettings(t, a, "3l")
	if a.settings.section != sectionWriter || !strings.Contains(plain(a), "nothing matches") {
		t.Fatalf("digits or letters left the filter:\n%s", plain(a))
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 0 || !a.settings.filter.on {
		t.Fatalf("enter with no match saved %v", *saves)
	}
	a = pressSettings(t, a, "backspace", "backspace")
	name, opt := focusedName(a)
	want := a.writerModelRows()[0].options[opt]
	if name != "Model" || !strings.Contains(strings.ToLower(want), "ode") {
		t.Fatalf("cursor on %q %d (%s)", name, opt, want)
	}
	a = pressSettings(t, a, "enter")
	if len(*saves) != 1 || (*saves)[0] != (savedKV{"ollama.model", want}) || a.settings.filter.on {
		t.Fatalf("enter in the filter: saves %v filter %+v", *saves, a.settings.filter)
	}
	if name, o := focusedName(a); name != "Model" || o != opt || !strings.Contains(plain(a), "✓ "+want) {
		t.Fatalf("after choosing: on %q %d\n%s", name, o, plain(a))
	}

	a = pressSettings(t, a, "/")
	a = typeIntoSettings(t, a, "bard")
	a = pressSettings(t, a, "esc")
	if a.settings.filter.on || a.screen != screenSettings || !strings.Contains(plain(a), "ballad") {
		t.Fatalf("esc did not clear the filter:\n%s", plain(a))
	}
}

func TestFilterIsOnlyForLongLists(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionWriter)
	if strings.Contains(footerLine(a), "/ filter") {
		t.Fatalf("short list offers a filter: %q", footerLine(a))
	}
	a = pressSettings(t, a, "/")
	if a.settings.filter.on {
		t.Fatal("a short list took a filter")
	}
}

func TestModelsThatCannotChatAreHiddenAndUnknownOnesKept(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	a.settings.models.list = fakeModelInfos(false,
		ollama.ModelInfo{Name: testModel},
		ollama.ModelInfo{Name: "plain:1b"},
		ollama.ModelInfo{Name: "vector-embed:v1", Family: "nomic-bert"},
		ollama.ModelInfo{Name: "doc-search:1b", Capabilities: []string{"embedding"}},
		ollama.ModelInfo{Name: "talker:2b", Capabilities: []string{"completion", "tools"}})
	a = openSettingsOn(t, a, sectionWriter)
	row := a.writerModelRows()[0]
	if !slices.Equal(row.options, []string{"plain:1b", testModel, "talker:2b"}) {
		t.Fatalf("options %q", row.options)
	}
	if !strings.Contains(plain(a), "2 hidden: not chat models") {
		t.Fatalf("no hidden note:\n%s", plain(a))
	}
	a.settings.models.list = nil
	saves := recordSaves(&a)
	a = commitText(t, a, "Other model", "vector-embed:v1")
	if len(*saves) != 1 || (*saves)[0].value != "vector-embed:v1" {
		t.Fatalf("a hidden model that is installed was not saved as typed: %v %q", *saves, a.settings.message)
	}
	if !strings.Contains(plain(a), "✓ vector-embed:v1") {
		t.Fatalf("the configured model is hidden:\n%s", plain(a))
	}
}

func TestSlidersShowWhereTheyAre(t *testing.T) {
	for _, c := range []struct {
		at   float64
		want string
	}{
		{10, "10   ●━━━━━━━━━━━━  10–60"},
		{35, "35   ━━━━━━●━━━━━━  10–60"},
		{60, "60   ━━━━━━━━━━━━●  10–60"},
	} {
		row := settingRow{kind: rowSlider, value: fmt.Sprint(c.at), lo: 10, hi: 60, at: c.at, scale: "10–60"}
		if got := ansi.Strip(sliderValue(row)); got != c.want {
			t.Errorf("at %v: %q, want %q", c.at, got, c.want)
		}
	}
	a, _ := newSettingsApp(t, readyChecks())
	a = openSettingsOn(t, a, sectionRender)
	if !strings.Contains(plain(a), "Steps         30   ━━━━━●━━━━━━━  10–60") {
		t.Fatalf("Render:\n%s", plain(a))
	}
}

func TestTheFocusedRowSaysWhatItIsForUnderIt(t *testing.T) {
	a, _ := newSettingsApp(t, readyChecks())
	for _, c := range []struct {
		sec        settingsSection
		name, desc string
	}{
		{sectionRender, "Steps", "More steps render slower"},
		{sectionWriter, "Temperature", "Higher drafts are more adventurous"},
		{sectionLibrary, "State folder", "Where songsmith keeps logs"},
	} {
		a = openSettingsOn(t, a, c.sec)
		a = focus(t, a, c.name, -1)
		text := sectionText(a)
		_, at := a.settingsDisplay(a.settingsLayout())
		if at+1 >= len(text) || !strings.Contains(text[at+1], c.desc) {
			t.Errorf("%s: no %q under the row:\n%s", c.name, c.desc, strings.Join(text, "\n"))
		}
	}
	a = openSettingsOn(t, a, sectionWriter)
	a = focus(t, a, "Model", 0)
	if !strings.Contains(plain(a), "enter chooses this model for drafts") {
		t.Fatalf("Writer model:\n%s", plain(a))
	}
}

func TestAdvancedListsSamplersOrSaysToStartTheServer(t *testing.T) {
	a := busyApp(t, 80, 24)
	a = openSettingsOn(t, a, sectionAdvanced)
	view := plain(a)
	for _, s := range []string{"Sampler · euler · 45 on this server", "Scheduler · simple", "Connections", "Ollama URL", "ComfyUI host", "ComfyUI port"} {
		if !strings.Contains(view, s) {
			t.Fatalf("Advanced missing %q:\n%s", s, view)
		}
	}
	b, _ := newSettingsApp(t, readyChecks())
	b = openSettingsOn(t, b, sectionAdvanced)
	if !strings.Contains(plain(b), "start the server in Settings → Server to list the choices") {
		t.Fatalf("no-server Advanced:\n%s", plain(b))
	}
}
