# The song form: a long idea wrapped in full, the Writer row that picks the
# model, the Draft action and the draft arriving, then the text editor with
# section tags and tag completion, Length, the unsaved-changes warning, the
# saved draft in the library, and revising it with the writer: ctrl+r, an
# instruction, the review with added lines marked, and applying it. Uses a throwaway config under
# /tmp/songsmith-demo-editor (see demos/editor.yaml); the writer is
# demos/setupfake's stand-in Ollama with a canned draft on a loopback port, and
# nothing is installed, queued or played.
# Record with: just demo-editor

Output demos/editor.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1300
Set Height 720
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 18
Set TypingSpeed 60ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR CLICOLOR VISUAL EDITOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1 TERM=xterm-256color SONGSMITH_COLOR_PROFILE=ansi" Enter
Type "rm -rf /tmp/songsmith-demo-editor && mkdir -p /tmp/songsmith-demo-editor/library && cp demos/editor.yaml /tmp/songsmith-demo-editor/config.yaml && cp -R demos/fixture/. /tmp/songsmith-demo-editor/library/" Enter
Type "export PATH=$PWD/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-editor/config.yaml SONGSMITH_DEMO_SETUP=http://127.0.0.1:18490" Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 2.5s

# Finish setup is preselected while setup fails; New song is next.
Down
Sleep 800ms
Enter
Sleep 2.5s

# A long idea wraps over as many lines as it needs.
Type@35ms "a lighthouse keeper's last night on the rock before the light goes automatic; he writes one more entry in the log while the radio says the fog is coming in, and he decides to keep the lamp turning by hand until dawn"
Sleep 1.5s

# The Writer row picks the model that drafts; the Draft row drafts.
Down
Sleep 1s
Right
Sleep 1.5s
Left
Sleep 1.2s
Down
Sleep 1.2s
Enter
Sleep 4.5s

# The draft fills Title, Lyrics and the Style parts. Add an outro in the
# text editor: a misspelt tag shows in the error colour, then tab completes.
Enter
Sleep 1.5s
Escape
Sleep 600ms
Type "G"
Sleep 600ms
Type "o"
Enter
Type "[Verze]"
Sleep 1.5s
Backspace 4
Sleep 500ms
Tab
Sleep 1.2s
Enter
Sleep 600ms
Enter
Type "Keep the light on"
Sleep 800ms
Escape
Sleep 600ms
Type ":w"
Sleep 500ms
Enter
Sleep 2s

# Length in 15 s steps.
Up@400ms 3
Sleep 600ms
Right@500ms 2
Sleep 1.5s

# esc on a changed form warns; ctrl+d saves the draft.
Escape
Sleep 2.5s
Ctrl+D
Sleep 3s

# The draft in the library.
Down
Sleep 800ms
Enter
Sleep 3.5s

# Revise it: ctrl+r, say what should change, review the result, apply it.
Up
Sleep 800ms
Type "e"
Sleep 2s
Ctrl+R
Sleep 1.2s
Type@35ms "make it longer with another verse and a bridge that continue the story"
Sleep 1.2s
Enter
Sleep 4.5s
Down@300ms 8
Sleep 2.5s
Down@300ms 12
Sleep 2.5s
Enter
Sleep 3s
Ctrl+D
Sleep 3s
Escape
Sleep 1s
Type "q"
Sleep 500ms
Hide
Type "rm -rf /tmp/songsmith-demo-editor" Enter
