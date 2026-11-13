# The song form and its text editor, written by hand because the writer is
# unavailable: a new song, lyrics with section tags and tag completion, a
# Style part, Length and Vocals, the unsaved-changes warning, and the saved
# draft in the library. Uses a throwaway config under /tmp/songsmith-demo-editor
# (see demos/editor.yaml); nothing is installed, queued or played, and the
# install plan reads demos/setupfake on a loopback port (SONGSMITH_DEMO_SETUP).
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

# Idea: the writer is unavailable, so enter moves on instead of drafting.
Type "a lighthouse keeper's last night on the rock"
Sleep 800ms
Enter
Sleep 2.5s
Type "Last Light"
Sleep 800ms

# Lyrics in the text editor: a misspelt tag shows in the error colour,
# then [ + tab completes a real one.
Down@400ms 4
Sleep 1s
Enter
Sleep 1.5s
Type "[Verze]"
Sleep 1.5s
Backspace 4
Sleep 500ms
Tab
Sleep 1.5s
Enter
Sleep 800ms
Enter
Type "The lamp still turns for no one"
Enter
Type "Salt on every stair"
Enter
Type "[Ch"
Sleep 500ms
Tab
Sleep 1.2s
Enter
Enter
Type "Keep the light on, keep the light on"
Sleep 800ms

# NORMAL mode; a bare esc only shows how to leave.
Escape
Sleep 1.2s
Escape
Sleep 2s
Type ":w"
Sleep 600ms
Enter
Sleep 2s

# A Style part, written in its own box.
Down
Sleep 800ms
Enter
Sleep 1s
Type "Folk ballad, 72 BPM, D minor, sparse and warm, a slow build to the last chorus."
Sleep 800ms
Escape
Sleep 600ms
Type ":w"
Enter
Sleep 1.5s

# Length in 15 s steps, and Vocals.
Up@400ms 4
Sleep 600ms
Left@500ms 4
Sleep 800ms
Down
Sleep 600ms
Right
Sleep 1.5s
Left
Sleep 1s

# esc on a changed form warns; ctrl+d saves the draft.
Escape
Sleep 2.5s
Ctrl+D
Sleep 2.5s

# The draft in the library.
Down
Sleep 800ms
Enter
Sleep 3.5s
Escape
Sleep 1s
Type "q"
Sleep 500ms
Hide
Type "rm -rf /tmp/songsmith-demo-editor" Enter
