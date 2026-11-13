package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
)

func (a *app) serverManager() (*server.Manager, config.Config, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, cfg, err
	}
	return server.New(cfg.ComfyUI, cfg.StateDir), cfg, nil
}

func newServerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage the local ComfyUI server",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Start ComfyUI with the flags MiniMax Music 3 needs",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				m, cfg, err := a.serverManager()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Starting ComfyUI from %s (log: %s)...\n", cfg.ComfyUI.Dir, m.LogPath())
				if err := m.Ensure(cmd.Context()); err != nil {
					return err
				}
				return printStatus(cmd, a)
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show whether ComfyUI is running",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return printStatus(cmd, a)
			},
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop a ComfyUI server that songsmith started",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				m, _, err := a.serverManager()
				if err != nil {
					return err
				}
				if err := m.Stop(cmd.Context()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "ComfyUI stopped.")
				return nil
			},
		},
	)
	return cmd
}

func printStatus(cmd *cobra.Command, a *app) error {
	m, cfg, err := a.serverManager()
	if err != nil {
		return err
	}
	st, err := m.Status(cmd.Context())
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if !st.Running {
		fmt.Fprintf(out, "ComfyUI is not running at %s.\n", cfg.ComfyUI.BaseURL())
		return nil
	}
	owner := "started outside songsmith"
	if st.Managed {
		owner = fmt.Sprintf("started by songsmith, pid %d", st.PID)
	}
	fmt.Fprintf(out, "ComfyUI is running at %s (%s), device %s, %s %v.\n",
		cfg.ComfyUI.BaseURL(), owner, st.Device, server.RequiredFlag, st.GPUOnly)
	return nil
}
