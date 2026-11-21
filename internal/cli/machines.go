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
			"`songsmith server start`. Then add it here with `songsmith machines add <name> <address>`,\n" +
			"or, if both computers are on Tailscale, find it with `songsmith machines add --discover`.\n\n" +
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
		fmt.Fprintf(w, "\nNo other machines yet. Add one with `songsmith machines add <name> <address>`, or find one on Tailscale with "+
			"`songsmith machines add --discover`; `songsmith machines --help` says what the other computer needs.\n")
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
	var discover bool
	cmd := &cobra.Command{
		Use:   "add <name> <address> | --discover [[<name>] <computer>]",
		Short: "Add another computer's ComfyUI server to render on",
		Long: "Add another computer's ComfyUI server, such as http://studio.tailnet.ts.net:8188. Songsmith checks it\n" +
			"and says what it found, then saves it, ready or not, so a computer that is asleep now can be added.\n\n" +
			"With --discover, songsmith asks Tailscale for the other computers on your tailnet and asks each\n" +
			"whether its ComfyUI answers. Alone, it lists them and adds nothing. Given a computer from that\n" +
			"list, by its name, Tailscale name or address, it adds that one, under <name> when one is given.\n\n" +
			privateNetworkNote,
		Example: `  songsmith machines add "MacBook Pro" http://macbook-pro-2:8188
  songsmith machines add Attic attic.tailnet.ts.net:8188 --vae minimax_music3_dav_fp8.safetensors
  songsmith machines add --discover
  songsmith machines add --discover "Studio Mac"
  songsmith machines add Studio studio --discover`,
		Args: func(cmd *cobra.Command, args []string) error {
			if !discover {
				return cobra.ExactArgs(2)(cmd, args)
			}
			if len(args) > 2 {
				return fmt.Errorf("--discover takes at most a name and a computer, such as "+
					"songsmith machines add --discover \"Studio Mac\" or songsmith machines add Studio \"Studio Mac\" --discover; got %d arguments", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if discover {
				return a.addDiscovered(cmd, cfg, m, args)
			}
			m.Name, m.URL, m.Enabled = args[0], args[1], true
			return a.saveMachine(cmd, cfg, m, func(out io.Writer, added config.Machine) {
				fmt.Fprintf(out, "Checking %s at %s...\n", added.Name, added.URL)
				reportCheck(out, added.Name, machines.Check(cmd.Context(), added.URL, added.Models(cfg.ComfyUI)))
			})
		},
	}
	cmd.Flags().BoolVar(&discover, "discover", false, "find the computer on Tailscale instead of giving its address")
	cmd.Flags().StringVar(&m.DiffusionModel, "diffusion-model", "", "the diffusion model file on that computer (default: comfyui.diffusion_model)")
	cmd.Flags().StringVar(&m.TextEncoder, "text-encoder", "", "the text encoder file on that computer (default: comfyui.text_encoder)")
	cmd.Flags().StringVar(&m.VAE, "vae", "", "the VAE file on that computer (default: comfyui.vae)")
	return cmd
}

func reportCheck(out io.Writer, name string, st machines.Status) {
	if st.State == machines.Ready {
		fmt.Fprintf(out, "%s is %s.\n", name, st.Detail)
	} else {
		fmt.Fprintf(out, "%s is %s: %s\n", name, st.State, st.Detail)
	}
}

// saveMachine adds m once report has said what it found, ready or not.
func (a *app) saveMachine(cmd *cobra.Command, cfg config.Config, m config.Machine, report func(out io.Writer, added config.Machine)) error {
	list, err := cfg.WithMachine(m)
	if err != nil {
		return err
	}
	added := list[len(list)-1]
	out := cmd.OutOrStdout()
	report(out, added)
	if _, err := config.AddMachine(config.Path(a.configPath), m); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved %s. Songs go to it once it is ready; a songsmith that is already open uses it once Settings → Machines is opened there, or after it restarts.\n", added.Name)
	fmt.Fprintln(out, privateNetworkNote)
	return nil
}

// addDiscovered lists the computers on the tailnet, or adds the one args
// names, under the name args gives first when there are two.
func (a *app) addDiscovered(cmd *cobra.Command, cfg config.Config, m config.Machine, args []string) error {
	find, err := a.machineFinder(cfg)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Looking for computers on Tailscale...")
	cs, err := find(cmd.Context())
	if err != nil {
		return err
	}
	if len(args) == 0 {
		listDiscovered(cmd.Context(), out, cfg, m.Models(cfg.ComfyUI), cs)
		return nil
	}
	c, err := pickComputer(cs, args[len(args)-1])
	if err != nil {
		return err
	}
	if name, ok := c.AddedAs(cfg); ok {
		return fmt.Errorf("%s is already added, as %s; `songsmith machines` shows it", c.Name, name)
	}
	m.Name, m.Enabled = c.Name, true
	if len(args) == 2 {
		m.Name = args[0]
	}
	if len(c.URLs) > 0 {
		m.URL = c.URLs[0]
	}
	if !c.Online {
		return a.saveMachine(cmd, cfg, m, func(out io.Writer, added config.Machine) {
			fmt.Fprintf(out, "%s is offline on Tailscale, so songsmith did not ask it; it is saved at %s.\n", added.Name, added.URL)
		})
	}
	url, answered, reason := machines.Prober{}.Reach(cmd.Context(), c)
	m.URL = url
	return a.saveMachine(cmd, cfg, m, func(out io.Writer, added config.Machine) {
		fmt.Fprintf(out, "Checking %s at %s...\n", added.Name, added.URL)
		if !answered {
			fmt.Fprintf(out, "%s is not answering (%s). %s\n", added.Name, reason, c.NotAnswering())
			return
		}
		reportCheck(out, added.Name, machines.Check(cmd.Context(), added.URL, added.Models(cfg.ComfyUI)))
	})
}

// pickComputer is the one computer of cs that want names, by its name, its
// Tailscale name or one of its addresses, ignoring case.
func pickComputer(cs []machines.Candidate, want string) (machines.Candidate, error) {
	var hits []machines.Candidate
	for _, c := range cs {
		if c.Matches(want) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if len(cs) == 0 {
			return machines.Candidate{}, fmt.Errorf("Tailscale lists no other computers, so none is called %q; add it by its address with `songsmith machines add <name> <address>`", want)
		}
		return machines.Candidate{}, fmt.Errorf("no computer on Tailscale is called %q; the computers found are: %s", want, candidateNames(cs))
	}
	return machines.Candidate{}, fmt.Errorf("%d computers on Tailscale match %q: %s; give its full Tailscale name or its address instead", len(hits), want, candidateNames(hits))
}

func candidateNames(cs []machines.Candidate) string {
	var names []string
	for _, c := range cs {
		names = append(names, fmt.Sprintf("%s (%s)", c.Name, c.Address))
	}
	return strings.Join(names, ", ")
}

// listDiscovered asks every computer on the tailnet whether its ComfyUI
// answers, and prints what it found and how to add one.
func listDiscovered(ctx context.Context, w io.Writer, cfg config.Config, models config.ComfyUI, cs []machines.Candidate) {
	if len(cs) == 0 {
		fmt.Fprintln(w, "Tailscale lists no other computers that could run ComfyUI. Add one by its address with `songsmith machines add <name> <address>`.")
		return
	}
	found := make([]machines.Found, len(cs))
	machines.Prober{}.ProbeAll(ctx, cfg, models, cs, func(i int, f machines.Found) { found[i] = f })

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COMPUTER\tCOMFYUI\tADDRESS")
	// The example is a computer that is ready, else the first not added.
	example, ready := -1, false
	for i, c := range cs {
		f := found[i]
		if _, added := c.AddedAs(cfg); !added {
			isReady := f.Answered && f.Status.State == machines.Ready
			if example < 0 || isReady && !ready {
				example, ready = i, isReady
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, discoveredState(cfg, c, f), f.URL)
	}
	tw.Flush()

	if example < 0 {
		fmt.Fprintln(w, "\nEvery computer found is added already.")
	} else {
		name := shellQuote(cs[example].Name)
		fmt.Fprintf(w, "\nAdd one by its name: songsmith machines add --discover %s\n", name)
		fmt.Fprintf(w, "Or under a name of your own: songsmith machines add <name> %s --discover\n", name)
	}
	for i, c := range cs {
		f := found[i]
		if _, added := c.AddedAs(cfg); added || !f.Asked {
			continue
		}
		switch {
		case !f.Answered:
			fmt.Fprintf(w, "\n%s: %s\n", c.Name, c.NotAnswering())
		case f.Status.State != machines.Ready || len(f.Status.Unchecked) > 0:
			fmt.Fprintf(w, "\n%s: %s\n", c.Name, f.Status.Detail)
		}
	}
	fmt.Fprintln(w, "\n"+privateNetworkNote)
}

// discoveredState sums up, in a few words, what asking c found.
func discoveredState(cfg config.Config, c machines.Candidate, f machines.Found) string {
	if name, ok := c.AddedAs(cfg); ok {
		return "already added as " + name
	}
	switch {
	case !c.Online:
		return "offline on Tailscale"
	case !f.Asked:
		return "not asked"
	case !f.Answered:
		return "not answering (" + f.Reason + ")"
	case f.Status.AnsweredWithError:
		return "answered with an error"
	case f.Status.State == machines.Ready && len(f.Status.Unchecked) > 0:
		return "ready, files not checked"
	case f.Status.State == machines.Ready:
		return strings.Join(append([]string{"ready"}, f.Status.Hardware()...), ", ")
	}
	return string(f.Status.State)
}

// shellQuote quotes s for a shell when it has a space or other character
// a shell would split or expand.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:@/+", r))
	}) {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s) + `"`
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
