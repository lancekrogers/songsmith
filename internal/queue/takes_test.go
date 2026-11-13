package queue

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/master"
)

func TestRenderRecordsATakeAndKeepsMasters(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	song := create(t, store, "Song", library.StatusQueued)
	first := q.Render(t.Context(), song)
	// A master made after the first render, which a stale copy of the song
	// does not know about.
	if _, err := store.AddTake(song.ID, library.Take{File: "song.master-1.flac", Kind: library.TakeMaster, Source: "song.flac"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(store.TakePath(song, "song.master-1.flac"), []byte("m"), 0o644)
	// Queued in place with its audio, as a build before new versions left it;
	// Enqueue now makes a new version instead.
	first.Status = library.StatusQueued
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	second := q.Render(t.Context(), first)
	if second.Audio != "song-2.flac" {
		t.Fatalf("new take = %q", second.Audio)
	}
	var files []string
	for _, tk := range store.Takes(second) {
		files = append(files, tk.File+":"+string(tk.Kind))
	}
	if strings.Join(files, " ") != "song.flac:render song.master-1.flac:master song-2.flac:render" {
		t.Fatalf("takes = %v", files)
	}
}

func TestEnqueueKeepsTheCurrentTakeChosenMeanwhile(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	song := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	stale := song
	os.WriteFile(store.TakePath(song, "song.master-1.flac"), []byte("m"), 0o644)
	if _, err := store.SetCurrent(song.ID, "song.master-1.flac"); err != nil {
		t.Fatal(err)
	}
	stale.Lyrics += "\nmore"
	if err := q.SaveEdit(stale); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load(song.ID)
	if saved.Audio != "song.master-1.flac" || !strings.HasSuffix(saved.Lyrics, "more") {
		t.Fatalf("saved %+v", saved)
	}
}

func TestAfterRenderMastersAndMakesItCurrent(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	calls := 0
	q.AfterRender(func(ctx context.Context, song library.Song) (library.Song, error) {
		calls++
		if song.Status != library.StatusDone || song.Audio != "song.flac" {
			t.Fatalf("hook got %+v", song)
		}
		run := func(_ context.Context, _, out string, _ master.Settings, _ func(float64)) (master.Measure, error) {
			return master.Measure{Duration: 60}, os.WriteFile(out, []byte("m"), 0o644)
		}
		take, _, err := store.MasterTake(ctx, song.ID, song.Audio, master.Settings{Loudness: -14}, run, nil)
		if err != nil {
			return song, err
		}
		return store.SetCurrent(song.ID, take.File)
	})
	got := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	if calls != 1 || got.Audio != "song.master-1.flac" || got.Status != library.StatusDone || got.Error != "" {
		t.Fatalf("song = %+v", got)
	}
	takes := store.Takes(got)
	if len(takes) != 2 || takes[0].File != "song.flac" || takes[1].Kind != library.TakeMaster {
		t.Fatalf("takes = %+v", takes)
	}
}

func TestAfterRenderFailureNeverFailsTheRender(t *testing.T) {
	q, store := setup(t, &fakeBackend{})
	q.AfterRender(func(context.Context, library.Song) (library.Song, error) {
		return library.Song{}, errors.New("ffmpeg is not installed")
	})
	got := q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	saved, _ := store.Load(got.ID)
	if saved.Status != library.StatusDone || saved.Audio != "song.flac" || !strings.Contains(saved.Error, "ffmpeg is not installed") || !strings.Contains(saved.Error, "raw take is current") {
		t.Fatalf("saved %+v", saved)
	}

	b := &fakeBackend{err: errors.New("boom")}
	q, store = setup(t, b)
	ran := false
	q.AfterRender(func(context.Context, library.Song) (library.Song, error) { ran = true; return library.Song{}, nil })
	q.Render(t.Context(), create(t, store, "Song", library.StatusQueued))
	if ran {
		t.Fatal("the hook ran after a failed render")
	}
}
