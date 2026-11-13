package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/config"
)

var version = "dev"

type app struct {
	configPath string
}

func (a *app) loadConfig() (config.Config, error) {
	return config.Load(a.configPath)
}

func newRootCmd() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:   "songsmith",
		Short: "Write and render songs with local models",
		Long: "Songsmith turns a song idea into a finished song with vocals. A local Ollama model drafts\n" +
			"lyrics and a style caption, and MiniMax Music 3 renders the song through a local ComfyUI server.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runTUI(cmd.Context())
		},
	}
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "config file (default $SONGSMITH_CONFIG or ~/.config/songsmith/config.yaml)")
	root.AddCommand(
		newWriteCmd(a),
		newReviseCmd(a),
		newGenerateCmd(a),
		newListCmd(a),
		newConfigCmd(a),
		newServerCmd(a),
		newDoctorCmd(a),
		newSetupCmd(a),
	)
	return root
}

func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fang.Execute(ctx, newRootCmd(), fang.WithVersion(version))
}
