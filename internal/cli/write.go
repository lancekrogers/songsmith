package cli

import (
	"cmp"
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

func (f *writeFlags) writer(cfg config.Config) *writer.Writer {
	return writer.New(ollama.NewClient(cfg.Ollama.URL), cmp.Or(f.model, cfg.Ollama.Model))
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
			d, err := f.writer(cfg).Write(cmd.Context(), strings.Join(args, " "), f.options())
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
