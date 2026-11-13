package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/tui/vim"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// typeInto sends each rune of s to the editor as its own key press; "\n"
// is enter.
func typeInto(t *textEditor, s string) editorEvent {
	ev := editorNone
	for _, r := range s {
		if r == '\n' {
			ev = t.update(key("enter"))
			continue
		}
		ev = t.update(runeKey(string(r)))
	}
	return ev
}

func TestTextEditorStartsInInsertAtTheEnd(t *testing.T) {
	ed := newTextEditor("Lyrics", "[Verse]\nfirst", true)
	if ed.ed.Mode() != vim.ModeInsert {
		t.Fatalf("mode %v", ed.ed.Mode())
	}
	typeInto(ed, " line")
	if got := ed.text(); got != "[Verse]\nfirst line" {
		t.Fatalf("text %q", got)
	}
}

func TestTextEditorCommands(t *testing.T) {
	cases := []struct {
		keys string
		want editorEvent
	}{
		{":w\n", editorKeep},
		{":q\n", editorDiscard},
		{":q!\n", editorDiscard},
		{":wq\n", editorSaveDraft},
		{":x\n", editorSaveDraft},
		{"\n", editorKeep},
	}
	for _, c := range cases {
		ed := newTextEditor("Arrangement", "old", false)
		typeInto(ed, " new")
		ed.update(key("esc"))
		if got := typeInto(ed, c.keys); got != c.want {
			t.Errorf("%q: event %v, want %v", c.keys, got, c.want)
		}
		if ed.text() != "old new" || ed.original != "old" {
			t.Errorf("%q: text %q original %q", c.keys, ed.text(), ed.original)
		}
	}
}

func TestTextEditorEnterAfterACountGoesBackNotSaves(t *testing.T) {
	ed := newTextEditor("Lyrics", "a\nb\nc", true)
	ed.update(key("esc"))
	if ev := typeInto(ed, "2\n"); ev != editorKeep {
		t.Fatalf("enter after a count: %v", ev)
	}
	if ev := typeInto(ed, ":wq\n"); ev != editorSaveDraft {
		t.Fatalf(":wq: %v", ev)
	}
}

func TestTextEditorEscInIdleNormalStays(t *testing.T) {
	ed := newTextEditor("Lyrics", "words", true)
	if ev := ed.update(key("esc")); ev != editorNone || ed.ed.Mode() != vim.ModeNormal {
		t.Fatalf("first esc: %v mode %v", ev, ed.ed.Mode())
	}
	if ev := ed.update(key("esc")); ev != editorIdleEsc {
		t.Fatalf("idle esc: %v", ev)
	}
	ed.update(runeKey("d"))
	if ev := ed.update(key("esc")); ev == editorIdleEsc {
		t.Fatal("esc with an operator pending should only cancel it")
	}
}

func TestTextEditorFormKeysWorkInEveryMode(t *testing.T) {
	for _, k := range []string{"ctrl+s", "ctrl+d", "ctrl+e"} {
		ed := newTextEditor("Lyrics", "", true)
		typeInto(ed, "[Verse]")
		want := map[string]editorEvent{"ctrl+s": editorSaveQueue, "ctrl+d": editorSaveDraft, "ctrl+e": editorExternal}[k]
		if ev := ed.update(key(k)); ev != want || ed.text() != "[Verse]" {
			t.Fatalf("%s in INSERT: %v text %q", k, ev, ed.text())
		}
		ed.update(key("esc"))
		if ev := ed.update(key(k)); ev != want {
			t.Fatalf("%s in NORMAL: %v", k, ev)
		}
	}
}

func TestTextEditorArrowsMoveInInsert(t *testing.T) {
	ed := newTextEditor("Lyrics", "café\nab", true)
	for _, k := range []string{"up", "right", "right"} {
		ed.update(key(k))
	}
	typeInto(ed, "!")
	if got := ed.text(); got != "café!\nab" {
		t.Fatalf("right over é: %q", got)
	}
	ed.update(key("left"))
	ed.update(key("left"))
	typeInto(ed, "X")
	if got := ed.text(); got != "cafXé!\nab" {
		t.Fatalf("left over é: %q", got)
	}
}

func TestTagColouring(t *testing.T) {
	cases := []struct {
		line  string
		style string
	}{
		{"[Chorus]", "known"},
		{"  [Verse 2]", "known"},
		{"[Hook]", "known"},
		{"[Chrous]", "unknown"},
		{"[verse]", "unknown"},
		{"[Chorus] la la", ""},
		{"plain words", ""},
	}
	for _, c := range cases {
		st, ok := tagStyle(c.line)
		got := ""
		switch {
		case ok && st.GetForeground() == tagKnownStyle.GetForeground():
			got = "known"
		case ok && st.GetForeground() == tagUnknownStyle.GetForeground():
			got = "unknown"
		}
		if got != c.style {
			t.Errorf("%q: %q, want %q", c.line, got, c.style)
		}
		spans := tagTokens(c.line)
		if (len(spans) == 1) != (c.style != "") {
			t.Errorf("%q: spans %v", c.line, spans)
			continue
		}
		if len(spans) == 1 && knownTags()[spans[0].Name] != (c.style == "known") {
			t.Errorf("%q: span name %q classified wrongly", c.line, spans[0].Name)
		}
	}
}

func TestTagCompletion(t *testing.T) {
	ed := newTextEditor("Lyrics", "", true)
	typeInto(ed, "[")
	ed.update(key("tab"))
	if !slices.Equal(ed.menu, writer.SectionTags) {
		t.Fatalf("menu %v", ed.menu)
	}
	typeInto(ed, "c")
	if !slices.Equal(ed.menu, []string{"Chorus"}) {
		t.Fatalf("filtered menu %v", ed.menu)
	}
	ed.update(key("enter"))
	if ed.text() != "[Chorus]" || ed.menu != nil {
		t.Fatalf("text %q menu %v", ed.text(), ed.menu)
	}

	ed = newTextEditor("Lyrics", "", true)
	typeInto(ed, "[p")
	ed.update(key("tab"))
	ed.update(key("down"))
	ed.update(key("tab"))
	if ed.text() != "[Post-Chorus]" {
		t.Fatalf("tab insert: %q", ed.text())
	}

	ed = newTextEditor("Lyrics", "", true)
	typeInto(ed, "[Br")
	ed.update(key("tab"))
	if ed.update(key("esc")); ed.menu != nil || ed.text() != "[Br" || ed.ed.Mode() != vim.ModeInsert {
		t.Fatalf("esc should close the menu only: text %q mode %v", ed.text(), ed.ed.Mode())
	}

	ed = newTextEditor("Lyrics", "", true)
	typeInto(ed, "[Zz")
	ed.update(key("tab"))
	if ed.menu != nil || ed.text() != "[Zz" || !strings.Contains(ed.note, "No section tag") {
		t.Fatalf("no candidates: text %q note %q", ed.text(), ed.note)
	}
}

func TestTagCompletionIsLyricsOnly(t *testing.T) {
	ed := newTextEditor("Arrangement", "", false)
	typeInto(ed, "[")
	ed.update(tea.KeyMsg{Type: tea.KeyTab})
	if ed.menu != nil {
		t.Fatal("style fields have no tag menu")
	}
}
