package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/library"
)

func footerLine(a app) string {
	lines := strings.Split(plain(a), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func TestLibraryFooterListsOnlyKeysForTheSelectedSong(t *testing.T) {
	cases := []struct {
		status library.Status
		want   string
	}{
		{library.StatusDraft, "r render   e edit   v revise / extend   n new song   esc back"},
		{library.StatusFailed, "r render   e edit   v revise / extend   n new song   esc back"},
		{library.StatusQueued, "x cancel   e edit   v revise / extend   n new song   esc back"},
		{library.StatusRendering, "x cancel   e edit   v revise / extend   n new song   esc back"},
		{library.StatusDone, "enter play   r new version   e edit   v revise / extend   n new song   esc back"},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			a, store := newTestModel(t, fakeDrafter{})
			song, err := store.Create(library.Song{Title: "One", Status: c.status, Lyrics: sampleDraft.Lyrics})
			if err != nil {
				t.Fatal(err)
			}
			if c.status == library.StatusDone {
				if err := os.WriteFile(store.AudioPath(song), []byte("audio"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			a = runCmd(t, a, a.loadSongs())
			if got := footerLine(a); got != c.want {
				t.Fatalf("footer = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLibraryFooterOnEmptyAndLongLists(t *testing.T) {
	a, store := newTestModel(t, fakeDrafter{})
	if got := footerLine(a); got != "n new song   esc back" {
		t.Fatalf("empty footer = %q", got)
	}
	for _, title := range []string{"One", "Two"} {
		if _, err := store.Create(library.Song{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	a = runCmd(t, a, a.loadSongs())
	if got := footerLine(a); !strings.HasPrefix(got, "↑/↓ select   r render") {
		t.Fatalf("footer = %q", got)
	}
}

func TestLibraryEscReturnsToLauncher(t *testing.T) {
	a, _ := newTestModel(t, fakeDrafter{})
	a.err = "render failed: boom"
	a = nav(t, a, "esc")
	if a.screen != screenLauncher || a.err != "" {
		t.Fatalf("screen %v err %q", a.screen, a.err)
	}
}
