package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/health"
)

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
			checks := health.Run(ctx, cfg)
			printChecks(cmd.OutOrStdout(), checks)
			if health.Failed(checks) {
				return errors.New("some checks failed")
			}
			return nil
		},
	}
}

func printChecks(w io.Writer, checks []health.Check) {
	for _, c := range checks {
		label := okStyle.Render("OK  ")
		switch c.Result {
		case health.Warn:
			label = warnStyle.Render("WARN")
		case health.Fail:
			label = failStyle.Render("FAIL")
		}
		fmt.Fprintf(w, "%s %-24s %s\n", label, c.Name, c.Detail)
	}
}
