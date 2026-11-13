package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
	"github.com/lancekrogers/songsmith/internal/queue"
)

// mastering is what the CLI needs to master a take; *master.Runner is it.
type mastering interface {
	Check() error
	Render(ctx context.Context, in, out string, s master.Settings, progress func(float64)) (master.Measure, error)
}

// autoMaster masters each new render with the saved default chain when the
// config says to, keeps the raw render as its own take and makes the master
// current. It reads the config at each render.
func autoMaster(store *library.Store, get func() config.Config, run mastering) queue.AfterRender {
	return func(ctx context.Context, song library.Song) (library.Song, error) {
		m := get().Master
		if !m.Auto || m.Default.IsZero() {
			return song, nil
		}
		if err := run.Check(); err != nil {
			return song, err
		}
		take, _, err := store.MasterTake(ctx, song.ID, song.Audio, m.Default, run.Render, nil)
		if err != nil {
			return song, err
		}
		return store.SetCurrent(song.ID, take.File)
	}
}

// findSong loads a song by its ID or a prefix of one that names only it.
func findSong(store *library.Store, id string) (library.Song, error) {
	if song, err := store.Load(id); err == nil {
		return song, nil
	}
	songs, _ := store.List()
	var found []library.Song
	for _, s := range songs {
		if strings.HasPrefix(s.ID, id) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return library.Song{}, fmt.Errorf("no song %q in the library; songsmith list shows the IDs", id)
	}
	return library.Song{}, fmt.Errorf("%q names %d songs; give more of the ID", id, len(found))
}

type masterFlags struct {
	presets                        []string
	useDefault                     bool
	take                           string
	setCurrent                     bool
	noise                          float64
	dc, notch                      bool
	lufs, ceiling                  float64
	lowCut, tilt, deHarsh, width   float64
	trimStart, trimEnd, fadeIn, fo float64
}

// settings builds the chain: the saved default with --default, then each
// --preset in turn, then every value flag given.
func (f masterFlags) settings(cmd *cobra.Command, saved master.Settings) (master.Settings, error) {
	var s master.Settings
	if f.useDefault {
		s = saved
	}
	for _, name := range f.presets {
		p, ok := master.Find(name)
		if !ok {
			var names []string
			for _, p := range master.Presets {
				if p.Name != "Off" {
					names = append(names, p.Name)
				}
			}
			return s, fmt.Errorf("no preset %q; the presets are %s", name, strings.Join(names, ", "))
		}
		s = s.With(p.Settings)
	}
	set := func(flag string, dst *float64, v float64) {
		if cmd.Flags().Changed(flag) {
			*dst = v
		}
	}
	set("noise", &s.NoiseBed, f.noise)
	set("lufs", &s.Loudness, f.lufs)
	set("ceiling", &s.Ceiling, f.ceiling)
	set("low-cut", &s.LowCut, f.lowCut)
	set("tilt", &s.Tilt, f.tilt)
	set("de-harsh", &s.DeHarsh, f.deHarsh)
	set("width", &s.Width, f.width)
	set("trim-start", &s.TrimStart, f.trimStart)
	set("trim-end", &s.TrimEnd, f.trimEnd)
	set("fade-in", &s.FadeIn, f.fadeIn)
	set("fade-out", &s.FadeOut, f.fo)
	if cmd.Flags().Changed("dc") {
		s.DCRumble = f.dc
	}
	if cmd.Flags().Changed("notch") {
		s.NotchTones = f.notch
	}
	if err := s.Check(); err != nil {
		return s, err
	}
	if s.IsZero() {
		return s, errors.New("nothing to do: give --preset, --default or a setting such as --lufs -14")
	}
	return s, nil
}

func newMasterCmd(a *app) *cobra.Command {
	var f masterFlags
	cmd := &cobra.Command{
		Use:   "master <song-id>",
		Short: "Master a take of a song into a new take",
		Long: "Master the current take (or --take) of a finished song with ffmpeg into a new take beside it.\n" +
			"Nothing is replaced: the library keeps playing the current take unless --set-current is given.\n" +
			"Presets: Streaming, Loud, Warm and Fix abrupt ending. The repair options (--dc, --notch, --noise) are\n" +
			"never part of a preset; --noise lowers steady hiss a little and dulls the song with it.",
		Example: `  songsmith master 20261112-200000-my-song --preset streaming --preset warm
  songsmith master 20261112-200000-my-song --lufs -14 --ceiling -1 --fade-out 4 --set-current
  songsmith master 20261112-200000-my-song --default`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			s, err := f.settings(cmd, cfg.Master.Default)
			if err != nil {
				return err
			}
			run := master.NewRunner()
			if err := run.Check(); err != nil {
				return fmt.Errorf("%w; install ffmpeg (macOS: brew install ffmpeg; Debian or Ubuntu: sudo apt install ffmpeg)", err)
			}
			return masterSong(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), library.NewStore(cfg.LibraryDir), run, args[0], f.take, s, f.setCurrent)
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.presets, "preset", nil, "apply a preset; repeat to combine (Streaming, Loud, Warm, Fix abrupt ending)")
	fl.BoolVar(&f.useDefault, "default", false, "start from the saved default chain (master.default)")
	fl.StringVar(&f.take, "take", "", "the take to master (default: the current take)")
	fl.BoolVar(&f.setCurrent, "set-current", false, "make the new master the current take")
	fl.Float64Var(&f.noise, "noise", 0, "lower steady hiss by this many dB; it also dulls the song (0 = off)")
	fl.BoolVar(&f.dc, "dc", false, "remove DC offset and rumble")
	fl.BoolVar(&f.notch, "notch", false, "notch the renderer's fixed tones")
	fl.Float64Var(&f.lufs, "lufs", 0, "loudness target in LUFS (0 = off)")
	fl.Float64Var(&f.ceiling, "ceiling", 0, "true-peak ceiling in dBTP (0 = off)")
	fl.Float64Var(&f.lowCut, "low-cut", 0, "high-pass frequency in Hz (0 = off)")
	fl.Float64Var(&f.tilt, "tilt", 0, "tone tilt in dB: negative warmer, positive brighter")
	fl.Float64Var(&f.deHarsh, "de-harsh", 0, "cut around 3–6 kHz in dB")
	fl.Float64Var(&f.width, "width", 0, "stereo width in percent (100 = unchanged)")
	fl.Float64Var(&f.trimStart, "trim-start", 0, "seconds to cut from the start")
	fl.Float64Var(&f.trimEnd, "trim-end", 0, "seconds to cut from the end")
	fl.Float64Var(&f.fadeIn, "fade-in", 0, "fade-in length in seconds")
	fl.Float64Var(&f.fo, "fade-out", 0, "fade-out length in seconds")
	return cmd
}

func masterSong(ctx context.Context, out, errOut io.Writer, store *library.Store, run mastering, id, take string, s master.Settings, setCurrent bool) error {
	song, err := findSong(store, id)
	if err != nil {
		return err
	}
	if take == "" {
		take = song.Audio
	}
	if take == "" || song.Status != library.StatusDone {
		return fmt.Errorf("%s has no finished take to master", song.Title)
	}
	last := -1
	progress := func(f float64) {
		if pct := int(f * 100); pct/10 != last/10 {
			last = pct
			fmt.Fprintf(errOut, "mastering %d%%\n", pct)
		}
	}
	t, saved, err := store.MasterTake(ctx, song.ID, take, s, run.Render, progress)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved %s (%s): %s LUFS, %s.\n", t.File, master.Name(s), master.Signed(math.Round(t.Loudness*10)/10), formatSeconds(t.Duration))
	if setCurrent {
		if _, err := store.SetCurrent(saved.ID, t.File); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is now the current take.\n", t.File)
	} else {
		fmt.Fprintf(out, "The current take is still %s; songsmith takes %s --use %s switches.\n", saved.Audio, saved.ID, t.File)
	}
	return nil
}

func newTakesCmd(a *app) *cobra.Command {
	var use string
	cmd := &cobra.Command{
		Use:   "takes <song-id>",
		Short: "List a song's takes, or choose the current one",
		Example: `  songsmith takes 20261112-200000-my-song
  songsmith takes 20261112-200000-my-song --use song-2.master-1.flac`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			store := library.NewStore(cfg.LibraryDir)
			song, err := findSong(store, args[0])
			if err != nil {
				return err
			}
			if use != "" {
				if _, err := store.SetCurrent(song.ID, use); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is now the current take of %s.\n", use, song.Title)
				return nil
			}
			listTakes(cmd.OutOrStdout(), song, store.Takes(song))
			return nil
		},
	}
	cmd.Flags().StringVar(&use, "use", "", "make this take file the current one")
	return cmd
}

func listTakes(out io.Writer, song library.Song, takes []library.Take) {
	if len(takes) == 0 {
		fmt.Fprintf(out, "%s has no takes yet.\n", song.Title)
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, t := range takes {
		mark := " "
		if t.File == song.Audio {
			mark = "*"
		}
		length, loud := "-", "-"
		if t.Duration > 0 {
			length = formatSeconds(t.Duration)
		}
		if t.Loudness != 0 {
			loud = fmt.Sprintf("%.1f LUFS", t.Loudness)
		}
		chain := ""
		if t.Master != nil {
			chain = master.Summary(*t.Master)
		}
		fmt.Fprintf(tw, "%s %d\t%s\t%s\t%s\t%s\t%s\n", mark, i+1, t.File, t.Kind, length, loud, chain)
	}
	tw.Flush()
	fmt.Fprintln(out, "* is the current take.")
}
