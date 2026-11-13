#!/usr/bin/env bash
# Prepares /tmp/songsmith-demo-hero for demos/tui.tape: a throwaway config,
# the demos/hero-fixture library with tones made by ffmpeg standing in for
# its audio, and a ComfyUI folder laid out like a finished install with
# demos/setupfake's small model files, so the launcher reads ready. Needs
# demos/setupfake running (demos/with-setupfake.sh). Nothing real is
# installed, rendered or played.
set -euo pipefail
root=/tmp/songsmith-demo-hero
fake=http://127.0.0.1:18490
rm -rf "$root"
mkdir -p "$root/library" "$root/state" "$root/bin"
cp demos/hero.yaml "$root/config.yaml"
cp -R demos/hero-fixture/. "$root/library/"

tone() { # file seconds root-hz
    ffmpeg -nostdin -v error -f lavfi \
        -i "aevalsrc='0.06*sin(2*PI*$3*t)*(0.7+0.3*sin(2*PI*0.25*t))+0.04*sin(2*PI*$3*1.5*t)+0.003*(random(0)-0.5)|0.06*sin(2*PI*$3*t)*(0.7+0.3*sin(2*PI*0.25*t+1))+0.04*sin(2*PI*$3*2*t)+0.003*(random(1)-0.5)':s=44100:d=$2" \
        -c:a flac "$1"
}
lib="$root/library"
tone "$lib/20261113-090000-lantern-road/song.flac" 120 147
tone "$lib/20261113-090000-lantern-road/song.master-1.flac" 118 147
tone "$lib/20261113-094500-lantern-road-v2/song.flac" 120 165
tone "$lib/20261112-200000-neon-water/song.flac" 60 220

comfy="$root/ComfyUI"
for p in main.py .venv/bin/python .venv/lib/python3.12/site-packages/torch/__init__.py; do
    mkdir -p "$(dirname "$comfy/$p")"
    : > "$comfy/$p"
done
curl -sf "$fake/catalog.json" |
    python3 -c 'import json, sys; [print(f["dir"], f["name"]) for f in json.load(sys.stdin)]' |
    while read -r dir name; do
        mkdir -p "$comfy/models/$dir"
        curl -sf "$fake/hf/$dir/$name" -o "$comfy/models/$dir/$name"
    done

ln -s "$(command -v ffmpeg)" "$root/bin/ffmpeg"
ln -s "$(command -v ffprobe)" "$root/bin/ffprobe"
for player in afplay ffplay; do
    printf '#!/bin/sh\nexit 0\n' > "$root/bin/$player"
    chmod +x "$root/bin/$player"
done
