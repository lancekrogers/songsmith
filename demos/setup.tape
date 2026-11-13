# First run on a machine with nothing installed: Finish setup installs
# ComfyUI, the MiniMax Music 3 files and the writer model, and the launcher
# ends ready. Uses a throwaway config under /tmp/songsmith-demo-setup (see
# demos/setup.yaml). The downloads and Ollama are demos/setupfake on a
# loopback port serving small stand-in files (SONGSMITH_DEMO_SETUP and
# SONGSMITH_DEMO_SETUP_FIXTURES), ComfyUI's install is simulated, and a
# stub stands in for ffprobe: nothing real is downloaded or installed.
# Record with: just demo-setup

Output demos/setup.gif

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
Type "rm -rf /tmp/songsmith-demo-setup && mkdir -p /tmp/songsmith-demo-setup/bin && cp demos/setup.yaml /tmp/songsmith-demo-setup/config.yaml" Enter
Type "printf '#!/bin/sh\nexit 0\n' > /tmp/songsmith-demo-setup/bin/ffprobe && chmod +x /tmp/songsmith-demo-setup/bin/ffprobe" Enter
Type "export PATH=$PWD/bin:/tmp/songsmith-demo-setup/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-setup/config.yaml SONGSMITH_DEMO_SETUP=http://127.0.0.1:18490 SONGSMITH_DEMO_SETUP_FIXTURES=1" Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 3.5s

# Finish setup is preselected: Setup lists each missing piece and who installs it.
Enter
Sleep 3.5s

# Install everything missing runs the plan in order: G goes to the last
# row, Re-run checks, and the install row is just above it.
Type "G"
Sleep 600ms
Up
Sleep 2s
Enter
Sleep 6s

# The install keeps going on the launcher, with progress in the chip.
Escape
Sleep 4s

# Back in Setup it is still running, then every row turns OK.
Enter
Sleep 10s

# The launcher is ready and Finish setup is gone.
Escape
Sleep 4s

Type "q"
Sleep 500ms
Hide
Type "rm -rf /tmp/songsmith-demo-setup" Enter
