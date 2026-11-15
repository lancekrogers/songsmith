# The library's folders: two versions of a song go into a new folder, the
# folder opens to show them, and a draft is deleted after its confirmation.
# Uses a throwaway config under /tmp/songsmith-demo-folders (see
# demos/folders.yaml) holding a copy of demos/hero-fixture; nothing is
# installed, rendered or played.
# Record with: just demo-folders

Output demos/folders.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1300
Set Height 620
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 18
Set TypingSpeed 50ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR CLICOLOR VISUAL EDITOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1 TERM=xterm-256color SONGSMITH_COLOR_PROFILE=ansi" Enter
Type "rm -rf /tmp/songsmith-demo-folders && mkdir -p /tmp/songsmith-demo-folders/library && cp demos/folders.yaml /tmp/songsmith-demo-folders/config.yaml && cp -R demos/hero-fixture/. /tmp/songsmith-demo-folders/library/" Enter
Type "for f in 20261113-090000-lantern-road/song.flac 20261113-090000-lantern-road/song.master-1.flac 20261113-094500-lantern-road-v2/song.flac 20261112-200000-neon-water/song.flac; do : > /tmp/songsmith-demo-folders/library/$f; done" Enter
Type "export PATH=$PWD/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-folders/config.yaml" Enter
# Stop here unless this shell runs the fresh build on the scratch config.
Type `[ "$SONGSMITH_CONFIG" = /tmp/songsmith-demo-folders/config.yaml ] && [ "$(command -v songsmith)" = "$PWD/bin/songsmith" ] || exit 1` Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 2.5s

# Library is two down.
Down@500ms 2
Enter
Sleep 2s

# Lantern Road (v2) goes in a new folder: f opens on + New folder… while there are none.
Down
Sleep 800ms
Type "f"
Sleep 1.5s
Enter
Sleep 800ms
Type "Lantern Road"
Sleep 800ms
Enter
Sleep 1.5s

# The cursor moved on to Lantern Road: f, down to the folder, enter.
Type "f"
Sleep 1s
Down
Sleep 700ms
Enter
Sleep 1.5s

# Up to the folder and open it.
Up@600ms 2
Sleep 800ms
Enter
Sleep 2s

# The untitled draft: d asks first; n keeps it.
Down@500ms 3
Sleep 800ms
Type "d"
Sleep 2s
Type "n"
Sleep 1.5s
Type "d"
Sleep 1.2s
Type "y"
Sleep 2s
