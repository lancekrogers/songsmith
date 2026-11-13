# songsmith

Write and render songs with vocals on your own machine. You describe a song, a local
Ollama model drafts the lyrics and a style caption, you edit them, and
[MiniMax Music 3](https://huggingface.co/MiniMaxAI/MiniMax-Music3) renders the song
through a local [ComfyUI](https://github.com/comfyanonymous/ComfyUI) server. Nothing
leaves the machine.

![songsmith TUI](demos/tui.gif)

## What to expect

Rendering is slow. Measured on a Mac Studio (M4 Max, 128 GB):

| Song | Render time |
|---|---|
| 20 s | about 2.5 min |
| 30 s | about 4-5 min |
| 61 s | about 10 min |

Time grows faster than the song length, so a 3-minute song can take 30 minutes or more.
songsmith queues renders and works through them in the background while you keep
writing.

## Requirements

- macOS on Apple Silicon (tested) or Linux with an NVIDIA GPU (untested)
- Room for about 22 GB of MiniMax Music 3 model files in GPU memory, plus your Ollama model
- ComfyUI with the MiniMax Music 3 model files: see [docs/setup.md](docs/setup.md)
- [Ollama](https://ollama.com) with a chat model, for drafting lyrics (optional: you can write them yourself)
- `ffprobe` (from ffmpeg) to record song lengths; `ffplay` for playback on Linux

## Install

```bash
go install github.com/lancekrogers/songsmith/cmd/songsmith@latest
# or from a checkout
just install
```

Then check the setup:

```bash
songsmith doctor
```

## Use

```bash
songsmith                     # open the TUI
songsmith write "a sea shanty about debugging production at 3am"
songsmith generate --seconds 60 "lo-fi study beat about rain on a tin roof"
songsmith generate --title "Coast Road" --lyrics lyrics.txt --caption caption.txt --seconds 90
songsmith list
songsmith server start|status|stop
songsmith config              # print the effective configuration
```

### TUI keys

| Key | Action |
|---|---|
| `n` | New song: type an idea, `enter` drafts it with Ollama, `tab` writes it yourself |
| `e` | Edit the selected song (editing a finished song makes a copy) |
| `enter` / `space` | Play or stop the selected finished song |
| `r` | Queue the selected song (a finished song renders a new take with a new seed; earlier takes stay on disk) |
| `x` | Cancel a queued or rendering song |
| `q` | Quit; ComfyUI keeps rendering the current song and songsmith picks up the result next time it starts |

In the editor: `tab` moves between title, length, lyrics and style, `ctrl+e` opens
lyrics or style in `$EDITOR`, `ctrl+s` saves and queues, `ctrl+d` saves a draft, and
`esc` discards changes.

### Writing for MiniMax Music 3

- **Lyrics** use section tags on their own lines: `[Intro]`, `[Verse]`, `[Pre-Chorus]`,
  `[Chorus]`, `[Post-Chorus]`, `[Bridge]`, `[Instrumental]`, `[Solo]`, `[Outro]`.
  The model sings what you write; song length follows the lyrics, up to the length you set.
- **Style** has three parts:
  - `Global Metadata:` genre, tempo, key, emotional arc, production
  - `Vocal Details:` voice, delivery, harmonies
  - `Arrangement:` what each section adds or drops

## Configuration

`~/.config/songsmith/config.yaml` (or `--config`, or `$SONGSMITH_CONFIG`). Every key is
optional; `songsmith config` prints the effective values.

```yaml
comfyui:
  dir: /Volumes/fast/ComfyUI
  host: 127.0.0.1
  port: 8188
  diffusion_model: minimax_music3_dit_fp16.safetensors
  text_encoder: minimax_music3_text_encoder_pruned_bf16.safetensors
  vae: minimax_music3_dav.safetensors
ollama:
  url: http://127.0.0.1:11434
  model: qwen3.8:latest
render:
  steps: 30
  cfg_scale: 1.7
  top_k: 50
library_dir: ~/Music/Songsmith
state_dir: ~/.local/state/songsmith
```

Each song lives in its own folder under `library_dir` with `song.json` (lyrics, style,
seed, status, timings) and its audio (`song.flac`, then `song-2.flac` and so on for
later takes). Only one songsmith process renders at a time; another open TUI waits and
takes over when the first one quits.

## Development

```bash
just build          # bin/songsmith
just test unit      # unit tests
just test race      # with the race detector
just test lint      # go vet + gofmt
just test hardware  # renders a 20 s song on the real ComfyUI install
```

## License

MiniMax Music 3 weights are under the
[MiniMax-Music3 Community License](https://huggingface.co/MiniMaxAI/MiniMax-Music3/blob/main/LICENSE).
Personal use is unrestricted; commercial products must credit "MiniMax-Music3" in their
interface and add safeguards against infringing use.
