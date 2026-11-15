package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
)

const privateNetworkNote = "ComfyUI has no login: anyone who can reach a machine's address can use it. " +
	"Use an address on a private network, such as the computer's Tailscale name, never a public one."

func newMachinesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "machines",
		Short: "List the computers songs render on, and whether each is ready",
		Long: "Songsmith renders on this computer and on any other computer's ComfyUI server you add.\n" +
			"Queued songs go to whichever machine is free, so two machines render two songs at once.\n\n" +
			"On the other computer, once: install ComfyUI and the model files (`songsmith setup --install`\n" +
			"there is the easy way), set comfyui.host to an address this computer can reach, and run\n" +
			"`songsmith server start`. Then add it here with `songsmith machines add <name> <address>`.\n\n" +
			privateNetworkNote,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			printMachines(cmd.Context(), cmd.OutOrStdout(), cfg)
			return nil
		},
	}
	cmd.AddCommand(newMachinesAddCmd(a), newMachinesRemoveCmd(a))
	return cmd
}

type machineRow struct {
	name, url, enabled string
	status             machines.Status
	local              bool
}

func printMachines(ctx context.Context, w io.Writer, cfg config.Config) {
	rows := []machineRow{{name: config.ThisComputer, url: cfg.ComfyUI.BaseURL(), enabled: onOff(cfg.Local.Enabled), local: true}}
	for _, m := range cfg.Machines {
		rows = append(rows, machineRow{name: m.Name, url: m.URL, enabled: onOff(m.Enabled)})
	}
	var wg sync.WaitGroup
	for i := range rows {
		models := cfg.ComfyUI
		if m, ok := cfg.Machine(rows[i].name); ok && !rows[i].local {
			models = m.Models(cfg.ComfyUI)
		}
		wg.Go(func() { rows[i].status = machines.Check(ctx, rows[i].url, models) })
	}
	wg.Wait()

	speeds := machines.NewSpeeds(cfg.StateDir).All()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MACHINE\tSTATUS\tADDRESS\tSPEED\tENABLED")
	for _, r := range rows {
		state := string(r.status.State)
		switch {
		case r.status.AnsweredWithError:
			state = "error"
		case r.local && r.status.State == machines.Offline:
			state = "stopped"
		case r.status.State == machines.Ready && len(r.status.Unchecked) > 0:
			state = "ready, files not checked"
		}
		speed := "not measured yet"
		if sp, ok := speeds[r.name]; ok && sp.PerAudioSecond() > 0 {
			speed = fmt.Sprintf("%.1f s per second of audio, %d %s", sp.PerAudioSecond(), sp.Renders, plural(sp.Renders, "render", "renders"))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.name, state, r.url, speed, r.enabled)
	}
	tw.Flush()
	for _, r := range rows {
		switch {
		case r.local && r.status.State == machines.Offline && !r.status.AnsweredWithError:
			fmt.Fprintf(w, "\n%s: ComfyUI is not running at %s; songsmith starts it on the first render here (shell: `songsmith server start`).\n", r.name, r.url)
		case r.status.State != machines.Ready || len(r.status.Unchecked) > 0:
			fmt.Fprintf(w, "\n%s: %s\n", r.name, r.status.Detail)
		}
	}
	if len(cfg.Machines) == 0 {
		fmt.Fprintf(w, "\nNo other machines yet. Add one with `songsmith machines add <name> <address>`; `songsmith machines --help` says what the other computer needs.\n")
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func newMachinesAddCmd(a *app) *cobra.Command {
	var m config.Machine
	cmd := &cobra.Command{
		Use:   "add <name> <address>",
		Short: "Add another computer's ComfyUI server to render on",
		Long: "Add another computer's ComfyUI server, such as http://studio.tailnet.ts.net:8188. Songsmith checks it\n" +
			"and says what it found, then saves it, ready or not, so a computer that is asleep now can be added.\n\n" +
			privateNetworkNote,
		Example: `  songsmith machines add "MacBook Pro" http://macbook-pro-2:8188
  songsmith machines add Attic attic.tailnet.ts.net:8188 --vae minimax_music3_dav_fp8.safetensors`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			m.Name, m.URL, m.Enabled = args[0], args[1], true
			list, err := cfg.WithMachine(m)
			if err != nil {
				return err
			}
			added := list[len(list)-1]
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Checking %s at %s...\n", added.Name, added.URL)
			st := machines.Check(cmd.Context(), added.URL, added.Models(cfg.ComfyUI))
			if st.State == machines.Ready {
				fmt.Fprintf(out, "%s is %s.\n", added.Name, st.Detail)
			} else {
				fmt.Fprintf(out, "%s is %s: %s\n", added.Name, st.State, st.Detail)
			}
			if _, err := config.AddMachine(config.Path(a.configPath), m); err != nil {
				return err
			}
			fmt.Fprintf(out, "Saved %s. Songs go to it once it is ready; a songsmith that is already open uses it once Settings → Machines is opened there, or after it restarts.\n", added.Name)
			fmt.Fprintln(out, privateNetworkNote)
			return nil
		},
	}
	cmd.Flags().StringVar(&m.DiffusionModel, "diffusion-model", "", "the diffusion model file on that computer (default: comfyui.diffusion_model)")
	cmd.Flags().StringVar(&m.TextEncoder, "text-encoder", "", "the text encoder file on that computer (default: comfyui.text_encoder)")
	cmd.Flags().StringVar(&m.VAE, "vae", "", "the VAE file on that computer (default: comfyui.vae)")
	return cmd
}

func newMachinesRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Stop rendering on an added machine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if _, ok := cfg.Machine(args[0]); !ok {
				return fmt.Errorf("there is no machine named %q; the machines are: %s", args[0], machineNames(cfg))
			}
			m, err := config.RemoveMachine(config.Path(a.configPath), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s. A song that was rendering there is queued again the next time songsmith starts, and its render there is left alone.\n", m.Name)
			return nil
		},
	}
}

// machineNames lists every machine a song can be sent to, this computer first.
func machineNames(cfg config.Config) string {
	names := []string{config.ThisComputer}
	for _, m := range cfg.Machines {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
