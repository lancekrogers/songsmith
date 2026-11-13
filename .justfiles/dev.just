# Build into bin/
build:
    go build -o {{bin_dir}}/{{binary_name}} ./cmd/songsmith

# Install into $GOBIN
install:
    go install ./cmd/songsmith

# Run songsmith with arguments
run *args:
    go run ./cmd/songsmith {{args}}

# Format all Go files
fmt:
    gofmt -w .

# Record demos/tui.gif, the README's hero: launcher, a draft from the writer, versions and takes, the Master screen (throwaway config in /tmp; no real Ollama or ComfyUI, nothing plays)
demo: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        demos/with-setupfake.sh vhs demos/tui.tape
    ls -lh demos/tui.gif

# Record demos/shell.gif: launcher, library and setup report (throwaway config in /tmp, no Ollama or ComfyUI needed)
demo-shell: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        demos/with-setupfake.sh vhs demos/shell.tape
    ls -lh demos/shell.gif

# Record demos/editor.gif: the song form drafting from an idea with the stand-in writer, and the text editor (throwaway config in /tmp, no real Ollama or ComfyUI)
demo-editor: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        demos/with-setupfake.sh vhs demos/editor.tape
    ls -lh demos/editor.gif

# Record demos/settings.gif: walk Settings and change three values (throwaway config in /tmp, no Ollama or ComfyUI needed)
demo-settings: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        demos/with-setupfake.sh vhs demos/settings.tape
    ls -lh demos/settings.gif

# Record demos/setup.gif: Finish setup installs everything from nothing (throwaway config in /tmp; downloads and Ollama are demos/setupfake on a loopback port)
demo-setup: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        demos/with-setupfake.sh vhs demos/setup.tape
    ls -lh demos/setup.gif

# Record demos/master.gif: the Master screen on a fixture song whose audio is a tone made by ffmpeg (throwaway config in /tmp; nothing plays)
demo-master: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        demos/with-setupfake.sh vhs demos/master.tape
    ls -lh demos/master.gif
