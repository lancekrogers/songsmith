# Rendering on more than one computer: Settings → Machines with this
# computer, Studio Mac (ready and measured), Attic PC (offline) and MacBook
# Air (its VAE file missing); Find on Tailscale listing the tailnet (Den iMac
# ready, Garage Mini offline, Kitchen PC not answering, with the fix under
# it, and Studio Mac added already), then adding Den iMac under its Tailscale
# name, checked before it is saved; the song form's Render on row pinning a
# song to Studio Mac; and the library with two songs rendering at once on
# different machines, one waiting for Attic PC to come back and one waiting
# for Studio Mac. Uses a throwaway config under /tmp/songsmith-demo-machines
# (see demos/machines.yaml); the tailnet is demos/tailnet.json, every ComfyUI
# is demos/machinesfake on a loopback port and the writer is
# demos/setupfake, so nothing real is reached, rendered or played.
# Record with: just demo-machines

Output demos/machines.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1300
Set Height 820
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 18
Set TypingSpeed 60ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR CLICOLOR VISUAL EDITOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1 TERM=xterm-256color SONGSMITH_COLOR_PROFILE=ansi" Enter
Type "rm -rf /tmp/songsmith-demo-machines && mkdir -p /tmp/songsmith-demo-machines/bin && cp demos/machines.yaml /tmp/songsmith-demo-machines/config.yaml" Enter
Type "d=/tmp/songsmith-demo-machines/ComfyUI && mkdir -p $d/.venv/bin $d/.venv/lib/python3.12/site-packages/torch $d/models/diffusion_models $d/models/text_encoders $d/models/vae && touch $d/main.py $d/.venv/bin/python $d/.venv/lib/python3.12/site-packages/torch/__init__.py" Enter
Type "for f in diffusion_models/minimax_music3_dit_fp16.safetensors:4914197682 text_encoders/minimax_music3_text_encoder_pruned_bf16.safetensors:16706629398 vae/minimax_music3_dav.safetensors:216696128; do dd if=/dev/zero of=$d/models/${f%%:*} bs=1 count=0 seek=${f##*:} 2>/dev/null; done" Enter
Type "for b in ffprobe ffmpeg; do printf '#!/bin/sh\nexit 0\n' > /tmp/songsmith-demo-machines/bin/$b && chmod +x /tmp/songsmith-demo-machines/bin/$b; done" Enter
Type "export PATH=$PWD/bin:/tmp/songsmith-demo-machines/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-machines/config.yaml SONGSMITH_DEMO_SETUP=http://127.0.0.1:18490 SONGSMITH_DEMO_TAILNET=$PWD/demos/tailnet.json" Enter
# Stop here unless this shell runs the fresh build on the scratch config.
Type `[ "$SONGSMITH_CONFIG" = /tmp/songsmith-demo-machines/config.yaml ] && [ "$(command -v songsmith)" = "$PWD/bin/songsmith" ] || exit 1` Enter
Type "/tmp/songsmith-demo-machinesfake/machinesfake -seed $SONGSMITH_CONFIG || exit 1" Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 5s

# Settings is three down once Now rendering is listed; 7 is Machines.
Down@500ms 3
Sleep 600ms
Enter
Sleep 1.5s
Type "7"
Sleep 4.5s

# Down through Studio Mac, Attic PC (offline) and MacBook Air (a model file missing).
Down@350ms 11
Sleep 3s
Down@350ms 10
Sleep 4s

# Find on Tailscale, just above Add: every computer on the tailnet and what its ComfyUI answered.
Type "G"
Sleep 1.5s
Up
Sleep 2s
Enter
Sleep 5s

# Kitchen PC is on Tailscale but nothing answers; the line under it says what to set there.
Down@700ms 3
Sleep 6s

# Den iMac: its Tailscale name filled in, then what the check found before it is saved.
Up@700ms 2
Sleep 2.5s
Enter
Sleep 2.5s
Enter
Sleep 4s
Enter
Sleep 4s

# The library, one up from Settings: two songs rendering at once on different machines.
Escape
Sleep 1.5s
Up
Sleep 600ms
Enter
Sleep 4s

# Pin Glass Harbor to Studio Mac on the song form's Render on row, and queue it.
Type "e"
Sleep 2.5s
Up
Sleep 1.5s
Right@900ms 2
Sleep 2.5s
Ctrl+S
Sleep 4s

# Each song says where it renders or what it waits for, and each take where it was rendered.
Down
Sleep 3.5s
Down
Sleep 3s
Down
Sleep 3.5s
Down@600ms 2
Sleep 3.5s
Escape
Sleep 2.5s
Type "q"
Sleep 1s

Hide
Type "rm -rf /tmp/songsmith-demo-machines" Enter
