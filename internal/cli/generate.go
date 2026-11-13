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
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

func newGenerateCmd(a *app) *cobra.Command {
	var f writeFlags
	var lyricsFile, captionFile, title string
	var seed int64
	cmd := &cobra.Command{
		Use:   "generate [idea]",
		Short: "Write (optionally) and render a song in the foreground",
		Long: "Render a song into the library. Give an idea to have the local LLM draft lyrics and a\n" +
			"caption first, or pass --lyrics and --caption files you wrote yourself.\n" +
			"Renders take several minutes per minute of audio.",
		Example: `  songsmith generate --seconds 60 "a sea shanty about debugging production at 3am"
  songsmith generate --title "Coast Road" --lyrics lyrics.txt --caption caption.txt --seconds 90`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			idea := strings.TrimSpace(strings.Join(args, " "))

			song := library.Song{Idea: idea, Title: title, Seed: seed, Seconds: f.seconds}
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
				song.Seconds = cmp.Or(song.Seconds, 120)
				song.Title = cmp.Or(song.Title, "Untitled")
			case idea != "":
				fmt.Fprintf(out, "Drafting lyrics with %s...\n", cmp.Or(f.model, cfg.Ollama.Model))
				d, err := f.writer(cfg).Write(cmd.Context(), idea, f.options())
				if err != nil {
					return err
				}
				song.Title = cmp.Or(title, d.Title)
				song.Lyrics, song.Caption, song.Seconds = d.Lyrics, d.Caption, d.Seconds
				fmt.Fprintf(out, "%q, %.0fs\n\n%s\n\n", song.Title, song.Seconds, song.Lyrics)
			default:
				return errors.New("give a song idea, or --lyrics and --caption files")
			}
			if err := (writer.Draft{Lyrics: song.Lyrics, Caption: song.Caption, Seconds: song.Seconds}).Validate(); err != nil {
				return err
			}

			r := newRenderer(cfg)
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

			updates, stop := r.queue.Subscribe()
			defer stop()
			go printProgress(out, updates)
			start := time.Now()
			song = r.queue.Render(cmd.Context(), song)
			stop()

			switch song.Status {
			case library.StatusDone:
				fmt.Fprintf(out, "\nDone in %s: %s (%.0fs)\n", time.Since(start).Round(time.Second), r.store.AudioPath(song), song.Duration)
				return nil
			case library.StatusQueued:
				fmt.Fprintf(out, "\nInterrupted; %s stays queued and renders the next time songsmith runs its queue.\n", song.ID)
				return cmd.Context().Err()
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
	return cmd
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
