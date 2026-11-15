package library

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func mustCreate(t *testing.T, s *Store, song Song) Song {
	t.Helper()
	song, err := s.Create(song)
	if err != nil {
		t.Fatal(err)
	}
	return song
}

func TestSongsInFoldersListLoadAndKeepTheirID(t *testing.T) {
	s := NewStore(t.TempDir())
	top := mustCreate(t, s, Song{Title: "Top"})
	deep := mustCreate(t, s, Song{Title: "Deep", Folder: "Albums/Demos"})
	if _, err := s.MakeFolder("Empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "Albums", "Demos", deep.ID, songFile)); err != nil {
		t.Fatalf("song not in its folder: %v", err)
	}
	songs, err := s.List()
	if err != nil || len(songs) != 2 {
		t.Fatalf("list = %v, %v", songs, err)
	}
	for _, song := range songs {
		want := map[string]string{top.ID: "", deep.ID: "Albums/Demos"}[song.ID]
		if song.Folder != want {
			t.Errorf("%s folder = %q, want %q", song.Title, song.Folder, want)
		}
	}
	folders, _ := s.Folders()
	if !slices.Equal(folders, []string{"Albums", "Albums/Demos", "Empty"}) {
		t.Fatalf("folders = %v", folders)
	}
	got, err := NewStore(s.Dir()).Load(deep.ID)
	if err != nil || got.Folder != "Albums/Demos" {
		t.Fatalf("a fresh store loads %+v, %v", got, err)
	}
}

func TestMoveKeepsTheIDAndAnotherProcessFindsTheSong(t *testing.T) {
	s := NewStore(t.TempDir())
	other := NewStore(s.Dir())
	song := mustCreate(t, s, Song{Title: "Song", Status: StatusDone, Audio: "song.flac"})
	os.WriteFile(s.AudioPath(song), []byte("audio"), 0o644)
	if _, err := other.Load(song.ID); err != nil {
		t.Fatal(err)
	}

	moved, err := s.Move(song.ID, "Keepers")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != song.ID || moved.Folder != "Keepers" {
		t.Fatalf("moved = %+v", moved)
	}
	if !s.HasAudio(moved) || filepath.Dir(s.AudioPath(moved)) != filepath.Join(s.Dir(), "Keepers", song.ID) {
		t.Fatalf("audio at %s", s.AudioPath(moved))
	}
	// The other store last saw the song at the top level.
	got, err := other.Load(song.ID)
	if err != nil || got.Folder != "Keepers" {
		t.Fatalf("other store loads %+v, %v", got, err)
	}
	got.Lyrics = "[Verse]\nedited"
	if err := other.Save(got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), song.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a save after the move recreated the song at its old place")
	}
	back, err := s.Move(song.ID, "")
	if err != nil || back.Folder != "" || !s.HasAudio(back) {
		t.Fatalf("move back = %+v, %v", back, err)
	}
}

func TestBusySongsStayPut(t *testing.T) {
	for _, status := range []Status{StatusQueued, StatusRendering} {
		s := NewStore(t.TempDir())
		song := mustCreate(t, s, Song{Title: "Busy", Status: status, Folder: "Album"})
		if _, err := s.Move(song.ID, ""); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: move err = %v", status, err)
		}
		if err := s.Delete(song.ID); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: delete err = %v", status, err)
		}
		if _, err := s.DeleteFolder("Album"); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: delete folder err = %v", status, err)
		}
		if _, err := s.MoveFolder("Album", "Renamed"); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: rename folder err = %v", status, err)
		}
		if _, err := s.Load(song.ID); err != nil {
			t.Errorf("%s: song gone: %v", status, err)
		}
	}
}

func TestDeleteRemovesTheSongForGoodAndALateSaveCannotBringItBack(t *testing.T) {
	s := NewStore(t.TempDir())
	keep := mustCreate(t, s, Song{Title: "Keep"})
	song := mustCreate(t, s, Song{Title: "Gone", Folder: "Album"})
	if err := s.Delete(song.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(song); !errors.Is(err, ErrNoSong) {
		t.Fatalf("save after delete err = %v", err)
	}
	if _, err := s.Update(song.ID, func(*Song) error { return nil }); err == nil {
		t.Fatal("update after delete succeeded")
	}
	songs, _ := s.List()
	if len(songs) != 1 || songs[0].ID != keep.ID {
		t.Fatalf("list = %v", songs)
	}
	if fi, err := os.Stat(filepath.Join(s.Dir(), "Album")); err != nil || !fi.IsDir() {
		t.Fatal("deleting a song removed its folder")
	}
}

func TestDeleteRefusesAnythingThatIsNotASong(t *testing.T) {
	s := NewStore(t.TempDir())
	mustCreate(t, s, Song{Title: "Song", Folder: "Album"})
	for _, id := range []string{"", ".", "..", "Album", "../x", "Album/x", "/"} {
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete(%q) succeeded", id)
		}
	}
	if _, err := os.Stat(s.Dir()); err != nil {
		t.Fatal("library removed")
	}
	if songs, _ := s.List(); len(songs) != 1 {
		t.Fatalf("songs = %v", songs)
	}
}

func TestFoldersMoveRenameAndDelete(t *testing.T) {
	s := NewStore(t.TempDir())
	a := mustCreate(t, s, Song{Title: "A", Folder: "Album"})
	b := mustCreate(t, s, Song{Title: "B", Folder: "Album/Disc 2"})
	mustCreate(t, s, Song{Title: "C"})

	if _, err := s.MoveFolder("Album", "Album/Disc 2/Album"); err == nil {
		t.Fatal("moved a folder inside itself")
	}
	to, err := s.MoveFolder("Album", "Archive/2026 Album")
	if err != nil || to != "Archive/2026 Album" {
		t.Fatalf("move folder = %q, %v", to, err)
	}
	if got, _ := s.Load(b.ID); got.Folder != "Archive/2026 Album/Disc 2" {
		t.Fatalf("b folder = %q", got.Folder)
	}
	mustCreate(t, s, Song{Title: "D", Folder: "Taken"})
	if _, err := s.MoveFolder("Archive", "Taken"); err == nil {
		t.Fatal("renamed a folder over another")
	}
	for _, path := range []string{"", "/", "..", "Nope", a.ID} {
		if _, err := s.DeleteFolder(path); err == nil {
			t.Errorf("DeleteFolder(%q) succeeded", path)
		}
	}
	n, err := s.DeleteFolder("Archive")
	if err != nil || n != 2 {
		t.Fatalf("delete folder = %d, %v", n, err)
	}
	songs, _ := s.List()
	if len(songs) != 2 {
		t.Fatalf("songs left = %v", songs)
	}
}

func TestFolderNamesAndPaths(t *testing.T) {
	s := NewStore(t.TempDir())
	song := mustCreate(t, s, Song{Title: "Song"})
	for in, want := range map[string]string{" Albums / Demos ": "Albums/Demos", "/x/": "x", "": ""} {
		if got, err := CleanFolder(in); err != nil || got != want {
			t.Errorf("CleanFolder(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"a//b", "..", "a/../b", ".hidden", "a/.b"} {
		if _, err := CleanFolder(bad); err == nil {
			t.Errorf("CleanFolder(%q) accepted", bad)
		}
	}
	if _, err := s.MakeFolder(song.ID + "/inside"); err == nil {
		t.Fatal("made a folder inside a song")
	}
	if _, err := s.Move(song.ID, song.ID); err == nil {
		t.Fatal("moved a song into itself")
	}
}

func TestNewVersionsStayInTheSourcesFolder(t *testing.T) {
	s := NewStore(t.TempDir())
	src := mustCreate(t, s, Song{Title: "Song", Folder: "Album"})
	src.Folder = "" // a copy that does not know where it lives
	v, err := s.NewVersion(src)
	if err != nil || v.Folder != "Album" {
		t.Fatalf("version = %+v, %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "Album", v.ID, songFile)); err != nil {
		t.Fatal(err)
	}
}
