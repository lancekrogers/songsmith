package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/writer"
)

func TestNewID(t *testing.T) {
	at := time.Date(2026, 11, 12, 19, 5, 9, 0, time.UTC)
	cases := map[string]string{
		"Salt & Grain!":                   "20261112-190509-salt-grain",
		"":                                "20261112-190509-untitled",
		"  --- ":                          "20261112-190509-untitled",
		strings.Repeat("long title ", 10): "20261112-190509-long-title-long-title-long-title-long-ti",
	}
	for in, want := range cases {
		if got := NewID(in, at); got != want {
			t.Errorf("NewID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreRoundTripAndOrder(t *testing.T) {
	s := NewStore(t.TempDir())
	clock := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }

	first, err := s.Create(Song{Title: "First", Lyrics: "[Verse]\nla", Caption: writer.Caption{GlobalMetadata: "pop"}, Seconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusDraft {
		t.Fatalf("status = %s", first.Status)
	}
	second, err := s.Create(Song{Title: "Second", Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "First" || got.Caption.GlobalMetadata != "pop" || got.Seconds != 60 || got.Updated.IsZero() {
		t.Fatalf("loaded %+v", got)
	}

	songs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 2 || songs[0].ID != second.ID {
		t.Fatalf("list order = %v", songs)
	}
	if s.AudioPath(got) != filepath.Join(s.Dir(), got.ID, "song.flac") {
		t.Fatalf("audio path = %s", s.AudioPath(got))
	}
}

func TestCreateAvoidsCollisions(t *testing.T) {
	s := NewStore(t.TempDir())
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	a, _ := s.Create(Song{Title: "Same"})
	b, err := s.Create(Song{Title: "Same"})
	if err != nil || a.ID == b.ID {
		t.Fatalf("ids %s %s err %v", a.ID, b.ID, err)
	}
}

func TestListSkipsCorruptSongs(t *testing.T) {
	s := NewStore(t.TempDir())
	good, _ := s.Create(Song{Title: "Good"})
	bad := filepath.Join(s.Dir(), "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, songFile), []byte("{not json"), 0o644)
	os.MkdirAll(filepath.Join(s.Dir(), "empty"), 0o755)

	songs, err := s.List()
	if len(songs) != 1 || songs[0].ID != good.ID {
		t.Fatalf("songs = %v", songs)
	}
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v", err)
	}
}

func TestListMissingLibrary(t *testing.T) {
	songs, err := NewStore(filepath.Join(t.TempDir(), "none")).List()
	if err != nil || songs != nil {
		t.Fatalf("songs %v err %v", songs, err)
	}
}

func TestTryLockIsExclusive(t *testing.T) {
	s := NewStore(t.TempDir())
	l, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TryLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock err = %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l2, err := s.TryLock()
	if err != nil {
		t.Fatalf("relock after release: %v", err)
	}
	l2.Release()
}
