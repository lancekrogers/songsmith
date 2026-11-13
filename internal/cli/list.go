package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/queue"
)

type queueUpdate = queue.Update

func newListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List songs in the library",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			store := library.NewStore(cfg.LibraryDir)
			songs, err := store.List()
			out := cmd.OutOrStdout()
			if len(songs) == 0 {
				fmt.Fprintf(out, "No songs yet in %s.\n", cfg.LibraryDir)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, s := range songs {
				length := "-"
				if s.Duration > 0 {
					length = formatSeconds(s.Duration)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Status, s.Title, length, s.Created.Format(time.DateTime), s.ID)
			}
			tw.Flush()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
			}
			return nil
		},
	}
}

func formatSeconds(s float64) string {
	d := time.Duration(s * float64(time.Second)).Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
