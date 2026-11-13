# First run on a machine with nothing installed: no ComfyUI, no model files,
# no reachable Ollama, no ffprobe on PATH and an empty library. Finish setup is
# offered first. Uses a throwaway config under /tmp/songsmith-demo-shell (see
# demos/shell.yaml); nothing is installed or rendered.
# Record with: just demo-shell

Output demos/shell.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1300
Set Height 720
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 18
Set TypingSpeed 40ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1 TERM=xterm-256color SONGSMITH_COLOR_PROFILE=ansi" Enter
Type "rm -rf /tmp/songsmith-demo-shell && mkdir -p /tmp/songsmith-demo-shell && cp demos/shell.yaml /tmp/songsmith-demo-shell/config.yaml" Enter
Type "export PATH=$PWD/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-shell/config.yaml" Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 3s

# Move through the menu: Finish setup is preselected while setup fails.
Down@700ms 4
Sleep 1s
Up@700ms 2
Sleep 1s

# Library (0): explains how to make the first song, then back.
Enter
Sleep 3s
Escape
Sleep 1.5s

# Finish setup: each missing piece and who can install it, then back.
Up@700ms 2
Sleep 800ms
Enter
Sleep 3s
Down@300ms 8
Sleep 2s
Escape
Sleep 1.5s

Type "q"
Sleep 500ms
Hide
Type "rm -rf /tmp/songsmith-demo-shell" Enter
