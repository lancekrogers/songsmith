package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/songsmith/internal/config"
)

func newConfigCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			path := config.Path(a.configPath)
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("encode config: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s", path, out)
			return nil
		},
	}
}
