package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/library"
)

func TestTheDetailPaneShowsTheSeed(t *testing.T) {
	cases := []struct {
		song library.Song
		want string
	}{
		{library.Song{Title: "Pinned", Seed: 7, SeedPinned: true}, "Seed 7 · pinned"},
		{library.Song{Title: "Pinned zero", SeedPinned: true}, "Seed 0 · pinned"},
		{library.Song{Title: "Rendered", Seed: 281474976710, Status: library.StatusDone}, "Seed 281474976710 · random"},
		{library.Song{Title: "Never rendered"}, "Seed · chosen at render"},
	}
	for _, c := range cases {
		m, store := newTestModel(t, fakeDrafter{})
		if _, err := store.Create(c.song); err != nil {
			t.Fatal(err)
		}
		m = runCmd(t, m, m.loadSongs())
		lines := strings.Split(ansi.Strip(m.detailText(80)), "\n")
		found := false
		for _, l := range lines {
			found = found || strings.TrimSpace(l) == c.want
		}
		if !found {
			t.Errorf("%s: want the line %q in\n%s", c.song.Title, c.want, strings.Join(lines, "\n"))
		}
	}
}

func TestTheDetailPaneNamesTheSourceOfACopy(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	src, err := store.Create(library.Song{Title: "Original", Seed: 42, Status: library.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	same, _ := store.Create(library.Song{Title: "Same seed", Seed: 42, SeedPinned: true, DerivedFrom: src.ID})
	other, _ := store.Create(library.Song{Title: "New seed", Seed: 9, DerivedFrom: src.ID})
	gone, _ := store.Create(library.Song{Title: "Orphan", DerivedFrom: "20200101-000000-deleted"})
	m = runCmd(t, m, m.loadSongs())
	detail := func(id string) string {
		m.library.selectID = id
		m.selectPending()
		lines := strings.Split(ansi.Strip(m.detailText(120)), "\n")
		for i, l := range lines {
			lines[i] = strings.TrimRight(l, " ")
		}
		return strings.Join(lines, "\n")
	}
	if d := detail(same.ID); !strings.Contains(d, "\nfrom Original\n") || !strings.Contains(d, "Seed 42 · pinned · from Original") {
		t.Fatalf("same seed:\n%s", d)
	}
	if d := detail(other.ID); !strings.Contains(d, "from Original") || !strings.Contains(d, "Seed 9 · random\n") {
		t.Fatalf("new seed:\n%s", d)
	}
	if d := detail(gone.ID); !strings.Contains(d, "from a song no longer in the library") {
		t.Fatalf("deleted source:\n%s", d)
	}
}

func TestTheSeedRowPinsAndUnpinsTheLastSeed(t *testing.T) {
	m, _ := openNewSong(t, fakeDrafter{})
	m.compose.focusOn(rowSeed)
	if !strings.Contains(plain(m), "Seed     random each render") || strings.Contains(plain(m), "last used") || strings.Contains(footerLines(m), "p ") {
		t.Fatalf("new song:\n%s", plain(m))
	}
	m = press(t, m, "p")
	if m.compose.seed.Value() != "" || m.compose.dirty() {
		t.Fatalf("p on a new song: %q", m.compose.seed.Value())
	}

	m, store := newTestModel(t, fakeDrafter{})
	if _, err := store.Create(library.Song{Title: "Rendered", Lyrics: sampleDraft.Lyrics, Seed: 281474976710, Status: library.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	m.compose.focusOn(rowSeed)
	if !strings.Contains(plain(m), "random each render · last used 281474976710") || !strings.Contains(footerLines(m), "p pin seed 281474976710") {
		t.Fatalf("unpinned:\n%s", plain(m))
	}
	m = press(t, m, "p")
	if m.compose.seed.Value() != "281474976710" || !m.compose.dirty() || !strings.Contains(plain(m), "pinned · every render uses it") ||
		!strings.Contains(footerLines(m), "p random each render") {
		t.Fatalf("pinned: %q dirty %v\n%s", m.compose.seed.Value(), m.compose.dirty(), plain(m))
	}
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	if !strings.Contains(strings.Join(strings.Fields(plain(m)), " "), "Seed 281474976710 pinned · every render uses it │") {
		t.Fatalf("pinned hint at 80 columns:\n%s", plain(m))
	}
	if s, err := m.compose.song(); err != nil || !s.SeedPinned || s.Seed != 281474976710 {
		t.Fatalf("song %+v %v", s, err)
	}
	m = press(t, m, "p")
	if m.compose.seed.Value() != "" || m.compose.dirty() {
		t.Fatalf("unpinned again: %q dirty %v", m.compose.seed.Value(), m.compose.dirty())
	}
	m = press(t, m, "1", "2")
	if m.compose.seed.Value() != "12" || !m.compose.dirty() {
		t.Fatalf("typing still pins: %q", m.compose.seed.Value())
	}
}

func TestUnpinningAPinnedSeedMakesTheFormDirty(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	if _, err := store.Create(library.Song{Title: "Pinned", Seed: 7, SeedPinned: true}); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	m = nav(t, m, "e")
	m.compose.focusOn(rowSeed)
	m = press(t, m, "p")
	if m.compose.seed.Value() != "" || !m.compose.dirty() {
		t.Fatalf("seed %q dirty %v", m.compose.seed.Value(), m.compose.dirty())
	}
	if s, _ := m.compose.song(); s.SeedPinned {
		t.Fatal("still pinned")
	}
}

func TestLibraryCopiesTheSeed(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	var copied []string
	m.d.Clipboard = func(s string) error { copied = append(copied, s); return nil }
	if _, err := store.Create(library.Song{Title: "No seed", Lyrics: sampleDraft.Lyrics}); err != nil {
		t.Fatal(err)
	}
	m = runCmd(t, m, m.loadSongs())
	if strings.Contains(footerLine(m), "s copy seed") {
		t.Fatalf("offered without a seed: %q", footerLine(m))
	}
	m, _ = send(t, m, key("s"))
	if len(copied) != 0 || m.notice != "No seed yet: one is chosen when the song renders." {
		t.Fatalf("copied %q notice %q", copied, m.notice)
	}
	song, err := store.Create(library.Song{Title: "Rendered", Seed: 281474976710, Status: library.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	m.library.selectID = song.ID
	m = runCmd(t, m, m.loadSongs())
	if !strings.Contains(plain(m), "s copy seed") {
		t.Fatalf("not offered:\n%s", plain(m))
	}
	m, _ = send(t, m, key("s"))
	if len(copied) != 1 || copied[0] != "281474976710" || !strings.Contains(plain(m), "Seed copied: 281474976710.") {
		t.Fatalf("copied %q:\n%s", copied, plain(m))
	}
}

func TestTheLibraryFooterWrapsInsteadOfCuttingKeys(t *testing.T) {
	m, store := newTestModel(t, fakeDrafter{})
	for _, title := range []string{"One", "Two"} {
		if _, err := store.Create(library.Song{Title: title, Lyrics: sampleDraft.Lyrics, Seed: 5, Status: library.StatusDone, Audio: "song.flac"}); err != nil {
			t.Fatal(err)
		}
	}
	m = runCmd(t, m, m.loadSongs())
	m = runCmd(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 80, Height: 24} })
	lines := strings.Split(plain(m), "\n")
	if len(lines) > 24 {
		t.Fatalf("%d lines", len(lines))
	}
	foot := strings.Join(lines[len(lines)-2:], " ")
	for _, k := range []string{"↑/↓ select", "e edit", "v revise / extend", "s copy seed", "n new song", "esc back"} {
		if !strings.Contains(foot, k) {
			t.Fatalf("footer lacks %q:\n%s", k, plain(m))
		}
	}
	if strings.Contains(foot, "…") {
		t.Fatalf("footer cut:\n%s", foot)
	}
}
