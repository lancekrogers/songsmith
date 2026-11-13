package tui

import (
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/songsmith/internal/writer"
)

// settingsText is a file open in the text editor inside Settings.
type settingsText struct {
	ed   *textEditor
	path string
}

var settingsTextNormalKeys = []string{"i insert", "enter or :w save", ":q discard", "u undo", "v visual"}

// settingsTextChrome is the header and key lines around the open file, and
// the rows left for its box.
func (a app) settingsTextChrome() (head, tail []string, rows int) {
	t := a.settings.text
	head = []string{headerStyle.Render("  ♪ Settings") + dimStyle.Render("  "+t.path)}
	msg := ""
	if a.settings.message != "" {
		msg = "  " + a.messageStyle().Render(a.settings.message)
	}
	keys := []string{"esc normal"}
	if externalEditorSet() {
		keys = append(keys, "ctrl+e $EDITOR")
	}
	tail = []string{msg, footer(a.width, "  ", t.ed.modeKeys()...), footer(a.width, "  ", keys...)}
	head, tail = fitChrome(a.height, head, tail)
	return head, tail, max(a.height-len(head)-len(tail), 0)
}

func (a app) settingsTextBoxWidth() int { return max(min(a.width-2, 100), 30) }

func (a *app) layoutSettingsText() {
	t := a.settings.text
	if t == nil {
		return
	}
	_, _, rows := a.settingsTextChrome()
	if rows >= 3 {
		rows -= 2
	}
	t.ed.setSize(a.settingsTextBoxWidth()-8, max(rows, 1))
}

func (a app) settingsTextView() string {
	t := a.settings.text
	head, tail, rows := a.settingsTextChrome()
	body := t.ed.bodyLines(0)
	if rows >= 3 {
		title := headerStyle.Render(t.ed.title) + "  " + t.ed.modeChip()
		body = formBox(title, body, a.settingsTextBoxWidth(), true)
	}
	all := append(append(head, fitLines(body, rows)...), tail...)
	for i, l := range all {
		all[i] = truncate(l, a.width)
	}
	return strings.Join(all, "\n")
}

// updateSettingsText gives every key to the open file: enter in NORMAL or
// :w saves it and returns to Settings, :q returns without saving, and
// ctrl+e saves it and opens it in $VISUAL or $EDITOR.
func (a app) updateSettingsText(key tea.KeyMsg) (app, tea.Cmd) {
	t := a.settings.text
	a.clearSettingsNote()
	var cmd tea.Cmd
	switch ev := t.ed.update(key); ev {
	case editorKeep, editorSaveDraft, editorSaveQueue:
		if a.writeSettingsText() {
			a.closeSettingsText()
			a.checkInstructions(t.path)
		}
	case editorDiscard:
		a.closeSettingsText()
		if t.ed.text() != t.ed.original {
			a.settingsNote("Discarded your changes; " + t.path + " is as it was")
		}
	case editorExternal:
		if !externalEditorSet() {
			a.settingsNote("ctrl+e opens $VISUAL or $EDITOR, and neither is set. Set one in your shell, or keep editing here.")
			break
		}
		if a.writeSettingsText() {
			a.closeSettingsText()
			cmd = a.externalInstructions(t.path)
		}
	case editorIdleEsc:
		a.settingsNote("Still editing: enter or :w saves and goes back · :q discards")
	default:
		if t.ed.note != "" {
			a.settingsNote(t.ed.note)
		}
	}
	a.layoutSettingsText()
	return a, cmd
}

// writeSettingsText saves the open file; a failure keeps it open and says why.
func (a *app) writeSettingsText() bool {
	t := a.settings.text
	if t.ed.text() == t.ed.original {
		return true
	}
	if err := os.WriteFile(t.path, []byte(t.ed.text()+"\n"), 0o644); err != nil {
		a.settingsError("Not saved: " + err.Error())
		return false
	}
	return true
}

func (a *app) closeSettingsText() {
	a.settings.text = nil
	a.syncSettings()
}

// checkInstructions says whether drafts will use the file at path.
func (a *app) checkInstructions(path string) {
	if _, err := writer.Instructions(path); err != nil {
		a.settingsError(err.Error() + "; drafts use the built-in instructions until it has text")
		return
	}
	a.settingsNote(capitalize(appliesNextDraft) + " · Writing instructions saved in " + path)
}
