# Settings: walk the sections, change the temperature, the render steps and
# the server port, then show that the config file changed only those keys and
# kept its comments. Uses a throwaway config under /tmp/songsmith-demo-settings
# (see demos/settings.yaml) where no ComfyUI or Ollama runs; nothing is
# installed, started or rendered.
# Record with: just demo-settings

Output demos/settings.gif

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
Type "rm -rf /tmp/songsmith-demo-settings && mkdir -p /tmp/songsmith-demo-settings && cp demos/settings.yaml /tmp/songsmith-demo-settings/config.yaml" Enter
Type "export PATH=$PWD/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-settings/config.yaml" Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 3s

# Settings is the fourth item while Finish setup is offered.
Down@600ms 3
Sleep 800ms
Enter
Sleep 3s

# Writer: Ollama is unreachable here, so the list gives way to one line.
Right
Sleep 2.5s
Down@500ms 4
Sleep 1s
Type "+"
Sleep 2.5s

# Music model, then Render: two more steps.
Right
Sleep 2.5s
Right
Sleep 1.5s
Type@600ms "++"
Sleep 2.5s

# Music engine: the port opens in the one-line editor and applies after restart.
Right
Sleep 2s
Down@500ms 5
Sleep 800ms
Enter
Sleep 800ms
Backspace@150ms 5
Type "18190"
Sleep 800ms
Enter
Sleep 3s

Right
Sleep 2.5s
Escape
Sleep 1s
Type "q"
Sleep 800ms

# Only the three changed values differ, and every comment is still there.
Type "diff demos/settings.yaml $SONGSMITH_CONFIG"
Sleep 400ms
Enter
Sleep 3s
Type "cat $SONGSMITH_CONFIG"
Sleep 400ms
Enter
Sleep 5s

Hide
Type "rm -rf /tmp/songsmith-demo-settings" Enter
