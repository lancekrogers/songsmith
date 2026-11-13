package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/master"
)

func writeTake(t *testing.T, s *Store, song Song, name string, at time.Time) {
	t.Helper()
	p := s.TakePath(song, name)
	if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestTakesDiscoversFilesOfOlderSongs(t *testing.T) {
	s := NewStore(t.TempDir())
	song, err := s.Create(Song{Title: "Old", Status: StatusDone, Audio: "song-2.flac", Duration: 150})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeTake(t, s, song, "song.flac", base)
	writeTake(t, s, song, "song-2.flac", base.Add(time.Hour))
	writeTake(t, s, song, "song-2.master-1.flac", base.Add(2*time.Hour))
	writeTake(t, s, song, "notes.txt", base)
	writeTake(t, s, song, ".master-123.flac", base)

	takes := s.Takes(song)
	var got []string
	for _, tk := range takes {
		got = append(got, tk.File+":"+string(tk.Kind)+":"+tk.Source)
	}
	if strings.Join(got, " ") != "song.flac:render: song-2.flac:render: song-2.master-1.flac:master:song-2.flac" {
		t.Fatalf("takes = %v", got)
	}
	if takes[1].Duration != 150 || takes[0].Duration != 0 {
		t.Fatalf("the current take should carry the song's length: %+v", takes)
	}
	if i := TakeIndex(takes, "song-2.flac"); i != 2 {
		t.Fatalf("index = %d", i)
	}
}

func TestNextMasterNeverCollides(t *testing.T) {
	s := NewStore(t.TempDir())
	song, _ := s.Create(Song{Title: "Song"})
	if got := s.NextMaster(song, "song-2.flac"); got != "song-2.master-1.flac" {
		t.Fatalf("next = %q", got)
	}
	writeTake(t, s, song, "song-2.master-1.flac", time.Now())
	if got := s.NextMaster(song, "song-2.flac"); got != "song-2.master-2.flac" {
		t.Fatalf("next = %q", got)
	}
	if got := s.NextMaster(song, "song.flac"); got != "song.master-1.flac" || renderFile.MatchString(got) {
		t.Fatalf("next = %q", got)
	}
}

func TestMasterTakeRecordsAndKeepsCurrent(t *testing.T) {
	s := NewStore(t.TempDir())
	song, _ := s.Create(Song{Title: "Song", Status: StatusDone, Audio: "song.flac", Duration: 100})
	writeTake(t, s, song, "song.flac", time.Now().Add(-time.Hour))
	set := master.Settings{Loudness: -14, Ceiling: -1, LowCut: 30}
	run := func(_ context.Context, in, out string, got master.Settings, progress func(float64)) (master.Measure, error) {
		if filepath.Base(in) != "song.flac" || got != set {
			t.Fatalf("run %s %+v", in, got)
		}
		progress(1)
		return master.Measure{Loudness: -14.1, Duration: 98}, os.WriteFile(out, []byte("m"), 0o644)
	}
	take, saved, err := s.MasterTake(t.Context(), song.ID, "song.flac", set, run, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if take.File != "song.master-1.flac" || take.Source != "song.flac" || take.Loudness != -14.1 || take.Master == nil || *take.Master != set {
		t.Fatalf("take = %+v", take)
	}
	if saved.Audio != "song.flac" {
		t.Fatalf("mastering changed the current take to %q", saved.Audio)
	}
	if take.Label() != "mastered (Streaming)" {
		t.Fatalf("label = %q", take.Label())
	}

	cur, err := s.SetCurrent(song.ID, take.File)
	if err != nil || cur.Audio != take.File || cur.Duration != 98 {
		t.Fatalf("set current: %+v %v", cur, err)
	}
	if _, err := s.SetCurrent(song.ID, "nope.flac"); !errors.Is(err, ErrNotATake) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := s.MasterTake(t.Context(), song.ID, "missing.flac", set, run, nil); !errors.Is(err, ErrNotATake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveKeepsTakeRecords(t *testing.T) {
	s := NewStore(t.TempDir())
	song, _ := s.Create(Song{Title: "Song"})
	stale := song
	if _, err := s.AddTake(song.ID, Take{File: "song.master-1.flac", Kind: TakeMaster}); err != nil {
		t.Fatal(err)
	}
	stale.Lyrics = "[Verse]\nnew"
	if err := s.Save(stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load(song.ID)
	if len(got.Takes) != 1 || got.Lyrics != stale.Lyrics {
		t.Fatalf("saved %+v", got)
	}
}

func TestSongsWithoutTakesStillLoad(t *testing.T) {
	s := NewStore(t.TempDir())
	dir := filepath.Join(s.Dir(), "old")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "song.json"), []byte(`{"id":"old","title":"Old","status":"done","audio":"song.flac"}`), 0o644)
	song, err := s.Load("old")
	if err != nil || song.Takes != nil {
		t.Fatalf("%+v %v", song, err)
	}
	data, _ := json.Marshal(Song{ID: "x"})
	if strings.Contains(string(data), "takes") {
		t.Fatal("takes should be omitted when empty")
	}
}
