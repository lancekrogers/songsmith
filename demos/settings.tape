# Settings: walk all seven sections on a machine that is ready to render,
# filter the writer's model list, change the temperature and the render
# steps, then show that the config file changed only those keys and kept its
# comments. Uses a throwaway config under /tmp/songsmith-demo-settings (see
# demos/settings.yaml) with stand-in ComfyUI and model files made there;
# Ollama, ComfyUI and the install plan are demos/setupfake on loopback ports.
# Nothing is installed, started or rendered.
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
Type "rm -rf /tmp/songsmith-demo-settings && mkdir -p /tmp/songsmith-demo-settings/bin && cp demos/settings.yaml /tmp/songsmith-demo-settings/config.yaml" Enter
Type "d=/tmp/songsmith-demo-settings/ComfyUI && mkdir -p $d/.venv/bin $d/.venv/lib/python3.12/site-packages/torch $d/models/diffusion_models $d/models/text_encoders $d/models/vae && touch $d/main.py $d/.venv/bin/python $d/.venv/lib/python3.12/site-packages/torch/__init__.py" Enter
Type "for f in diffusion_models/minimax_music3_dit_fp16.safetensors:4914197682 text_encoders/minimax_music3_text_encoder_pruned_bf16.safetensors:16706629398 vae/minimax_music3_dav.safetensors:216696128; do dd if=/dev/zero of=$d/models/${f%%:*} bs=1 count=0 seek=${f##*:} 2>/dev/null; done" Enter
Type "printf '#!/bin/sh\nexit 0\n' > /tmp/songsmith-demo-settings/bin/ffprobe && chmod +x /tmp/songsmith-demo-settings/bin/ffprobe" Enter
Type "export PATH=$PWD/bin:/tmp/songsmith-demo-settings/bin:/usr/bin:/bin SONGSMITH_CONFIG=/tmp/songsmith-demo-settings/config.yaml SONGSMITH_DEMO_SETUP=http://127.0.0.1:18490" Enter
Type "clear" Enter
Show

Type "songsmith"
Sleep 400ms
Enter
Sleep 3s

# Everything is ready, so Settings is the third item.
Down@600ms 2
Sleep 800ms
Enter
Sleep 3.5s

# Writer: about thirty models in a window, two embedding models hidden.
Right
Sleep 3.5s
Down@500ms 3
Sleep 1.5s

# / filters the list; esc clears the filter.
Type "/"
Sleep 800ms
Type@250ms "bal"
Sleep 2.5s
Escape
Sleep 1.5s

# The temperature slider.
Type "G"
Sleep 800ms
Up@500ms 2
Sleep 1s
Type "+"
Sleep 2.5s

# Music model, then Render: two more steps.
Right
Sleep 3s
Right
Sleep 1.5s
Type@600ms "++"
Sleep 2.5s

# Server, Advanced with the server's samplers, and Library.
Right
Sleep 3s
Right
Sleep 3s
Down@400ms 4
Sleep 1.5s
Right
Sleep 3s
Escape
Sleep 1s
Type "q"
Sleep 800ms

# Only the two changed values differ, and every comment is still there.
Type "diff demos/settings.yaml $SONGSMITH_CONFIG"
Sleep 400ms
Enter
Sleep 4s

Hide
Type "rm -rf /tmp/songsmith-demo-settings" Enter
