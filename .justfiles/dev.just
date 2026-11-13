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

# Record the TUI demo GIF (needs Ollama; uses a throwaway library in /tmp)
demo: build
    PATH="$PWD/bin:$PATH" vhs demos/tui.tape

# Record demos/shell.gif: launcher, library and setup report (throwaway config in /tmp, no Ollama or ComfyUI needed)
demo-shell: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        vhs demos/shell.tape
    ls -lh demos/shell.gif

# Record demos/editor.gif: the song form and text editor on a hand-written song (throwaway config in /tmp, no Ollama or ComfyUI needed)
demo-editor: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        SONGSMITH_COLOR_PROFILE=ansi \
        vhs demos/editor.tape
    ls -lh demos/editor.gif
