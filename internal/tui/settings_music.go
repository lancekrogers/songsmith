package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/setup"
)

// ModelVariant is one known file for a music model slot.
type ModelVariant struct {
	File  string
	Label string
	Size  int64
	// Tag is a few words drawn after the state, such as "recommended".
	Tag string
	// Note is what a user should know before choosing this file.
	Note string
	// Installed is true when the file is on disk at its full size.
	Installed bool
	// Partial is how much of an unfinished download is on disk, in percent.
	Partial float64
}

// Music model slots, as Deps.Variants is asked for them: the folders under
// ComfyUI's models folder.
const (
	SlotDiffusion   = setup.DirDiffusion
	SlotTextEncoder = setup.DirTextEncoder
	SlotVAE         = setup.DirVAE
)

// manifestVariants lists the files songsmith can download for slot, with
// their state in cfg's ComfyUI models folder.
func manifestVariants(cfg config.Config, slot string) []ModelVariant {
	var out []ModelVariant
	for _, f := range setup.Variants(slot) {
		st := setup.State(cfg.ComfyUI.Dir, f)
		v := ModelVariant{File: f.Name, Label: f.Label, Size: f.Size, Tag: f.Tag, Note: f.Note,
			Installed: st.Status == setup.FileInstalled}
		if f.Recommended && v.Tag == "" {
			v.Tag = "recommended"
		}
		if st.Status == setup.FilePartial {
			v.Partial = st.Percent()
		}
		out = append(out, v)
	}
	return out
}

func (a *app) refreshVariants() {
	list := a.d.Variants
	if list == nil {
		list = manifestVariants
	}
	cfg := a.cfg()
	a.settings.variants = map[string][]ModelVariant{}
	for _, slot := range []string{SlotDiffusion, SlotTextEncoder, SlotVAE} {
		a.settings.variants[slot] = list(cfg, slot)
	}
}

func gigabytes(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

func (a app) musicModelRows() []settingRow {
	cfg := a.cfg()
	width := a.settingsContentWidth() - rowIndent
	var rows []settingRow
	rows = append(rows, a.variantRow("Text encoder", "comfyui.text_encoder", SlotTextEncoder, cfg.ComfyUI.TextEncoder))
	rows = append(rows, a.slotJobRows(SlotTextEncoder, width)...)
	rows = append(rows, a.variantRow("Diffusion model", "comfyui.diffusion_model", SlotDiffusion, cfg.ComfyUI.DiffusionModel))
	rows = append(rows, a.slotJobRows(SlotDiffusion, width)...)
	vae := "VAE: " + cfg.ComfyUI.VAE
	if i := slices.IndexFunc(a.settings.variants[SlotVAE], func(v ModelVariant) bool { return v.File == cfg.ComfyUI.VAE }); i >= 0 {
		v := a.settings.variants[SlotVAE][i]
		vae = "VAE · " + gigabytes(v.Size) + " · " + a.variantState(SlotVAE, v)
	}
	models := filepath.Join(cfg.ComfyUI.Dir, "models")
	rows = append(rows, settingRow{name: "VAE", kind: rowNote, gap: true, value: vae + " · files in " + shortPath(models)})
	return append(rows, a.slotJobRows(SlotVAE, width)...)
}

// variantState is installed, downloading, partly downloaded or not on disk.
func (a app) variantState(slot string, v ModelVariant) string {
	if j, ok := a.job(variantKey(slot, v.File)); ok {
		return jobStatus(j)
	}
	switch {
	case v.Installed:
		return "installed"
	case v.Partial > 0:
		return fmt.Sprintf("%.0f%% downloaded", v.Partial)
	}
	return "not on disk"
}

func variantKey(slot, file string) string {
	return installTarget{kind: setup.KindModel, file: setup.ModelFile{Dir: slot, Name: file}}.key()
}

// slotJobRows are the Cancel rows of slot's running downloads and the full
// error of any that failed.
func (a app) slotJobRows(slot string, width int) []settingRow {
	var rows []settingRow
	for _, j := range a.runningJobs() {
		if j.target.kind == setup.KindModel && j.target.file.Dir == slot {
			rows = append(rows, a.cancelRow(j))
		}
	}
	for _, f := range setup.Variants(slot) {
		rows = append(rows, a.failureRows(modelTarget(a.cfg(), f), width)...)
	}
	return rows
}

// variantRow lists slot's known files with size and state, ✓ on the one in
// use. A file that is not on disk is dim: choosing it would need a download,
// so it changes nothing. A configured file that is not a known one is listed
// as custom.
func (a app) variantRow(name, key, slot, configured string) settingRow {
	vs := a.settings.variants[slot]
	row := settingRow{key: key, name: name, kind: rowPick, current: -1, applies: appliesNextRender,
		help: "Which file ComfyUI loads for the " + name}
	for i, v := range vs {
		label := shortLabel(name, v.Label)
		option := label + " · " + gigabytes(v.Size) + " · " + a.variantState(slot, v)
		if v.Tag != "" {
			option += " · " + v.Tag
		}
		detail := v.Note
		if !v.Installed {
			detail += " Not on disk: enter downloads it (" + gigabytes(v.Size) + ") and switches to it once it is verified."
		}
		row.options = append(row.options, option)
		row.muted = append(row.muted, !v.Installed)
		row.details = append(row.details, strings.TrimSpace(detail))
		row.optionDescs = append(row.optionDescs, v.File)
		if v.File == configured {
			row.current = i
			row.value = label
		}
	}
	if row.current < 0 && configured != "" {
		row.options = append(row.options, configured+" · custom")
		row.muted = append(row.muted, false)
		row.details = append(row.details, "A file named in the config file that is not one of the known files; songsmith uses it as named")
		row.optionDescs = append(row.optionDescs, configured)
		row.current = len(row.options) - 1
		row.value = "custom"
	}
	row.set = func(a *app, i int) tea.Cmd {
		if i >= len(vs) || vs[i].File == configured {
			a.settingsNote(name + " is already " + configured)
			return nil
		}
		v := vs[i]
		if !v.Installed {
			return a.downloadVariant(name, key, slot, v)
		}
		a.saved(key, v.File, name+": "+v.File, appliesNextRender)
		return nil
	}
	return row
}

// shortLabel drops the slot's own name from a variant's label, so under the
// "Text encoder" heading "Text encoder bf16 (pruned)" reads "bf16 (pruned)".
func shortLabel(slot, label string) string {
	for _, p := range []string{slot + " ", "DiT "} {
		if rest, ok := strings.CutPrefix(label, p); ok && rest != "" {
			return rest
		}
	}
	return label
}

// downloadVariant fetches a file that is not on disk and, once it is
// installed and verified, makes it the one in use. Nothing is saved before.
func (a *app) downloadVariant(name, key, slot string, v ModelVariant) tea.Cmd {
	if j, ok := a.job(variantKey(slot, v.File)); ok {
		a.settingsNote(v.File + " is already " + jobStatus(j))
		return nil
	}
	f, ok := setup.Lookup(slot, v.File)
	if !ok || a.d.Installer == nil {
		a.settingsError(v.File + " cannot be downloaded from here")
		return nil
	}
	cfg := a.cfg()
	if !setup.InspectComfyUI(cfg.ComfyUI.Dir).HasCheckout {
		a.settingsError("ComfyUI is not installed yet, and the file goes in its folder: install ComfyUI in Settings → Setup first")
		return nil
	}
	a.settingsNote(fmt.Sprintf("Downloading %s (%s); %s switches to it once it is verified", v.File, gigabytes(v.Size), name))
	return a.startJob(modelTarget(cfg, f), func(a *app, err error) {
		if err != nil || setup.State(cfg.ComfyUI.Dir, f).Status != setup.FileInstalled {
			return
		}
		a.saved(key, v.File, name+": "+v.File, appliesNextRender)
	})
}
