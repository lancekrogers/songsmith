#!/usr/bin/env bash
# Runs a command (a vhs recording) while demos/setupfake stands in for
# Hugging Face and Ollama on 127.0.0.1:18490, then stops it. Tapes point
# songsmith at it with SONGSMITH_DEMO_SETUP, so a recording reaches no real
# host and downloads nothing real.
set -euo pipefail
rm -rf /tmp/songsmith-demo-setupfake
mkdir -p /tmp/songsmith-demo-setupfake
go build -o /tmp/songsmith-demo-setupfake/setupfake ./demos/setupfake
/tmp/songsmith-demo-setupfake/setupfake -addr 127.0.0.1:18490 -model songwriter:demo 2>/tmp/songsmith-demo-setupfake/log &
fake=$!
trap 'kill $fake 2>/dev/null || true; rm -rf /tmp/songsmith-demo-setupfake' EXIT
for _ in $(seq 50); do
    curl -sf http://127.0.0.1:18490/catalog.json >/dev/null && break
    sleep 0.1
done
"$@"
