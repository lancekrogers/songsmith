package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/setup"
)

func newSetupCmd(a *app) *cobra.Command {
	var install, comfyui, models, writer bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Show or install ComfyUI, the MiniMax Music 3 files and the writer model",
		Long: "Without flags, setup lists what is installed and what is missing, and exits non-zero\n" +
			"when something rendering needs is missing. --install installs everything missing, in order.\n" +
			"--comfyui, --models and --writer restrict either mode to those actions.\n" +
			"An interrupted download keeps its .part file and the next run resumes it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			in := setup.New(version)
			only := map[setup.Kind]bool{setup.KindComfyUI: comfyui, setup.KindModel: models, setup.KindWriter: writer}
			if !comfyui && !models && !writer {
				only = map[setup.Kind]bool{setup.KindComfyUI: true, setup.KindModel: true, setup.KindWriter: true}
			}
			out := cmd.OutOrStdout()
			plan := filterPlan(in.Plan(cmd.Context(), cfg), only)
			printPlan(out, plan)
			if install {
				if err := runInstall(cmd.Context(), out, in, cfg, plan); err != nil {
					return err
				}
				plan = filterPlan(in.Plan(cmd.Context(), cfg), only)
				fmt.Fprintln(out)
				printPlan(out, plan)
			}
			if missing := plan.MissingRequired(); len(missing) > 0 {
				if !install {
					fmt.Fprintln(out, "\nRun `songsmith setup --install` to install what is missing.")
				}
				return fmt.Errorf("%d required item(s) missing", len(missing))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&install, "install", false, "install everything missing")
	f.BoolVar(&comfyui, "comfyui", false, "only ComfyUI")
	f.BoolVar(&models, "models", false, "only the MiniMax Music 3 model files")
	f.BoolVar(&writer, "writer", false, "only the Ollama writer model")
	return cmd
}

func filterPlan(p setup.Plan, only map[setup.Kind]bool) setup.Plan {
	var out setup.Plan
	for _, a := range p.Actions {
		if only[a.Kind] {
			out.Actions = append(out.Actions, a)
		}
	}
	return out
}

func printPlan(w io.Writer, p setup.Plan) {
	for _, a := range p.Actions {
		mark := map[setup.Status]string{
			setup.StatusDone: "ok", setup.StatusMissing: "missing", setup.StatusPartial: "partial", setup.StatusUnavailable: "blocked",
		}[a.Status]
		optional := ""
		if !a.Required && a.Pending() {
			optional = " (optional)"
		}
		fmt.Fprintf(w, "  %-8s %-16s %s%s\n", mark, a.Name, a.Detail, optional)
	}
	if n := p.DownloadBytes(); n > 0 {
		fmt.Fprintf(w, "Left to download: about %s\n", setup.GB(n))
	}
}

func runInstall(ctx context.Context, w io.Writer, in *setup.Installer, cfg config.Config, plan setup.Plan) error {
	var failed []string
	for _, a := range plan.Actions {
		if !a.Pending() {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", a.Name)
		if a.Status == setup.StatusUnavailable {
			fmt.Fprintf(w, "  skipped: %s\n", a.Detail)
			if a.Required {
				failed = append(failed, a.Name)
			}
			continue
		}
		line := newProgressLine(w)
		var err error
		switch a.Kind {
		case setup.KindComfyUI:
			logPath := filepath.Join(cfg.StateDir, "install.log")
			fmt.Fprintf(w, "  log: %s\n", logPath)
			err = in.InstallComfyUI(ctx, cfg.ComfyUI.Dir, logPath, line.update)
		case setup.KindModel:
			err = in.Download(ctx, cfg.ComfyUI.Dir, *a.File, line.update)
		case setup.KindWriter:
			err = in.PullWriter(ctx, cfg.Ollama.URL, cfg.Ollama.Model, line.update)
		}
		line.end()
		if ctx.Err() != nil {
			if a.Kind == setup.KindModel {
				fmt.Fprintf(w, "Stopped. The partial download is kept at %s; run `songsmith setup --install` again to resume.\n",
					setup.PartPath(cfg.ComfyUI.Dir, *a.File))
			} else {
				fmt.Fprintln(w, "Stopped. Run `songsmith setup --install` again to continue.")
			}
			return errors.New("setup stopped before it finished")
		}
		if err != nil {
			fmt.Fprintf(w, "  failed: %v\n", err)
			if a.Required {
				failed = append(failed, a.Name)
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not install: %s", strings.Join(failed, ", "))
	}
	return nil
}

// progressLine redraws one terminal line with the latest progress tick.
type progressLine struct {
	w       io.Writer
	started time.Time
	from    int64
	drawn   bool
}

func newProgressLine(w io.Writer) *progressLine { return &progressLine{w: w} }

func (l *progressLine) update(p setup.Progress) {
	switch p.Phase {
	case setup.PhaseStart:
		l.started, l.from = time.Now(), p.Bytes
	case setup.PhaseDone, setup.PhaseSkipped:
		l.end()
		fmt.Fprintf(l.w, "  %s: %s\n", p.Phase, p.Detail)
		return
	}
	var b strings.Builder
	b.WriteString("  ")
	if p.Step != "" {
		b.WriteString(p.Step + ": ")
	}
	if p.Total > 0 {
		fmt.Fprintf(&b, "%3.0f%%  %s / %s", p.Percent, gbNum(p.Bytes), setup.GB(p.Total))
		if p.Phase == setup.PhaseDownload && !l.started.IsZero() {
			if secs := time.Since(l.started).Seconds(); secs > 1 {
				fmt.Fprintf(&b, "  %.1f MB/s", float64(p.Bytes-l.from)/secs/1e6)
			}
		}
	}
	if p.Detail != "" {
		if p.Total > 0 {
			b.WriteString("  ")
		}
		b.WriteString(p.Detail)
	}
	text := b.String()
	if r := []rune(text); len(r) > 100 {
		text = string(r[:99]) + "…"
	}
	fmt.Fprintf(l.w, "\r%s\033[K", text)
	l.drawn = true
}

func (l *progressLine) end() {
	if l.drawn {
		fmt.Fprintln(l.w)
		l.drawn = false
	}
}

func gbNum(n int64) string {
	return strings.TrimSuffix(setup.GB(n), " GB")
}
