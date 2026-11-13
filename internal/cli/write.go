package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/writer"
)

type writeFlags struct {
	model        string
	seconds      float64
	instrumental bool
}

func (f *writeFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.model, "model", "", "Ollama model (default from config)")
	cmd.Flags().Float64Var(&f.seconds, "seconds", 0, "target length in seconds (default: model decides)")
	cmd.Flags().BoolVar(&f.instrumental, "instrumental", false, "write an instrumental with no sung lines")
}

// writer drafts with cfg's writer settings, warning on stderr when the
// instructions file cannot be used.
func (f *writeFlags) writer(cmd *cobra.Command, cfg config.Config) *writer.Writer {
	w, err := newWriter(cfg, f.model)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; using the built-in instructions\n", err)
	}
	return w
}

// newWriter builds a writer from cfg, with model overriding the configured
// one when set. The error reports an unusable instructions file; the writer
// is still usable and falls back to the built-in instructions.
func newWriter(cfg config.Config, model string) (*writer.Writer, error) {
	prompt, err := writer.Instructions(cfg.Ollama.InstructionsFile)
	w := writer.New(ollama.NewClient(cfg.Ollama.URL), cmp.Or(model, cfg.Ollama.Model)).
		WithTemperature(cfg.Ollama.Temperature).
		WithContext(cfg.Ollama.Context).
		WithKeepAlive(cfg.Ollama.KeepAlive).
		WithInstructions(prompt)
	return w, err
}

// liveDrafter drafts with the settings current when each draft starts.
type liveDrafter struct{ live *config.Live }

func (d liveDrafter) Write(ctx context.Context, idea string, opts writer.Options) (writer.Draft, error) {
	w, _ := newWriter(d.live.Get(), "")
	return w.Write(ctx, idea, opts)
}

func (f *writeFlags) options() writer.Options {
	return writer.Options{Seconds: f.seconds, Instrumental: f.instrumental}
}

func newWriteCmd(a *app) *cobra.Command {
	var f writeFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "write <idea>",
		Short: "Draft lyrics and a style caption from a song idea",
		Example: `  songsmith write "a road-trip anthem about leaving a small town"
  songsmith write --seconds 90 --json "lo-fi study beat about rain"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			d, err := f.writer(cmd, cfg).Write(cmd.Context(), strings.Join(args, " "), f.options())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(d)
			}
			data, err := yaml.Marshal(d)
			if err != nil {
				return fmt.Errorf("encode draft: %w", err)
			}
			_, err = out.Write(data)
			return err
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON instead of YAML")
	return cmd
}
