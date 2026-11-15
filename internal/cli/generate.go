package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

func newGenerateCmd(a *app) *cobra.Command {
	var f writeFlags
	var lyricsFile, captionFile, title, machine string
	var seed int64
	cmd := &cobra.Command{
		Use:   "generate [idea]",
		Short: "Write (optionally) and render a song in the foreground",
		Long: "Render a song into the library. Give an idea to have the local LLM draft lyrics and a\n" +
			"caption first, or pass --lyrics and --caption files you wrote yourself.\n" +
			"Renders take several minutes per minute of audio.",
		Example: `  songsmith generate --seconds 60 "a sea shanty about debugging production at 3am"
  songsmith generate --title "Coast Road" --lyrics lyrics.txt --caption caption.txt --seconds 90
  songsmith generate --machine "MacBook Pro" --lyrics lyrics.txt --caption caption.txt`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			idea := strings.TrimSpace(strings.Join(args, " "))
			pin, err := machinePin(cfg, machine)
			if err != nil {
				return err
			}

			song := library.Song{Idea: idea, Title: title, Seed: seed, SeedPinned: cmd.Flags().Changed("seed"), Seconds: f.seconds, Machine: pin}
			switch {
			case lyricsFile != "" || captionFile != "":
				if lyricsFile == "" || captionFile == "" {
					return errors.New("--lyrics and --caption must be given together")
				}
				lyrics, err := os.ReadFile(lyricsFile)
				if err != nil {
					return fmt.Errorf("read lyrics: %w", err)
				}
				caption, err := os.ReadFile(captionFile)
				if err != nil {
					return fmt.Errorf("read caption: %w", err)
				}
				song.Lyrics = strings.TrimSpace(string(lyrics))
				song.Caption = writer.ParseCaption(string(caption))
				song.Seconds = cmp.Or(song.Seconds, float64(cfg.Render.DefaultSeconds))
				song.Title = cmp.Or(song.Title, "Untitled")
			case idea != "":
				fmt.Fprintf(out, "Drafting lyrics with %s...\n", cmp.Or(f.model, cfg.Ollama.Model))
				d, err := f.writer(cmd, cfg).Write(cmd.Context(), idea, f.options())
				if err != nil {
					return err
				}
				song.Title = cmp.Or(title, d.Title)
				song.Lyrics, song.Caption, song.Seconds = d.Lyrics, d.Caption, d.Seconds
				fmt.Fprintf(out, "%q, %.0fs\n\n%s\n\n", song.Title, song.Seconds, song.Lyrics)
			default:
				return errors.New("give a song idea, or --lyrics and --caption files")
			}
			if err := song.CheckRender(); err != nil {
				return err
			}

			r := newRenderer(config.NewLive(a.configPath, cfg))
			lock, err := r.store.TryLock()
			if err != nil {
				if errors.Is(err, library.ErrLocked) {
					return errors.New("another songsmith (probably the TUI) is rendering; queue this song there instead")
				}
				return err
			}
			defer lock.Release()

			song.Status = library.StatusQueued
			song, err = r.store.Create(song)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Rendering %s (up to %.0fs of audio, usually several minutes per minute)...\n", song.ID, song.Seconds)
			if len(cfg.Machines) > 0 {
				fmt.Fprintf(out, "It goes to %s.\n", cmp.Or(pin, "the fastest machine that answers"))
			}

			updates, stop := r.queue.Subscribe()
			defer stop()
			printed := make(chan struct{})
			go func() {
				defer close(printed)
				printProgress(out, updates)
			}()
			start := time.Now()
			song = r.queue.Render(cmd.Context(), song)
			stop()
			<-printed

			switch song.Status {
			case library.StatusDone:
				fmt.Fprintf(out, "\nDone in %s: %s (%.0fs)\n", time.Since(start).Round(time.Second), r.store.AudioPath(song), song.Duration)
				return nil
			case library.StatusQueued:
				if song.JobID == "" {
					return fmt.Errorf("%s did not render: %s. It is saved as %s, and the songsmith terminal UI renders it once a machine can take it", song.Title, song.Error, song.ID)
				}
				fmt.Fprintf(out, "\nDetached; ComfyUI on %s keeps rendering %s and songsmith picks up the result the next time it runs its queue.\n",
					cmp.Or(song.RenderingOn, config.ThisComputer), song.ID)
				return nil
			default:
				return fmt.Errorf("render failed: %s", song.Error)
			}
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&lyricsFile, "lyrics", "", "lyrics file with section tags")
	cmd.Flags().StringVar(&captionFile, "caption", "", "caption file (Global Metadata / Vocal Details / Arrangement)")
	cmd.Flags().StringVar(&title, "title", "", "song title")
	cmd.Flags().Int64Var(&seed, "seed", 0, "render seed (default random)")
	cmd.Flags().StringVar(&machine, "machine", "", `render on this machine only, "`+config.ThisComputer+`" or a name from songsmith machines (default: the fastest free one)`)
	return cmd
}

// machinePin checks that name is a machine a song can be pinned to now, and
// returns its name as the config spells it; "" pins nothing.
func machinePin(cfg config.Config, name string) (string, error) {
	switch {
	case name == "":
		return "", nil
	case strings.EqualFold(name, config.ThisComputer):
		if !cfg.Local.Enabled {
			return "", fmt.Errorf("%s is turned off for rendering (local.enabled: false in the config)", config.ThisComputer)
		}
		return config.ThisComputer, nil
	}
	m, ok := cfg.Machine(name)
	if !ok {
		return "", fmt.Errorf("there is no machine named %q; the machines are: %s", name, machineNames(cfg))
	}
	if !m.Enabled {
		return "", fmt.Errorf("%s is turned off for rendering (enabled: false in its machines entry in the config)", m.Name)
	}
	return m.Name, nil
}

func printProgress(w io.Writer, updates <-chan queueUpdate) {
	var last backend.Progress
	for u := range updates {
		p := u.Progress
		if p.Stage == "" {
			continue
		}
		if p.Stage == last.Stage && (p.Max == 0 || p.Value != p.Max && (p.Value-last.Value)*20 < p.Max) {
			continue
		}
		last = p
		if p.Max > 0 {
			fmt.Fprintf(w, "  %-16s %3d%%\n", p.Stage, p.Value*100/p.Max)
		} else {
			fmt.Fprintf(w, "  %s\n", p.Stage)
		}
	}
}
