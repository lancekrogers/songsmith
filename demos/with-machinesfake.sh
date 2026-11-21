#!/usr/bin/env bash
# Runs a command (a vhs recording) while demos/machinesfake stands in for the
# ComfyUI servers of several computers: this computer on 127.0.0.1:18512,
# Studio Mac on :18513, MacBook Air on :18515 and Den iMac on :18516, with
# nothing on :18514 so Attic PC is offline, and nothing on :18517 or :18518,
# where Kitchen PC and Garage Mini of demos/tailnet.json do not answer.
# demos/machines.yaml points songsmith at them, so a recording reaches no
# real machine.
set -euo pipefail
for port in 18512 18513 18514 18515 18516 18517 18518; do
    if nc -z 127.0.0.1 "$port" 2>/dev/null; then
        echo "127.0.0.1:$port is in use; the machines demo needs it free" >&2
        exit 1
    fi
done
rm -rf /tmp/songsmith-demo-machinesfake
mkdir -p /tmp/songsmith-demo-machinesfake
go build -o /tmp/songsmith-demo-machinesfake/machinesfake ./demos/machinesfake
/tmp/songsmith-demo-machinesfake/machinesfake 2>/tmp/songsmith-demo-machinesfake/log &
fake=$!
trap 'kill $fake 2>/dev/null || true; rm -rf /tmp/songsmith-demo-machinesfake' EXIT
for _ in $(seq 50); do
    curl -sf http://127.0.0.1:18512/system_stats >/dev/null &&
        curl -sf http://127.0.0.1:18516/system_stats >/dev/null && break
    sleep 0.1
done
"$@"
