package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/library"
)

// libKey sends k and runs the command it returns, as the program would.
func libKey(t *testing.T, a app, k string) app {
	t.Helper()
	a, cmd := send(t, a, key(k))
	return runCmd(t, a, cmd)
}

// libraryWith makes a library of songs with these titles, newest last, and
// selects the first.
func libraryWith(t *testing.T, titles ...string) (app, *library.Store, []library.Song) {
	t.Helper()
	a, store := newTestModel(t, fakeDrafter{})
	var songs []library.Song
	for _, title := range titles {
		s, err := store.Create(library.Song{Title: title, Lyrics: sampleDraft.Lyrics, Status: library.StatusDone, Audio: "song.flac"})
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(store.AudioPath(s), []byte("audio"), 0o644)
		songs = append(songs, s)
	}
	a = runCmd(t, a, a.loadSongs())
	a = selectSong(t, a, songs[0].ID)
	return a, store, songs
}

func selectSong(t *testing.T, a app, id string) app {
	t.Helper()
	a.library.selectID = id
	a.selectPending()
	if s, ok := a.selected(); !ok || s.ID != id {
		t.Fatalf("could not select %s", id)
	}
	return a
}

// listRows is the text of the list pane, without the detail pane.
func listRows(a app) string {
	var out []string
	for _, line := range strings.Split(plain(a), "\n") {
		if left, _, ok := strings.Cut(line, "││"); ok {
			out = append(out, left)
		}
	}
	return strings.Join(out, "\n")
}

func TestMovingSongsIntoANewFolderAndThenAnExistingOne(t *testing.T) {
	a, store, songs := libraryWith(t, "Stolen Future", "Stolen Future (v2)", "Sock Thief")
	a = libKey(t, a, "f")
	if a.library.picker == nil || !strings.Contains(plain(a), "Move Stolen Future to") || !strings.Contains(plain(a), "+ New folder…") {
		t.Fatalf("no picker:\n%s", plain(a))
	}
	a = libKey(t, a, "up")
	a, _ = send(t, a, key("enter"))
	if a.library.naming == nil {
		t.Fatalf("enter on new folder did not ask for a name:\n%s", plain(a))
	}
	a, _ = send(t, a, key("Stolen Future"))
	a = libKey(t, a, "enter")
	if _, err := os.Stat(filepath.Join(store.Dir(), "Stolen Future", songs[0].ID, "song.flac")); err != nil {
		t.Fatalf("song not moved with its audio: %v\n%s", err, plain(a))
	}
	if !strings.Contains(a.notice, "Moved Stolen Future to Stolen Future") {
		t.Fatalf("notice = %q", a.notice)
	}
	if got, _ := a.selected(); got.ID == songs[0].ID {
		t.Fatal("the cursor stayed on the moved song instead of moving on")
	}
	rows := listRows(a)
	if !strings.Contains(rows, "▸ Stolen Future/") || !strings.Contains(rows, "1 song") {
		t.Fatalf("folder row missing:\n%s", rows)
	}

	a = selectSong(t, a, songs[1].ID)
	a = libKey(t, a, "f")
	for a.library.picker.choices[a.library.picker.cursor] != "Stolen Future" {
		a = libKey(t, a, "down")
	}
	a = libKey(t, a, "enter")
	if got, _ := store.Load(songs[1].ID); got.Folder != "Stolen Future" {
		t.Fatalf("v2 folder = %q", got.Folder)
	}
	if !strings.Contains(listRows(a), "2 songs") {
		t.Fatalf("folder count:\n%s", listRows(a))
	}
}

func TestFoldersOpenCloseAndHoldTheirSongs(t *testing.T) {
	a, store, songs := libraryWith(t, "Inside", "Outside")
	if _, err := store.Move(songs[0].ID, "Album"); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	if strings.Contains(listRows(a), "Inside") {
		t.Fatalf("a closed folder shows its songs:\n%s", listRows(a))
	}
	if f, ok := a.selectedFolder(); !ok || f != "Album" {
		t.Fatalf("selection did not follow the song into its closed folder: %v %q", ok, f)
	}
	if !strings.Contains(plain(a), "enter open") {
		t.Fatalf("folder footer:\n%s", plain(a))
	}
	a = libKey(t, a, "enter")
	if !strings.Contains(listRows(a), "▾ Album/") || !strings.Contains(listRows(a), "Inside") {
		t.Fatalf("open folder:\n%s", listRows(a))
	}
	a = libKey(t, a, "down")
	if s, _ := a.selected(); s.ID != songs[0].ID {
		t.Fatalf("selected %+v", s)
	}
	a = libKey(t, a, "left")
	if f, ok := a.selectedFolder(); !ok || f != "Album" {
		t.Fatal("left from a song did not go up to its folder")
	}
	a = libKey(t, a, "left")
	if strings.Contains(listRows(a), "Inside") {
		t.Fatal("left on an open folder did not close it")
	}

	// Jumping to a song from elsewhere opens the folders it is in.
	a, _ = send(t, a, openLibraryMsg{songID: songs[0].ID})
	if s, ok := a.selected(); !ok || s.ID != songs[0].ID {
		t.Fatalf("jump to a song in a closed folder selected %+v", s)
	}
}

func TestDeletingASongAsksFirstAndAnyOtherKeyKeepsIt(t *testing.T) {
	a, store, songs := libraryWith(t, "Doomed", "Other")
	cursor := a.library.cursor
	a = libKey(t, a, "d")
	if a.library.confirm == nil || !strings.Contains(plain(a), "Delete “Doomed” and its 1 take for good?") {
		t.Fatalf("no confirmation:\n%s", plain(a))
	}
	if !strings.Contains(plain(a), "y delete") {
		t.Fatalf("footer:\n%s", plain(a))
	}
	a = libKey(t, a, "down")
	if a.library.confirm != nil || a.library.cursor != cursor {
		t.Fatal("another key did not just cancel the delete")
	}
	if _, err := store.Load(songs[0].ID); err != nil {
		t.Fatal("cancelled delete removed the song")
	}

	// The list reloading while it asks does not change what is deleted.
	a = libKey(t, a, "d")
	if _, err := store.Create(library.Song{Title: "Newest"}); err != nil {
		t.Fatal(err)
	}
	a = runCmd(t, a, a.loadSongs())
	a = libKey(t, a, "y")
	if _, err := store.Load(songs[0].ID); err == nil {
		t.Fatal("y did not delete")
	}
	if left, _ := store.List(); len(left) != 2 {
		t.Fatalf("left = %v", left)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), songs[0].ID)); !os.IsNotExist(err) {
		t.Fatal("song folder still on disk")
	}
	if strings.Contains(listRows(a), "Doomed") || a.notice != "Deleted Doomed." {
		t.Fatalf("notice %q:\n%s", a.notice, listRows(a))
	}
}

func TestBusySongsCannotBeMovedOrDeleted(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	song, _ := store.Create(library.Song{Title: "Busy", Status: library.StatusQueued, Lyrics: sampleDraft.Lyrics})
	a = runCmd(t, a, a.loadSongs())
	for _, k := range []string{"d", "f"} {
		a = libKey(t, a, k)
		if a.library.confirm != nil || a.library.picker != nil || !strings.Contains(a.notice, "x cancels it") {
			t.Fatalf("%s on a queued song: notice %q", k, a.notice)
		}
	}
	a.mastering.running[song.ID] = &masterJob{}
	song.Status = library.StatusDone
	a.library.songs[0].Status = library.StatusDone
	a = libKey(t, a, "d")
	if a.library.confirm != nil || !strings.Contains(a.notice, "being mastered") {
		t.Fatalf("delete while mastering: %q", a.notice)
	}
}

func TestDeletingAndMovingStopsPlaybackOfThatSong(t *testing.T) {
	a, _, songs := libraryWith(t, "Playing", "Quiet")
	player, _ := fakesOf(a)
	a = libKey(t, a, "enter")
	if player.Current() == "" {
		t.Fatal("not playing")
	}
	a = libKey(t, a, "f")
	a = libKey(t, a, "up")
	a, _ = send(t, a, key("enter"), key("Live"))
	a = libKey(t, a, "enter")
	if player.Current() != "" {
		t.Fatal("still playing a file that moved")
	}

	a = selectSong(t, a, songs[1].ID)
	a = libKey(t, a, "enter")
	a = libKey(t, a, "d")
	a = libKey(t, a, "y")
	if player.Current() != "" {
		t.Fatal("still playing a deleted song")
	}
}

func TestDeletingAFolderNamesItsSongsAndRefusesIfItChanged(t *testing.T) {
	a, store, songs := libraryWith(t, "One", "Two", "Stay")
	store.Move(songs[0].ID, "Album")
	store.Move(songs[1].ID, "Album/Disc 2")
	a = runCmd(t, a, a.loadSongs())
	if f, _ := a.selectedFolder(); f != "Album" {
		t.Fatalf("selected folder %q", f)
	}
	a = libKey(t, a, "d")
	if !strings.Contains(a.notice, "Delete the folder “Album” and the 2 songs in it") {
		t.Fatalf("notice = %q", a.notice)
	}
	// A song lands in the folder before the answer.
	extra, _ := store.Create(library.Song{Title: "Late", Folder: "Album"})
	a = libKey(t, a, "y")
	if !strings.Contains(a.notice, "changed since you asked") {
		t.Fatalf("notice = %q", a.notice)
	}
	if _, err := store.Load(extra.ID); err != nil {
		t.Fatal("deleted a folder that changed after the question")
	}
	a = libKey(t, a, "d")
	a = libKey(t, a, "y")
	if left, _ := store.List(); len(left) != 1 || left[0].ID != songs[2].ID {
		t.Fatalf("left = %v (%q)", left, a.notice)
	}
	if folders, _ := store.Folders(); len(folders) != 0 {
		t.Fatalf("folders = %v", folders)
	}
}

func TestRenamingAndMovingFolders(t *testing.T) {
	a, store, songs := libraryWith(t, "One", "Two")
	store.Move(songs[0].ID, "Demos")
	store.MakeFolder("Albums")
	a = runCmd(t, a, a.loadSongs())
	a.rebuildRows("", "Demos")

	a, _ = send(t, a, key("e"))
	if a.library.naming == nil || a.library.naming.input.Value() != "Demos" {
		t.Fatal("e on a folder did not offer its name")
	}
	a.library.naming.input.SetValue("../x")
	a, _ = send(t, a, key("enter"))
	if a.library.naming == nil || a.library.naming.err == "" {
		t.Fatal("a bad name was not refused in place")
	}
	a.library.naming.input.SetValue("Early Demos")
	a = libKey(t, a, "enter")
	if got, _ := store.Load(songs[0].ID); got.Folder != "Early Demos" {
		t.Fatalf("after rename folder = %q (%s)", got.Folder, a.err)
	}
	if f, _ := a.selectedFolder(); f != "Early Demos" {
		t.Fatalf("selection after rename = %q", f)
	}

	a = libKey(t, a, "f")
	for a.library.picker.choices[a.library.picker.cursor] != "Albums" {
		a = libKey(t, a, "down")
	}
	a = libKey(t, a, "enter")
	if got, _ := store.Load(songs[0].ID); got.Folder != "Albums/Early Demos" {
		t.Fatalf("after move folder = %q (%s)", got.Folder, a.err)
	}
	if f, _ := a.selectedFolder(); f != "Albums/Early Demos" {
		t.Fatalf("moved folder not selected and revealed: %q", f)
	}
}

func TestNewSongFromAFolderIsWrittenThere(t *testing.T) {
	a, store, songs := libraryWith(t, "One")
	store.Move(songs[0].ID, "Album")
	a = runCmd(t, a, a.loadSongs())
	_, cmd := send(t, a, key("n"))
	msg, ok := cmd().(openComposeMsg)
	if !ok || msg.song == nil || msg.song.Folder != "Album" || msg.song.ID != "" {
		t.Fatalf("n on a folder opened %+v", msg)
	}
	created, err := store.Create(*msg.song)
	if err != nil || created.Folder != "Album" {
		t.Fatalf("created %+v, %v", created, err)
	}
	var _ tea.Msg = msg
}

func TestAnUntitledSongIsNamedInTheQuestionAndTheAnswer(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	store.Create(library.Song{Idea: "a lighthouse keeper's last night"})
	a = runCmd(t, a, a.loadSongs())
	a = libKey(t, a, "d")
	if !strings.Contains(a.notice, "Delete “Untitled” for good?") {
		t.Fatalf("question = %q", a.notice)
	}
	a = libKey(t, a, "y")
	if a.notice != "Deleted Untitled." {
		t.Fatalf("answer = %q", a.notice)
	}
}
