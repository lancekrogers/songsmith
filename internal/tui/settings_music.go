package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/config"
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
}

// Music model slots, as Deps.Variants is asked for them: the folders under
// ComfyUI's models folder.
const (
	SlotDiffusion   = "diffusion_models"
	SlotTextEncoder = "text_encoders"
	SlotVAE         = "vae"
)

// builtinVariants are the files in the Comfy-Org/MiniMax-Music-3 repository.
var builtinVariants = map[string][]ModelVariant{
	SlotDiffusion: {
		{File: "minimax_music3_dit_fp16.safetensors", Label: "fp16", Size: 4914197682, Tag: "recommended",
			Note: "The standard diffusion model."},
		{File: "minimax_music3_dit_fp32.safetensors", Label: "fp32", Size: 9828345396,
			Note: "Full precision, twice the size of fp16."},
		{File: "minimax_music3_dit_int8_convrot.safetensors", Label: "int8", Size: 2502161682,
			Note: "Quantized, about half the size of fp16."},
	},
	SlotTextEncoder: {
		{File: "minimax_music3_text_encoder_pruned_bf16.safetensors", Label: "pruned bf16", Size: 16706629398, Tag: "recommended",
			Note: "The standard text encoder."},
		{File: "minimax_music3_text_encoder_pruned_int8_convrot.safetensors", Label: "pruned int8", Size: 9196611886, Tag: "for NVIDIA 16 GB cards",
			Note: "For NVIDIA cards with 16 GB (untested on NVIDIA). On Apple Silicon it ran on the CPU in testing, about 50x slower."},
		{File: "minimax_music3_text_encoder_bf16.safetensors", Label: "full bf16", Size: 18472478038,
			Note: "The unpruned text encoder, larger than the pruned one."},
	},
	SlotVAE: {
		{File: "minimax_music3_dav.safetensors", Label: "dav", Size: 216696128,
			Note: "The audio decoder."},
	},
}

// localVariants lists the built-in files for slot with whether each is in
// cfg's ComfyUI models folder at its full size.
func localVariants(cfg config.Config, slot string) []ModelVariant {
	list := slices.Clone(builtinVariants[slot])
	for i := range list {
		fi, err := os.Stat(filepath.Join(cfg.ComfyUI.Dir, "models", slot, list[i].File))
		list[i].Installed = err == nil && fi.Size() == list[i].Size
	}
	return list
}

func (a *app) refreshVariants() {
	list := a.d.Variants
	if list == nil {
		list = localVariants
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
	rows := []settingRow{
		a.variantRow("Text encoder", "comfyui.text_encoder", SlotTextEncoder, cfg.ComfyUI.TextEncoder),
		a.variantRow("Diffusion model", "comfyui.diffusion_model", SlotDiffusion, cfg.ComfyUI.DiffusionModel),
	}
	vae := settingRow{name: "VAE", kind: rowInfo, value: cfg.ComfyUI.VAE,
		help: "The audio decoder; MiniMax Music 3 has one: " + cfg.ComfyUI.VAE}
	if i := slices.IndexFunc(a.settings.variants[SlotVAE], func(v ModelVariant) bool { return v.File == cfg.ComfyUI.VAE }); i >= 0 {
		v := a.settings.variants[SlotVAE][i]
		vae.value = v.Label + " · " + gigabytes(v.Size) + " · " + installedLabel(v.Installed)
	}
	models := filepath.Join(cfg.ComfyUI.Dir, "models")
	folder := settingRow{name: "Model folder", kind: rowInfo, value: models,
		help: "Where ComfyUI reads the model files, under Settings → Music engine → Install folder: " + models}
	return append(rows, vae, folder)
}

func installedLabel(ok bool) string {
	if ok {
		return "installed"
	}
	return "not on disk"
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
		label := v.Label + " · " + gigabytes(v.Size) + " · " + installedLabel(v.Installed)
		if v.Tag != "" {
			label += " · " + v.Tag
		}
		detail := v.Note
		if !v.Installed {
			detail += " Not on disk: a " + gigabytes(v.Size) + " download, which arrives with in-app setup."
		}
		detail += " " + v.File
		row.options = append(row.options, label)
		row.muted = append(row.muted, !v.Installed)
		row.details = append(row.details, detail)
		if v.File == configured {
			row.current = i
		}
	}
	if row.current < 0 && configured != "" {
		row.options = append(row.options, configured+" · custom")
		row.muted = append(row.muted, false)
		row.details = append(row.details, "A file named in the config file that is not one of the known files; songsmith uses it as named")
		row.current = len(row.options) - 1
	}
	row.set = func(a *app, i int) tea.Cmd {
		if i >= len(vs) || vs[i].File == configured {
			a.settingsNote(name + " is already " + configured)
			return nil
		}
		v := vs[i]
		if !v.Installed {
			a.settingsNote(fmt.Sprintf("%s is not on disk (%s). Downloading it from here arrives with in-app setup; for now see docs/setup.md",
				v.File, gigabytes(v.Size)))
			return nil
		}
		a.saved(key, v.File, name+": "+v.File, appliesNextRender)
		return nil
	}
	return row
}
