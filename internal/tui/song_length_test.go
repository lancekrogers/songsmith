package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/library"
)

func TestLongSongWarningThresholds(t *testing.T) {
	sung := func(chars int) string { return "[Verse]\n" + strings.Repeat("x", chars) }
	cases := []struct {
		name          string
		auto, drafted bool
		seconds       float64
		lyrics        string
		want          bool
	}{
		{"3:00 exactly", false, false, 180, sung(100), false},
		{"3:05", false, false, 185, sung(100), true},
		{"auto with no length yet", true, false, 120, sung(100), false},
		{"auto with a long drafted length", true, true, 200, sung(100), true},
		{"short length, long lyrics", false, false, 120, sung(1251), true},
		{"lyrics at the threshold", false, false, 120, sung(1250), false},
		{"tags do not count", false, false, 120, strings.Repeat("[Verse]\n", 300) + "la", false},
	}
	for _, c := range cases {
		m, _ := openNewSong(t, fakeDrafter{})
		m.compose.autoLength, m.compose.draftedLength, m.compose.seconds = c.auto, c.drafted, c.seconds
		m.compose.texts[0] = c.lyrics
		if got := m.compose.longSong(); got != c.want {
			t.Errorf("%s: warning %v", c.name, got)
		}
		view := strings.Join(strings.Fields(plain(m)), " ")
		if shown := strings.Contains(view, "Songs over about 3:00 have come out with static on this setup."); shown != c.want {
			t.Errorf("%s: shown %v:\n%s", c.name, shown, plain(m))
		}
	}
}

func TestLongSongWarningWrapsAndNeverBlocks(t *testing.T) {
	for _, k := range []string{"ctrl+s", "ctrl+d"} {
		m, store := openNewSong(t, fakeDrafter{})
		m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		m = fillValid(m)
		m.compose.autoLength, m.compose.seconds = false, 240
		view := m.View()
		flat := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(view), "│", " ")), " ")
		if !strings.Contains(flat, longSongWarning) {
			t.Fatalf("warning cut short:\n%s", ansi.Strip(view))
		}
		for _, l := range strings.Split(view, "\n") {
			if ansi.StringWidth(l) > 80 {
				t.Fatalf("line too wide: %q", ansi.Strip(l))
			}
		}
		m, cmd := send(t, m, key(k))
		m = runCmd(t, m, cmd)
		songs, _ := store.List()
		want := map[string]library.Status{"ctrl+s": library.StatusQueued, "ctrl+d": library.StatusDraft}[k]
		if len(songs) != 1 || songs[0].Status != want || songs[0].Seconds != 240 {
			t.Fatalf("%s: songs %+v", k, songs)
		}
	}
}
