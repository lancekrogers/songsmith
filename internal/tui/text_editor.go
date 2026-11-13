package tui

import (
	"cmp"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/songsmith/internal/tui/vim"
	"github.com/lancekrogers/songsmith/internal/writer"
)

// Under a 16-colour profile the palette's hex colours land on their nearest
// slots, where the accent is a pale cyan close to the text and gray turns
// blue. These name the slots, so tags and the focus border stay distinct.
var (
	formAccent = lipgloss.CompleteColor{TrueColor: string(colorAccent), ANSI256: "117", ANSI: "6"}
	formMuted  = lipgloss.CompleteColor{TrueColor: string(colorDim), ANSI256: "243", ANSI: "8"}
)

var (
	tagKnownStyle   = lipgloss.NewStyle().Foreground(formAccent).Bold(true)
	tagUnknownStyle = lipgloss.NewStyle().Foreground(colorError).Underline(true)
	selectionStyle  = lipgloss.NewStyle().Foreground(colorBg).Background(colorDim)
	insertCursor    = lipgloss.NewStyle().Foreground(colorBg).Background(colorStatus)
)

// textEditor edits one long field in a vim-style editor that takes the whole
// form area. It reports what the user asked for as an editorEvent and leaves
// acting on it to the form.
type textEditor struct {
	ed       *vim.Editor
	title    string
	original string
	// tags turns on section-tag colouring and [ + tab completion.
	tags          bool
	width, height int

	// menu holds the tag candidates while the completion menu is open.
	menu    []string
	menuSel int
	// note is a one-off message for the form's message line.
	note string
}

type editorEvent int

const (
	editorNone editorEvent = iota
	// editorKeep returns to the form with the text (:w, normal-mode enter).
	editorKeep
	// editorDiscard returns to the form without this field's changes (:q).
	editorDiscard
	// editorSaveDraft keeps the text and saves the song as a draft (:wq, ctrl+d).
	editorSaveDraft
	// editorSaveQueue keeps the text, saves the song and queues it (ctrl+s).
	editorSaveQueue
	// editorExternal hands the text to $VISUAL or $EDITOR (ctrl+e).
	editorExternal
	// editorIdleEsc is esc with nothing to cancel. It never leaves: a reflex
	// esc after typing must not throw the edit away.
	editorIdleEsc
)

// newTextEditor opens text in INSERT mode with the cursor at its end.
func newTextEditor(title, text string, tags bool) *textEditor {
	t := &textEditor{ed: vim.NewEditor(text), title: title, original: text, tags: tags, width: 60, height: 10}
	t.ed.SetSize(t.width, t.height)
	// G then A: the last line, in INSERT, with an undo point for the session.
	t.ed.Update(runeKey("G"))
	t.ed.Update(runeKey("A"))
	return t
}

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func (t *textEditor) text() string { return t.ed.Content() }

// setText replaces the text, as when it comes back from $EDITOR.
func (t *textEditor) setText(s string) {
	t.ed.SetContent(s)
	t.ed.State().Reset()
	t.closeMenu()
}

func (t *textEditor) setSize(width, height int) {
	t.width, t.height = max(width, 10), max(height, 1)
	t.ed.SetSize(t.width, t.height)
	t.ed.EnsureCursorVisible()
}

func (t *textEditor) update(msg tea.KeyMsg) editorEvent {
	t.note = ""
	switch msg.String() {
	case "ctrl+s":
		return editorSaveQueue
	case "ctrl+d":
		return editorSaveDraft
	case "ctrl+e":
		return editorExternal
	}
	if t.menu != nil && t.menuKey(msg) {
		return editorNone
	}
	mode := t.ed.Mode()
	if mode == vim.ModeInsert {
		if msg.Type == tea.KeyTab && t.tags && t.openMenu() {
			return editorNone
		}
		if t.insertArrow(msg) {
			t.closeMenu()
			return editorNone
		}
	}
	if t.idleNormal() {
		switch msg.Type {
		case tea.KeyEsc:
			return editorIdleEsc
		case tea.KeyEnter:
			return editorKeep
		}
	}
	cmd, _ := t.ed.Update(msg)
	if t.menu != nil {
		t.refreshMenu()
	}
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == "wq" && mode == vim.ModeNormal:
		// The editor reports normal-mode enter as "wq"; here it means back to the form.
		return editorKeep
	case cmd == "w":
		return editorKeep
	case cmd == "q" || cmd == "q!":
		return editorDiscard
	case cmd == "wq" || cmd == "x":
		return editorSaveDraft
	case cmd != "":
		t.note = "Unknown command :" + cmd + ". Use :w, :q or :wq."
	}
	return editorNone
}

// idleNormal is NORMAL mode with nothing pending, the only state where esc
// has nothing left to cancel.
func (t *textEditor) idleNormal() bool {
	s := t.ed.State()
	return s.Mode == vim.ModeNormal && !s.HasPendingOperator() && s.PendingKey == 0 &&
		!s.AwaitingChar && !s.AwaitingReplace && !s.AwaitingTextObj && !s.HasCount
}

// insertArrow moves the cursor on arrow keys in INSERT, which the vim
// package leaves to its host.
func (t *textEditor) insertArrow(msg tea.KeyMsg) bool {
	lines := strings.Split(t.ed.Content(), "\n")
	cur := t.ed.Cursor()
	line := lines[cur.Line]
	switch msg.Type {
	case tea.KeyLeft:
		if cur.Col > 0 {
			_, n := utf8.DecodeLastRuneInString(line[:min(cur.Col, len(line))])
			cur.Col -= n
		}
	case tea.KeyRight:
		if cur.Col < len(line) {
			_, n := utf8.DecodeRuneInString(line[cur.Col:])
			cur.Col += n
		}
	case tea.KeyUp, tea.KeyDown:
		next := cur.Line - 1
		if msg.Type == tea.KeyDown {
			next = cur.Line + 1
		}
		if next < 0 || next >= len(lines) {
			return true
		}
		cur.Line, cur.Col = next, runeBoundary(lines[next], cur.Col)
	default:
		return false
	}
	t.ed.SetCursorInsert(cur)
	t.ed.EnsureCursorVisible()
	return true
}

// runeBoundary is the last rune start in s at or before byte col.
func runeBoundary(s string, col int) int {
	col = min(col, len(s))
	for col > 0 && col < len(s) && !utf8.RuneStart(s[col]) {
		col--
	}
	return col
}

// openTag finds an unclosed [ that starts the cursor's line and the text
// typed after it.
func (t *textEditor) openTag() (bracket int, partial string, ok bool) {
	line, col := t.ed.CurrentLine(), t.ed.Cursor().Col
	before := line[:min(col, len(line))]
	i := strings.IndexByte(before, '[')
	if i < 0 || strings.TrimSpace(before[:i]) != "" || strings.ContainsAny(before[i+1:], "[]") {
		return 0, "", false
	}
	return i, before[i+1:], true
}

func tagCandidates(partial string) []string {
	var out []string
	for _, tag := range writer.SectionTags {
		if strings.HasPrefix(strings.ToLower(tag), strings.ToLower(partial)) {
			out = append(out, tag)
		}
	}
	return out
}

// openMenu opens tag completion after a [ that starts the line. It reports
// whether tab was used for that rather than typed.
func (t *textEditor) openMenu() bool {
	_, partial, ok := t.openTag()
	if !ok {
		return false
	}
	t.menu, t.menuSel = tagCandidates(partial), 0
	if len(t.menu) == 0 {
		t.menu = nil
		t.note = "No section tag starts with " + partial + "."
	}
	return true
}

func (t *textEditor) closeMenu() { t.menu, t.menuSel = nil, 0 }

// refreshMenu filters the open menu by what has been typed since.
func (t *textEditor) refreshMenu() {
	_, partial, ok := t.openTag()
	if !ok || t.ed.Mode() != vim.ModeInsert {
		t.closeMenu()
		return
	}
	t.menu = tagCandidates(partial)
	if len(t.menu) == 0 {
		t.closeMenu()
		return
	}
	t.menuSel = min(t.menuSel, len(t.menu)-1)
}

func (t *textEditor) menuKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyUp, tea.KeyShiftTab:
		t.menuSel = (t.menuSel + len(t.menu) - 1) % len(t.menu)
	case tea.KeyDown:
		t.menuSel = (t.menuSel + 1) % len(t.menu)
	case tea.KeyTab, tea.KeyEnter:
		t.insertTag(t.menu[t.menuSel])
	case tea.KeyEsc:
		t.closeMenu()
	default:
		return false
	}
	return true
}

// insertTag completes the tag being typed and closes its bracket.
func (t *textEditor) insertTag(tag string) {
	bracket, _, ok := t.openTag()
	t.closeMenu()
	if !ok {
		return
	}
	col := t.ed.Cursor().Col
	line := t.ed.CurrentLine()
	if col < len(line) && line[col] == ']' {
		t.ed.ReplaceLineRange(bracket+1, col+1, tag+"]")
	} else {
		t.ed.ReplaceLineRange(bracket+1, col, tag+"]")
	}
	t.ed.EnsureCursorVisible()
}

// menuRows is how many rows the completion menu draws, given at most limit.
func (t *textEditor) menuRows(limit int) int {
	return min(len(t.menu), max(limit, 1))
}

func (t *textEditor) menuLines(rows int) []string {
	if len(t.menu) == 0 {
		return nil
	}
	start := min(max(t.menuSel-rows/2, 0), max(len(t.menu)-rows, 0))
	var out []string
	for i := start; i < min(start+rows, len(t.menu)); i++ {
		label := "[" + t.menu[i] + "]"
		if i == t.menuSel {
			out = append(out, "    "+selectedStyle.Render("▸ "+label))
		} else {
			out = append(out, "    "+dimStyle.Render("  "+label))
		}
	}
	return out
}

func (t *textEditor) viewConfig() vim.ViewConfig {
	cfg := vim.DefaultViewConfig()
	cfg.NormalText = normalStyle
	cfg.LineNumber = dimStyle
	cfg.CommandLine = dimStyle
	cfg.Selection = selectionStyle
	cfg.CursorInsert = insertCursor
	if t.tags {
		cfg.Tokens = tagTokens
		cfg.KnownPlaceholders = knownTags()
		cfg.PlaceholderKnown = tagKnownStyle
		cfg.PlaceholderUnknown = tagUnknownStyle
	}
	return cfg
}

// tagTokens reports the section tag on a tag-only line. Its name is the
// section it counts as, or "" when writer would not count it.
func tagTokens(line string) []vim.TokenSpan {
	start, end, section, ok := writer.TagLine(line)
	if !ok {
		return nil
	}
	return []vim.TokenSpan{{Start: start, End: end, Name: section}}
}

func knownTags() map[string]bool {
	known := map[string]bool{}
	for _, tag := range writer.AcceptedSectionTags {
		known[tag] = true
	}
	return known
}

// tagStyle paints a whole line the way the editor paints a tag line: known
// tags in the accent colour, unknown ones in the error colour.
func tagStyle(line string) (lipgloss.Style, bool) {
	_, _, section, ok := writer.TagLine(line)
	switch {
	case !ok:
		return lipgloss.Style{}, false
	case writer.IsSectionTag(section):
		return tagKnownStyle, true
	default:
		return tagUnknownStyle, true
	}
}

// bodyLines is the editor text with the completion menu right under the
// text, above the empty rows.
func (t *textEditor) bodyLines(menuRows int) []string {
	lines := strings.Split(t.ed.View(t.viewConfig()), "\n")
	menu := t.menuLines(menuRows)
	if len(menu) == 0 {
		return lines
	}
	end := slices.Index(lines, emptyRow)
	if end < 0 {
		end = len(lines)
	}
	return slices.Concat(lines[:end], menu, lines[end:])
}

// emptyRow is how the editor draws a row past the end of the text.
var emptyRow = strings.Split(vim.NewEditor("").View(vim.ViewConfig{ShowLineNums: true}), "\n")[1]

func (t *textEditor) modeChip() string {
	chip := func(c lipgloss.Color, s string) string {
		return lipgloss.NewStyle().Foreground(colorBg).Background(c).Bold(true).Render(" " + s + " ")
	}
	switch t.ed.Mode() {
	case vim.ModeInsert:
		return chip(colorStatus, "INSERT")
	case vim.ModeVisual, vim.ModeVisualLine:
		return chip(colorSelect, t.ed.Mode().String())
	case vim.ModeCommand:
		return chip(colorWarning, "COMMAND")
	default:
		return chip(colorAccent, "NORMAL")
	}
}

// modeKeys are the keys that work in the current mode.
func (t *textEditor) modeKeys() []string {
	if t.menu != nil {
		return []string{"↑/↓ choose", "tab/enter insert", "esc close"}
	}
	switch t.ed.Mode() {
	case vim.ModeInsert:
		keys := []string{"esc normal", "←/→/↑/↓ move"}
		if t.tags {
			keys = append(keys, "[ then tab: section tags")
		}
		return keys
	case vim.ModeVisual, vim.ModeVisualLine:
		return []string{"y yank", "d delete", "c change", "esc normal"}
	case vim.ModeCommand:
		return []string{":" + t.ed.CommandBuffer() + "▏", "enter run", "esc cancel"}
	}
	return []string{"i insert", ":w back to form", ":q discard", ":wq save draft", "u undo", "v visual"}
}

// editorKeys are the form keys that also work inside the editor.
func editorKeys() []string {
	keys := []string{"ctrl+s save & queue", "ctrl+d save draft"}
	if externalEditorSet() {
		keys = append(keys, "ctrl+e $EDITOR")
	}
	return keys
}

// idleEscHint is shown when esc has nothing left to cancel.
const idleEscHint = "Still editing: enter or :w back to the form · :q discard · :wq save draft"

// editorCommand opens path in $VISUAL, then $EDITOR, then vi.
func editorCommand(path string) *exec.Cmd {
	parts := strings.Fields(cmp.Or(strings.TrimSpace(os.Getenv("VISUAL")), strings.TrimSpace(os.Getenv("EDITOR")), "vi"))
	return exec.Command(parts[0], append(parts[1:], path)...)
}

// externalEditorSet reports whether $VISUAL or $EDITOR names an editor.
func externalEditorSet() bool {
	return strings.TrimSpace(os.Getenv("VISUAL")) != "" || strings.TrimSpace(os.Getenv("EDITOR")) != ""
}

// truncate cuts s to width display columns.
func truncate(s string, width int) string { return ansi.Truncate(s, max(width, 0), "…") }
