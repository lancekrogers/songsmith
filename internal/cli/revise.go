package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/writer"
)

func newReviseCmd(a *app) *cobra.Command {
	var model string
	var asJSON, save, queueIt bool
	var seed int64
	cmd := &cobra.Command{
		Use:   "revise <song-id> <instruction>",
		Short: "Have the writer revise or extend a song's lyrics and style",
		Long: "Send a library song and an instruction to the writer and print the revised draft.\n" +
			"--save keeps it as a new draft song that records where it came from; the source song\n" +
			"never changes. A copy of a rendered song keeps that song's seed, pinned.\n" +
			"Song IDs are in the last column of `songsmith list`.",
		Example: `  songsmith revise 20261112-200000-neon-water "make it longer with two more verses that continue the story"
  songsmith revise --queue 20261112-200000-neon-water "add a bridge before the last chorus"`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("seed") && !save && !queueIt {
				return errors.New("--seed applies to the saved song; add --save or --queue")
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			store := library.NewStore(cfg.LibraryDir)
			src, err := store.Load(args[0])
			if err != nil {
				return err
			}
			w, werr := newWriter(cfg, model)
			if werr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; using the built-in instructions\n", werr)
			}
			current := writer.Draft{Title: src.Title, Lyrics: src.Lyrics, Caption: src.Caption, Seconds: src.Seconds, Instrumental: src.Instrumental}
			d, err := w.Revise(cmd.Context(), current, src.Idea, strings.Join(args[1:], " "), writer.Options{Instrumental: src.Instrumental})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				err = enc.Encode(d)
			} else {
				var data []byte
				if data, err = yaml.Marshal(d); err == nil {
					_, err = out.Write(data)
				}
			}
			if err != nil || !save && !queueIt {
				return err
			}

			song := revisedSong(src, d)
			if cmd.Flags().Changed("seed") {
				if seed < 0 {
					return errors.New("--seed must be 0 or more")
				}
				song.Seed, song.SeedPinned = seed, true
			}
			song, err = store.Create(song)
			if err != nil {
				return err
			}
			if !queueIt {
				fmt.Fprintf(cmd.ErrOrStderr(), "Saved %s as a draft (from %s).\n", song.ID, src.ID)
				return nil
			}
			r := newRenderer(config.NewLive(a.configPath, cfg))
			if _, err := r.queue.Enqueue(song); err != nil {
				return fmt.Errorf("saved %s as a draft but could not queue it: %w", song.ID, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Queued %s (from %s); it renders when songsmith's queue runs, such as while the TUI is open.\n", song.ID, src.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Ollama model (default from config)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON instead of YAML")
	cmd.Flags().BoolVar(&save, "save", false, "save the revision as a new draft song")
	cmd.Flags().BoolVar(&queueIt, "queue", false, "save the revision and queue it to render")
	cmd.Flags().Int64Var(&seed, "seed", 0, "pin this render seed on the saved song")
	return cmd
}

// revisedSong is a new draft song holding d, derived from src. A copy of a
// rendered or rendering song keeps its seed, pinned, and says it is extended
// unless the revision renamed it. The length grows to the revision's, never
// shrinks.
func revisedSong(src library.Song, d writer.Draft) library.Song {
	song := library.Song{
		Idea: src.Idea, Title: d.Title, Lyrics: d.Lyrics, Caption: d.Caption,
		Seconds: max(src.Seconds, d.Seconds), Instrumental: src.Instrumental, DerivedFrom: src.ID,
		Status: library.StatusDraft,
	}
	if strings.TrimSpace(song.Title) == "" {
		song.Title = src.Title
	}
	if src.SeedPinned {
		song.Seed, song.SeedPinned = src.Seed, true
	}
	if src.Status == library.StatusDone || src.Status == library.StatusRendering {
		if src.Seed != 0 {
			song.Seed, song.SeedPinned = src.Seed, true
		}
		if song.Title == src.Title && !strings.HasSuffix(song.Title, " (extended)") {
			song.Title += " (extended)"
		}
	}
	return song
}
