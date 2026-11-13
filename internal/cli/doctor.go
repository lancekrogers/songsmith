package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/ollama"
)

type checkResult int

const (
	checkOK checkResult = iota
	checkWarn
	checkFail
)

type check struct {
	name   string
	result checkResult
	detail string
}

var (
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	failStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
)

func newDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check ComfyUI, the MiniMax Music 3 files and Ollama",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			checks := runChecks(ctx, cfg)
			printChecks(cmd.OutOrStdout(), checks)
			for _, c := range checks {
				if c.result == checkFail {
					return errors.New("some checks failed")
				}
			}
			return nil
		},
	}
}

func runChecks(ctx context.Context, cfg config.Config) []check {
	var out []check
	add := func(name string, r checkResult, detail string, args ...any) {
		out = append(out, check{name: name, result: r, detail: fmt.Sprintf(detail, args...)})
	}

	m := server.New(cfg.ComfyUI, cfg.StateDir)
	if _, err := os.Stat(filepath.Join(cfg.ComfyUI.Dir, "main.py")); err != nil {
		add("ComfyUI install", checkFail, "no main.py in %s; set comfyui.dir or install ComfyUI there", cfg.ComfyUI.Dir)
	} else if _, err := os.Stat(m.Python()); err != nil {
		add("ComfyUI install", checkFail, "no venv python at %s", m.Python())
	} else {
		add("ComfyUI install", checkOK, "%s", cfg.ComfyUI.Dir)
	}

	models := []struct{ dir, file string }{
		{"diffusion_models", cfg.ComfyUI.DiffusionModel},
		{"text_encoders", cfg.ComfyUI.TextEncoder},
		{"vae", cfg.ComfyUI.VAE},
	}
	for _, mf := range models {
		p := filepath.Join(cfg.ComfyUI.Dir, "models", mf.dir, mf.file)
		if fi, err := os.Stat(p); err != nil {
			add("Model "+mf.dir, checkFail, "missing %s; download it from Comfy-Org/MiniMax-Music-3", p)
		} else {
			add("Model "+mf.dir, checkOK, "%s (%.1f GB)", mf.file, float64(fi.Size())/1e9)
		}
	}

	st, _ := m.Status(ctx)
	switch {
	case !st.Running:
		add("ComfyUI server", checkWarn, "not running at %s; songsmith starts it on the first render, or run `songsmith server start`", cfg.ComfyUI.BaseURL())
	case st.Device == "mps" && !st.GPUOnly:
		add("ComfyUI server", checkFail, "running without %s, so the MiniMax LLM runs on the CPU (about 50x slower); restart it with songsmith", server.RequiredFlag)
	default:
		add("ComfyUI server", checkOK, "running at %s on %s", cfg.ComfyUI.BaseURL(), st.Device)
	}

	if st.Running {
		out = append(out, nodeChecks(ctx, m.Client(), cfg.ComfyUI)...)
	}

	oc := ollama.NewClient(cfg.Ollama.URL)
	names, err := oc.Models(ctx)
	switch {
	case err != nil:
		add("Ollama", checkWarn, "%v; drafting lyrics needs Ollama, manual lyrics still work", err)
	case !slices.Contains(names, cfg.Ollama.Model):
		add("Ollama", checkWarn, "model %s not installed; run `ollama pull %s` or set ollama.model", cfg.Ollama.Model, cfg.Ollama.Model)
	default:
		add("Ollama", checkOK, "%s at %s", cfg.Ollama.Model, cfg.Ollama.URL)
	}
	return out
}

func nodeChecks(ctx context.Context, c *comfy.Client, models config.ComfyUI) []check {
	var missingNodes, unseenFiles []string
	for _, class := range slices.Sorted(maps.Keys(comfy.RequiredNodes)) {
		info, ok, err := c.ObjectInfo(ctx, class)
		if err != nil {
			return []check{{"ComfyUI nodes", checkFail, err.Error()}}
		}
		if !ok {
			missingNodes = append(missingNodes, class)
			continue
		}
		for _, in := range comfy.RequiredNodes[class] {
			if !info.HasInput(in) {
				missingNodes = append(missingNodes, class+"."+in)
			}
		}
		for input, want := range map[string]string{"unet_name": models.DiffusionModel, "clip_name": models.TextEncoder, "vae_name": models.VAE} {
			if opts := info.ComboOptions(input); opts != nil && !slices.Contains(opts, want) {
				unseenFiles = append(unseenFiles, want)
			}
		}
	}
	var out []check
	if len(missingNodes) > 0 {
		out = append(out, check{"ComfyUI nodes", checkFail, "missing " + strings.Join(missingNodes, ", ") + "; update ComfyUI to a version with MiniMax Music 3 support"})
	} else {
		out = append(out, check{"ComfyUI nodes", checkOK, "MiniMax Music 3 nodes available"})
	}
	if len(unseenFiles) > 0 {
		out = append(out, check{"ComfyUI model list", checkFail, "ComfyUI cannot see " + strings.Join(unseenFiles, ", ") + "; download the file, then restart ComfyUI"})
	}
	return out
}

func printChecks(w io.Writer, checks []check) {
	for _, c := range checks {
		label := okStyle.Render("OK  ")
		switch c.result {
		case checkWarn:
			label = warnStyle.Render("WARN")
		case checkFail:
			label = failStyle.Render("FAIL")
		}
		fmt.Fprintf(w, "%s %-24s %s\n", label, c.name, c.detail)
	}
}
