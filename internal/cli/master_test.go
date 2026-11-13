package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

type fakeMastering struct {
	err   error
	calls []master.Settings
}

func (f *fakeMastering) Check() error { return f.err }

func (f *fakeMastering) Render(_ context.Context, _, out string, s master.Settings, progress func(float64)) (master.Measure, error) {
	f.calls = append(f.calls, s)
	if progress != nil {
		progress(1)
	}
	return master.Measure{Loudness: s.Loudness, Duration: 88}, os.WriteFile(out, []byte("m"), 0o644)
}

func finishedSong(t *testing.T, store *library.Store) library.Song {
	t.Helper()
	song, err := store.Create(library.Song{Title: "Song", Status: library.StatusDone, Audio: "song.flac", Duration: 90})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(store.TakePath(song, "song.flac"), []byte("a"), 0o644)
	return song
}

func TestMasterFlagsBuildTheChain(t *testing.T) {
	saved := master.Settings{Tilt: -1}
	cases := []struct {
		args []string
		want master.Settings
		err  string
	}{
		{[]string{"--preset", "streaming"}, master.Settings{Loudness: -14, Ceiling: -1, LowCut: 30}, ""},
		{[]string{"--preset", "loud", "--preset", "warm", "--dc", "--lufs", "-11"}, master.Settings{DCRumble: true, Loudness: -11, Ceiling: -1, Tilt: -1.5, DeHarsh: 2}, ""},
		{[]string{"--preset", "clean up static"}, master.Settings{}, "no preset"},
		{[]string{"--default", "--fade-out", "4", "--trim-end", "1.5"}, master.Settings{Tilt: -1, FadeOut: 4, TrimEnd: 1.5}, ""},
		{[]string{"--preset", "nope"}, master.Settings{}, "no preset"},
		{nil, master.Settings{}, "nothing to do"},
		{[]string{"--lufs", "-2"}, master.Settings{}, "between"},
	}
	for _, c := range cases {
		cmd := newMasterCmd(&app{})
		if err := cmd.ParseFlags(c.args); err != nil {
			t.Fatal(err)
		}
		f := flagsOf(t, cmd)
		got, err := f.settings(cmd, saved)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v: err %v", c.args, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%v: %+v, %v", c.args, got, err)
		}
	}
}

// flagsOf reads the parsed flags back into a masterFlags.
func flagsOf(t *testing.T, cmd *cobra.Command) masterFlags {
	t.Helper()
	fl := cmd.Flags()
	var f masterFlags
	f.presets, _ = fl.GetStringArray("preset")
	f.useDefault, _ = fl.GetBool("default")
	f.noise, _ = fl.GetFloat64("noise")
	f.dc, _ = fl.GetBool("dc")
	f.notch, _ = fl.GetBool("notch")
	f.lufs, _ = fl.GetFloat64("lufs")
	f.ceiling, _ = fl.GetFloat64("ceiling")
	f.lowCut, _ = fl.GetFloat64("low-cut")
	f.tilt, _ = fl.GetFloat64("tilt")
	f.deHarsh, _ = fl.GetFloat64("de-harsh")
	f.width, _ = fl.GetFloat64("width")
	f.trimStart, _ = fl.GetFloat64("trim-start")
	f.trimEnd, _ = fl.GetFloat64("trim-end")
	f.fadeIn, _ = fl.GetFloat64("fade-in")
	f.fo, _ = fl.GetFloat64("fade-out")
	return f
}

func TestMasterSongAndTakes(t *testing.T) {
	store := library.NewStore(t.TempDir())
	song := finishedSong(t, store)
	run := &fakeMastering{}
	var out, errOut bytes.Buffer
	s := master.Settings{Loudness: -14}
	if err := masterSong(t.Context(), &out, &errOut, store, run, song.ID[:12], "", s, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Saved song.master-1.flac") || !strings.Contains(errOut.String(), "mastering 100%") {
		t.Fatalf("out %q err %q", out.String(), errOut.String())
	}
	if got, _ := store.Load(song.ID); got.Audio != "song.flac" {
		t.Fatal("mastering changed the current take")
	}
	if err := masterSong(t.Context(), &out, &errOut, store, run, song.ID, "song.flac", s, true); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Load(song.ID)
	if got.Audio != "song.master-2.flac" {
		t.Fatalf("current %q", got.Audio)
	}
	out.Reset()
	listTakes(&out, got, store.Takes(got))
	if !strings.Contains(out.String(), "* 3  song.master-2.flac  master") || !strings.Contains(out.String(), "−14 LUFS") {
		t.Fatalf("takes:\n%s", out.String())
	}
	if err := masterSong(t.Context(), &out, &errOut, store, run, "nope", "", s, false); err == nil {
		t.Fatal("unknown song mastered")
	}
}

func TestTakesUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("library_dir: "+filepath.Join(dir, "lib")+"\nstate_dir: "+filepath.Join(dir, "state")+"\n"), 0o644)
	store := library.NewStore(filepath.Join(dir, "lib"))
	song := finishedSong(t, store)
	os.WriteFile(store.TakePath(song, "song-2.flac"), []byte("b"), 0o644)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--config", path, "takes", song.ID, "--use", "song-2.flac"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Load(song.ID); got.Audio != "song-2.flac" {
		t.Fatalf("current %q", got.Audio)
	}
	root.SetArgs([]string{"--config", path, "takes", song.ID, "--use", "other.flac"})
	if err := root.Execute(); err == nil {
		t.Fatal("a file that is not a take became current")
	}
	out.Reset()
	root.SetArgs([]string{"--config", path, "config"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"master:", "auto: false", "default:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("config output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestAutoMaster(t *testing.T) {
	store := library.NewStore(t.TempDir())
	song := finishedSong(t, store)
	cfg := config.Default()
	run := &fakeMastering{}
	hook := autoMaster(store, func() config.Config { return cfg }, run)
	if got, err := hook(t.Context(), song); err != nil || got.Audio != "song.flac" || len(run.calls) != 0 {
		t.Fatalf("off: %+v %v", got, err)
	}
	cfg.Master.Auto = true
	if _, err := hook(t.Context(), song); err != nil || len(run.calls) != 0 {
		t.Fatal("an empty default chain should master nothing")
	}
	cfg.Master.Default = master.Settings{Loudness: -14, NoiseBed: 20}
	got, err := hook(t.Context(), song)
	if err != nil || got.Audio != "song.master-1.flac" || run.calls[0] != cfg.Master.Default {
		t.Fatalf("on: %+v %v", got, err)
	}
	run.err = errors.New("ffmpeg is not installed")
	if _, err := hook(t.Context(), song); err == nil {
		t.Fatal("a missing ffmpeg should fail the hook")
	}
}
