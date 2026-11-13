<h1 align="center">♪ songsmith</h1>

<p align="center">
  <strong>Write and render songs with vocals on your own machine.</strong><br>
  Describe a song, a local model drafts the lyrics and the style, you edit them,<br>
  and MiniMax Music 3 sings it. Nothing leaves the machine.
</p>

<p align="center">
  <img src="https://img.shields.io/badge/go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go 1.26+">
  <img src="https://img.shields.io/badge/platform-macOS%20(Apple%20Silicon)%20%7C%20Linux-5DADE2" alt="Platforms: macOS on Apple Silicon and Linux">
  <a href="https://huggingface.co/MiniMaxAI/MiniMax-Music3"><img src="https://img.shields.io/badge/music-MiniMax%20Music%203-FF6F3C" alt="Music by MiniMax Music 3"></a>
  <a href="https://ollama.com"><img src="https://img.shields.io/badge/writer-Ollama-000000?logo=ollama&logoColor=white" alt="Writer: Ollama"></a>
  <img src="https://img.shields.io/badge/runs-100%25%20local-2EA44F" alt="Runs 100% locally">
  <a href="#license"><img src="https://img.shields.io/badge/license-MIT%20or%20Apache--2.0-1ABC9C" alt="License: MIT or Apache-2.0"></a>
  <a href="https://github.com/Obedience-Corp/festival"><img src="https://img.shields.io/badge/built%20with-Festival-7c3aed.svg" alt="Built with Festival"></a>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#the-terminal-ui">Terminal UI</a> ·
  <a href="#command-line">Command line</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#development">Development</a>
</p>

<p align="center">
  <img src="demos/tui.gif" width="900" alt="songsmith in a terminal: the launcher ready to render, the writer drafting a song called Last Light from a one-line idea, the library with Lantern Road and its new version Lantern Road (v2), switching Lantern Road between its render and its mastered take, and the Master screen applying the Streaming preset with loudness moving from -25.7 to -14.1 LUFS">
</p>

<p align="center"><sub>The launcher, a draft from the writer, versions and takes in the library, and the Master screen.</sub></p>

> [!NOTE]
> **Early and experimental.** Tested on a Mac Studio (M4 Max). The Linux and NVIDIA
> path is written but untested. Renders are slow: see [What to expect](#what-to-expect).

## Why songsmith

<table>
<tr>
<td width="50%" valign="top">

**Local from end to end**<br>
Lyrics come from a model you run in [Ollama](https://ollama.com); the music comes
from [MiniMax Music 3](https://huggingface.co/MiniMaxAI/MiniMax-Music3) in a
[ComfyUI](https://github.com/comfyanonymous/ComfyUI) server songsmith starts and
stops for you. No account, no upload.

</td>
<td width="50%" valign="top">

**A writer that drafts, you decide**<br>
One line of idea becomes a title, tagged lyrics and a three-part style. Edit any of
it in the built-in vim-style editor, or ask the writer to revise: "add a bridge",
"two more verses". Every revision is shown as a diff before it lands.

</td>
</tr>
<tr>
<td valign="top">

**Nothing is overwritten**<br>
Rendering a finished song again makes a new version, "Title (v2)", with its own
audio and seed. Masters are new takes beside the render. Pick the take the library
plays.

</td>
<td valign="top">

**Master in the terminal**<br>
Before and after meters for loudness, true peak, range and hiss. Presets for
streaming, loud and warm, 30-second previews against the original, and an option
to master every new render automatically.

</td>
</tr>
<tr>
<td valign="top">

**Keeps working while you write**<br>
Renders queue and run in the background. Quit and ComfyUI keeps rendering;
songsmith picks the result up the next time it starts.

</td>
<td valign="top">

**Sets itself up**<br>
Finish setup installs ComfyUI and the 22 GB of model files from inside the app,
with checksums and resumable downloads, and says what is missing and why.

</td>
</tr>
</table>

## What to expect

Rendering is slow. Measured on a Mac Studio (M4 Max, 128 GB):

| Song | Render time |
|---|---|
| 20 s | about 2.5 min |
| 30 s | about 4–5 min |
| 61 s | about 10 min |

Time grows faster than the song length, so a 3-minute song can take 30 minutes or
more. Renders longer than about 3:00 have come out with static on the test machine;
the song form warns when the length or the lyrics point past that.

## Requirements

- macOS on Apple Silicon (tested) or Linux with an NVIDIA GPU (untested)
- Room for about 22 GB of MiniMax Music 3 model files in GPU memory, plus your Ollama model
- [Ollama](https://ollama.com) with a chat model to draft lyrics (optional: you can write them yourself)
- `ffmpeg` and `ffprobe` for song lengths and mastering; `ffplay` for playback on Linux
- `git`, and [uv](https://docs.astral.sh/uv/) or Python 3.12/3.13, if songsmith installs ComfyUI for you

## Install

```bash
go install github.com/lancekrogers/songsmith/cmd/songsmith@latest
```

Or from a checkout:

```bash
git clone https://github.com/lancekrogers/songsmith
cd songsmith
just install
```

### ComfyUI and the model files

Open `songsmith` and choose **Finish setup**, or from the command line:

```bash
songsmith setup              # what is installed and what is missing
songsmith setup --install    # install what is missing (--comfyui, --models or --writer for one part)
songsmith doctor             # check ComfyUI, the model files and Ollama
```

1. **ComfyUI** into `comfyui.dir`: a `git clone`, a `.venv` with Python 3.12 through
   uv when it is installed, otherwise through an installed Python 3.12 or 3.13, then
   torch and ComfyUI's requirements. Output goes to `<state_dir>/install.log`.
2. **MiniMax Music 3 files** from Hugging Face, about 22 GB. Each download is checked
   against its SHA-256. An interrupted download (ctrl+c is fine) resumes from its
   `.part` file next time.
3. **The writer model** through Ollama. songsmith does not install Ollama itself; get
   it from [ollama.com](https://ollama.com/download).

Already have ComfyUI? Point `comfyui.dir` at it in Settings → Server.
[docs/setup.md](docs/setup.md) covers a manual install.

<details>
<summary>Recording: Finish setup on a machine with nothing installed</summary>
<br>
<img src="demos/setup.gif" width="900" alt="Finish setup installing ComfyUI, the MiniMax Music 3 files and the writer model, ending with the launcher ready to render">
</details>

## Quick start

```bash
songsmith
```

1. **New song**: type an idea ("a sea shanty about debugging production at 3am") and
   draft it with the writer, or fill in the title, lyrics and style yourself.
2. Read the draft, change what you like, and press `ctrl+s` to save and queue it.
3. Keep writing while it renders. When it is done, `enter` plays it from the library.

## The terminal UI

### Library

| Key | Action |
|---|---|
| `n` | New song |
| `enter` / `space` | Play or stop the selected finished song |
| `g` | Draft lyrics and style for a song that only has an idea |
| `e` | Edit the selected song (editing a finished song makes a copy) |
| `r` | Queue the selected song. On a finished song it queues a new version, "Title (v2)", with a new seed unless the seed is pinned; the finished song keeps its audio and seed |
| `v` | Revise or extend the selected song with the writer (a finished song gets a copy with the same seed, pinned) |
| `m` | Master the selected finished song |
| `t` | Switch to the song's next take |
| `s` | Copy the selected song's seed |
| `x` | Cancel a queued or rendering song |
| `q` | Quit. ComfyUI keeps rendering, and songsmith picks up the result next time |

### Song form

`↑/↓` or `tab` move between fields, and `←/→` change Length and Vocals. `enter` opens
Lyrics or a Style part in the built-in vim-style editor: `:w` or `enter` in NORMAL
goes back, `:q` drops that field's changes, `:wq` saves a draft, and `[` then `tab`
completes a section tag. `ctrl+e` opens the field in `$VISUAL` or `$EDITOR`.
`ctrl+s` saves and queues, `ctrl+d` saves a draft, and `esc` on a changed form asks
once before discarding. On the Seed row, `p` pins the seed the last render used, or
unpins it.

`ctrl+r` revises a song that has lyrics. Say what should change ("make it longer with
two more verses that continue the story") and the writer returns the whole song with
that change. Nothing changes until you review it: added lines are marked `+` and
removed ones `-`. `enter` applies it, `r` asks again, `e` edits the instruction and
`esc` discards it. MiniMax Music 3 cannot continue a recording, so an extended song is
a new performance of the new lyrics with the same seed and style.

<details>
<summary>Recording: drafting, the text editor and a revision</summary>
<br>
<img src="demos/editor.gif" width="900" alt="The song form drafting from a long idea, adding an outro in the vim-style editor with section-tag completion, saving a draft, and revising it with the writer">
</details>

### Master screen

`m` on a finished song measures it and prepares a 30-second excerpt. The meters show
loudness, true peak, range, hiss, DC offset and length, before and after.

- **Listen:** `space` plays the processed preview and `o` the original from the same
  spot; `[` and `]` move the spot by 15 seconds.
- **Presets:** Streaming (−14 LUFS), Loud (−10 LUFS), Warm and Fix abrupt ending, plus
  sliders for level, tone and edges. `+`/`-` step a value, and a slider also takes a
  typed value.
- **Repair:** DC and rumble removal, notches for the fixed tones long renders contain,
  and noise-bed reduction. Off by default and never part of a preset. Noise reduction
  dulls the song along with the hiss.
- **Master this take** (`ctrl+s`) renders the whole take as a new file,
  `song.master-1.flac`, in the background. Nothing is replaced; `c` makes a take the
  one the library plays.

<details>
<summary>Recording: mastering a take</summary>
<br>
<img src="demos/master.gif" width="900" alt="The Master screen applying the Streaming preset and a warmer tilt, mastering the take, and making the master the take the library plays">
</details>

### Writing for MiniMax Music 3

- **Lyrics** use section tags on their own lines: `[Intro]`, `[Verse]`, `[Pre-Chorus]`,
  `[Chorus]`, `[Post-Chorus]`, `[Bridge]`, `[Instrumental]`, `[Solo]`, `[Outro]`. The
  model sings what you write; the song's length follows the lyrics, up to the length
  you set.
- **Style** has three parts:
  - `Global Metadata:` genre, tempo, key, emotional arc, production
  - `Vocal Details:` voice, delivery, harmonies
  - `Arrangement:` what each section adds or drops

## Command line

```bash
songsmith                                  # the terminal UI
songsmith write "a sea shanty about debugging production at 3am"
songsmith generate --seconds 60 "lo-fi study beat about rain on a tin roof"
songsmith generate --title "Coast Road" --lyrics lyrics.txt --caption caption.txt --seconds 90
songsmith list                             # status, title, length, seed, created, ID
songsmith revise <song-id> "make it longer with two more verses that continue the story"
songsmith revise --save <song-id> "add a bridge"    # keep it as a new draft; --queue also queues it
songsmith master <song-id> --preset streaming --set-current
songsmith takes <song-id>                  # list takes; --use <file> makes one current
songsmith server start|status|stop
songsmith config                           # the effective configuration
```

`songsmith <command> --help` lists every flag.

## Your library

Each song is a folder under `library_dir`:

```text
~/Music/Songsmith/
├── 20261113-090000-lantern-road/
│   ├── song.json             lyrics, style, seed, status, timings, takes
│   ├── song.flac             the render
│   └── song.master-1.flac    a master of it
└── 20261113-094500-lantern-road-v2/
    ├── song.json             derived_from: 20261113-090000-lantern-road
    └── song.flac
```

A **version** is a new performance: rendering a finished song again makes a new
song, "Title (v2)", that records which song it came from. A **take** is a file of
the same performance: the render and the masters made from it. Nothing deletes or
overwrites either. Only one songsmith process renders at a time; another open
songsmith waits and takes over when the first one quits.

## Configuration

`~/.config/songsmith/config.yaml`, or `--config`, or `$SONGSMITH_CONFIG`. Every key is
optional; `songsmith config` prints the effective values, and Settings in the
terminal UI edits them.

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
  instructions_file: ""       # a text file that replaces the built-in writing instructions; relative to this file
  context: 0                  # num_ctx for drafts; 0 uses the context Ollama is configured with
  keep_alive: 5m              # how long Ollama keeps the writer loaded after a draft ("0" unloads at once)
  unload_before_render: true  # ask Ollama to unload the writer when a song starts rendering
render:
  steps: 30
  cfg_scale: 1.7
  top_k: 50
  default_seconds: 120        # the length a new song starts with
  sampler: euler
  scheduler: simple
master:
  auto: false                 # master every new render with default and make it current
  default: {}                 # e.g. {loudness: -14, ceiling: -1, low_cut: 30}
library_dir: ~/Music/Songsmith
state_dir: ~/.local/state/songsmith
```

## Development

```bash
just build          # bin/songsmith
just test unit      # unit tests
just test race      # with the race detector
just test lint      # go vet + gofmt
just test hardware  # renders a 20 s song on the ComfyUI install your config names
just demo           # re-record demos/tui.gif (also demo-editor, demo-master, demo-setup, demo-settings)
```

The recordings run against `demos/setupfake`, a loopback stand-in for Ollama, ComfyUI
and Hugging Face, so recording one installs, renders and plays nothing real.

This project is planned and built with [Festival](https://github.com/Obedience-Corp/festival).

## Acknowledgements

- [MiniMax Music 3](https://huggingface.co/MiniMaxAI/MiniMax-Music3) by MiniMax, the model that sings
- [ComfyUI](https://github.com/comfyanonymous/ComfyUI), which runs it
- [Ollama](https://ollama.com), which runs the writer
- [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Lip Gloss](https://github.com/charmbracelet/lipgloss) and [VHS](https://github.com/charmbracelet/vhs) from Charm

## License

songsmith is dual licensed under [MIT](LICENSE-MIT) or [Apache-2.0](LICENSE-APACHE), at
your option.

The MiniMax Music 3 weights are not part of songsmith and have their own
[MiniMax-Music3 Community License](https://huggingface.co/MiniMaxAI/MiniMax-Music3/blob/main/LICENSE).
Personal use is unrestricted; commercial products must credit "MiniMax-Music3" in their
interface and add safeguards against infringing use. songsmith shows that credit on
its launcher.
