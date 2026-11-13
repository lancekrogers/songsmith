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
- `ffprobe` (from ffmpeg) to record song lengths; `ffplay` for playback on Linux (untested)

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

### Installing ComfyUI and the models

`songsmith setup` lists what is installed and what is missing; `songsmith setup --install`
installs what is missing, in order (`--comfyui`, `--models` or `--writer` limit it to one
part):

1. **ComfyUI** into `comfyui.dir`: a `git clone`, a `.venv` with Python 3.12 through
   [uv](https://docs.astral.sh/uv/) when it is installed, otherwise through an installed
   Python 3.12 or 3.13 (other versions are refused), then torch and ComfyUI's
   requirements. Needs `git`. Output goes to `<state_dir>/install.log`. On Linux the
   default torch wheel is the CUDA build; the Linux path is untested.
2. **MiniMax Music 3 files** from Hugging Face, about 22 GB. Each download is checked
   against its SHA-256. If it is interrupted (ctrl+c is fine), the `.part` file stays and
   the next run resumes it.
3. **Writer model** through Ollama. songsmith does not install Ollama itself; get it from
   [ollama.com](https://ollama.com/download). Lyrics can be written by hand without it.

## Use

```bash
songsmith                     # open the TUI
songsmith write "a sea shanty about debugging production at 3am"
songsmith generate --seconds 60 "lo-fi study beat about rain on a tin roof"
songsmith generate --title "Coast Road" --lyrics lyrics.txt --caption caption.txt --seconds 90
songsmith list                # status, title, length, seed, created, ID
songsmith revise <song-id> "make it longer with two more verses that continue the story"
songsmith revise --save <song-id> "add a bridge"   # keep it as a new draft (--queue also queues it)
songsmith server start|status|stop
songsmith config              # print the effective configuration
```

### TUI keys

| Key | Action |
|---|---|
| `n` | New song: type an idea and `enter` drafts it with Ollama, or fill in the fields yourself |
| `e` | Edit the selected song (editing a finished song makes a copy) |
| `enter` / `space` | Play or stop the selected finished song |
| `r` | Queue the selected song (a finished song renders a new take with a new seed; earlier takes stay on disk) |
| `v` | Revise or extend the selected song with the writer (a finished song gets a copy with the same seed, pinned) |
| `s` | Copy the selected song's seed to the clipboard |
| `x` | Cancel a queued or rendering song |
| `q` | Quit; ComfyUI keeps rendering the current song and songsmith picks up the result next time it starts |

In the song form: `↑/↓` or `tab` move between fields, `←/→` change Length and Vocals,
`enter` opens Lyrics or a Style part in the built-in vim-style editor (`:w` or `enter`
in NORMAL goes back, `:q` drops that field's changes, `:wq` saves a draft, `[` then
`tab` completes a section tag), `ctrl+e` opens the field in `$VISUAL` or `$EDITOR`,
`ctrl+s` saves and queues, and `ctrl+d` saves a draft. `esc` on a changed form asks
once before discarding. On the Seed row, `p` pins the seed the last render used, or
unpins a pinned one.

`ctrl+r` revises a song that has lyrics: say what should change ("make it longer with
two more verses that continue the story") and the writer returns the whole song with
that change. Nothing changes until you review it: added lines are marked `+`, removed
ones `-`, and `enter` applies it to the form, `r` asks again, `e` edits the instruction
and `esc` discards it. MiniMax Music 3 cannot continue a recording, so an extended song
is a new performance of the new lyrics with the same seed and style.

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
  dir: ~/.local/share/songsmith/ComfyUI
  host: 127.0.0.1
  port: 8188
  diffusion_model: minimax_music3_dit_fp16.safetensors
  text_encoder: minimax_music3_text_encoder_pruned_bf16.safetensors
  vae: minimax_music3_dav.safetensors
ollama:
  url: http://127.0.0.1:11434
  model: qwen3.8:latest
  temperature: 0.8
  instructions_file: ""   # a text file that replaces the built-in writing instructions; relative to this file
  context: 0              # num_ctx for drafts; 0 uses the context Ollama is configured with
  keep_alive: 5m          # how long Ollama keeps the writer loaded after a draft ("0" unloads at once)
  unload_before_render: true  # ask Ollama to unload the writer when a song starts rendering
render:
  steps: 30
  cfg_scale: 1.7
  top_k: 50
  default_seconds: 120    # length a new song starts with
  sampler: euler
  scheduler: simple
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
just test hardware  # renders a 20 s song on the ComfyUI install your config names
```

## License

MiniMax Music 3 weights are under the
[MiniMax-Music3 Community License](https://huggingface.co/MiniMaxAI/MiniMax-Music3/blob/main/LICENSE).
Personal use is unrestricted; commercial products must credit "MiniMax-Music3" in their
interface and add safeguards against infringing use.
