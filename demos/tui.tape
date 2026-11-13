# The README's hero recording: the launcher ready to render, the writer
# drafting a song from its idea, the library with a song, a new version of
# it and its takes, and the Master screen. Uses a throwaway config under
# /tmp/songsmith-demo-hero (see demos/hero.yaml and demos/hero-setup.sh);
# Ollama and ComfyUI are demos/setupfake's stand-ins on loopback ports, the
# audio is tones made by ffmpeg, and the players are no-op stubs, so nothing
# real is installed, rendered or played.
# Record with: just demo

Output demos/tui.gif

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
Type "unset NO_COLOR CLICOLOR VISUAL EDITOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1 TERM=xterm-256color SONGSMITH_COLOR_PROFILE=ansi" Enter
Type "demos/hero-setup.sh" Enter
Sleep 8s
Type "export PATH=$PWD/bin:/tmp/songsmith-demo-hero/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-hero/config.yaml SONGSMITH_DEMO_SETUP=http://127.0.0.1:18490 SONGSMITH_DEMO_SETUP_FIXTURES=1" Enter
# Stop here unless this shell runs the fresh build on the scratch config with the stub players.
Type `[ "$SONGSMITH_CONFIG" = /tmp/songsmith-demo-hero/config.yaml ] && [ "$(command -v songsmith)" = "$PWD/bin/songsmith" ] && [ "$(command -v afplay)" = /tmp/songsmith-demo-hero/bin/afplay ] && [ -s /tmp/songsmith-demo-hero/library/20261113-090000-lantern-road/song.flac ] || exit 1` Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 3s

# The library is one down from New song.
Down
Sleep 600ms
Enter
Sleep 2.5s

# A draft with only an idea: g has the writer draft lyrics and style.
Type "g"
Sleep 6s
Down@500ms 4
Sleep 1.5s
Ctrl+D
Sleep 2.5s

# A new version of a finished song, then the song it came from with a
# mastered take; t switches the take the library plays.
Down
Sleep 2.5s
Down
Sleep 2.5s
Type "t"
Sleep 2s

# The Master screen: the Streaming preset, and the meters follow.
Type "m"
Sleep 4s
Down@500ms 3
Sleep 800ms
Type "+"
Sleep 9s

Escape
Sleep 1.5s
Escape
Sleep 800ms
Type "q"
Sleep 500ms

Hide
Type "rm -rf /tmp/songsmith-demo-hero" Enter
