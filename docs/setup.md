# Setting up ComfyUI for songsmith

songsmith renders through ComfyUI's native MiniMax Music 3 nodes. The official
MiniMax runtime (SGLang-Omni) needs two CUDA GPUs; ComfyUI runs the model on a single
GPU, including Apple Silicon.

## 1. Install ComfyUI

The MiniMax Music 3 nodes landed in ComfyUI in 2026; use a current checkout.

```bash
git clone https://github.com/comfyanonymous/ComfyUI.git /path/to/ComfyUI
cd /path/to/ComfyUI
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python torch torchvision torchaudio -r requirements.txt
```

songsmith expects the virtualenv at `<comfyui dir>/.venv`. The default folder is
`~/.local/share/songsmith/ComfyUI`; if your ComfyUI is elsewhere, set `comfyui.dir` in
the config, for example `dir: /path/to/ComfyUI`.

## 2. Download the model files

From [Comfy-Org/MiniMax-Music-3](https://huggingface.co/Comfy-Org/MiniMax-Music-3), about
22 GB. The repo layout matches ComfyUI's `models/` folder:

```bash
cd /path/to/ComfyUI
hf download Comfy-Org/MiniMax-Music-3 \
  diffusion_models/minimax_music3_dit_fp16.safetensors \
  text_encoders/minimax_music3_text_encoder_pruned_bf16.safetensors \
  vae/minimax_music3_dav.safetensors \
  --local-dir models
```

The repo also has INT8 files (`minimax_music3_dit_int8_convrot`,
`minimax_music3_text_encoder_pruned_int8_convrot`, about 12 GB in total) meant for
NVIDIA cards with 16 GB. On Apple Silicon use the BF16 text encoder above.

## 3. Start the server through songsmith

```bash
songsmith server start
songsmith doctor
```

songsmith starts ComfyUI with `--gpu-only`. **This flag matters on Apple Silicon**:
without it ComfyUI keeps the text encoder, which is an 8B language model, on the CPU,
and a 30-second song takes about an hour instead of about four minutes. If you run
ComfyUI yourself, pass `--gpu-only`; `songsmith doctor` flags a server without it.

The server keeps running after songsmith exits so models stay loaded between songs.
Stop it with `songsmith server stop`. Its log is at
`~/.local/state/songsmith/comfyui.log`.

## 4. Rendering on another computer too

Another computer with ComfyUI and the model files can render songs for this one.
On that computer, follow steps 1 and 2 (or run `songsmith setup --install` there), set
`comfyui.host` in its config to an address this computer can reach, such as its
Tailscale IP, and run `songsmith server start`. Then, on this computer:

```bash
songsmith machines add "MacBook Pro" http://100.101.102.103:8188
```

ComfyUI has no login, so keep that address on a private network such as Tailscale,
never a public one. The README's "Rendering on more than one computer" covers how
songs are shared out and the known limits.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `doctor`: ComfyUI nodes missing | Update ComfyUI (`git pull`, reinstall requirements) |
| `doctor`: ComfyUI cannot see a model file | Check the file name in config, then restart ComfyUI |
| Renders extremely slow | Make sure the server runs with `--gpu-only` (`songsmith server status`) |
| Draft fails | Check `ollama.model` is installed (`ollama list`); you can still write lyrics by hand |
| `songsmith machines`: a machine is offline | Wake that computer, run `songsmith server start` there, and check its `comfyui.host` is an address this computer can reach |
| `songsmith machines`: models missing | Install the model files on that computer, or set its file names in its `machines` entry, then restart ComfyUI there |
