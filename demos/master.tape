# The Master screen: from the library, m opens it on a finished song, the
# Streaming preset and a warmer tilt change the meters once their excerpt is
# prepared, Master this take renders a new take with its progress, and the
# new master becomes the take the library plays. Uses a throwaway config
# under /tmp/songsmith-demo-master (see demos/master.yaml) and a fixture song
# whose audio is a tone made by ffmpeg here; Ollama is demos/setupfake on a
# loopback port. Nothing plays: space and o are never pressed, and afplay is
# a no-op stub first on PATH.
# Record with: just demo-master

Output demos/master.gif

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
Type "rm -rf /tmp/songsmith-demo-master && mkdir -p /tmp/songsmith-demo-master/library /tmp/songsmith-demo-master/bin && cp demos/master.yaml /tmp/songsmith-demo-master/config.yaml && cp -R demos/master-fixture/. /tmp/songsmith-demo-master/library/" Enter
Type "ln -s $(command -v ffmpeg) /tmp/songsmith-demo-master/bin/ffmpeg && ln -s $(command -v ffprobe) /tmp/songsmith-demo-master/bin/ffprobe && printf '#!/bin/sh\nexit 0\n' > /tmp/songsmith-demo-master/bin/afplay && chmod +x /tmp/songsmith-demo-master/bin/afplay" Enter
Type "export PATH=$PWD/bin:/tmp/songsmith-demo-master/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-master/config.yaml SONGSMITH_DEMO_SETUP=http://127.0.0.1:18490" Enter
Type `ffmpeg -nostdin -v error -f lavfi -i "aevalsrc='0.06*sin(2*PI*147*t)*(0.7+0.3*sin(2*PI*0.25*t))+0.04*sin(2*PI*220*t)+0.03*sin(2*PI*370*t)+0.004*(random(0)-0.5)+0.01|0.06*sin(2*PI*147*t)*(0.7+0.3*sin(2*PI*0.25*t+1))+0.04*sin(2*PI*294*t)+0.03*sin(2*PI*440*t)+0.004*(random(1)-0.5)+0.01':s=44100:d=120" -c:a flac /tmp/songsmith-demo-master/library/20261113-090000-lantern-road/song.flac` Enter
Sleep 4s
# Stop here unless this shell runs the fresh build on the scratch config with the stub player.
Type `[ "$SONGSMITH_CONFIG" = /tmp/songsmith-demo-master/config.yaml ] && [ "$(command -v songsmith)" = "$PWD/bin/songsmith" ] && [ "$(command -v afplay)" = /tmp/songsmith-demo-master/bin/afplay ] && [ -s /tmp/songsmith-demo-master/library/20261113-090000-lantern-road/song.flac ] || exit 1` Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 3s

# Finish setup is preselected while setup fails; the library is two down.
Down@600ms 2
Sleep 600ms
Enter
Sleep 2.5s

# m masters the finished song: it is measured, and an excerpt prepared.
Type "m"
Sleep 4s

# The Preset row: Streaming.
Down@500ms 2
Sleep 1s
Type "+"
Sleep 4.5s

# A warmer tilt makes the preset Custom, and the meters follow the new excerpt.
Down@300ms 7
Sleep 1s
Type "-"
Sleep 4.5s

# Master this take renders the whole take as a new one.
Ctrl+S
Sleep 7s

# The new master is listed; make it the take the library plays.
Enter
Sleep 2s
Type "g"
Sleep 3s

# Back in the library: take 2 of 2, mastered.
Escape
Sleep 4s
Escape
Sleep 800ms
Type "q"
Sleep 500ms

Hide
Type "rm -rf /tmp/songsmith-demo-master" Enter
