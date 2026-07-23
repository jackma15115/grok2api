#!/bin/sh
set -eu

cd /app
python3 -u flaresolverr.py &
flare_pid=$!

ready=false
for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8191/health >/dev/null 2>&1; then
    ready=true
    break
  fi
  if ! kill -0 "$flare_pid" 2>/dev/null; then
    exit 1
  fi
  sleep 1
done
if [ "$ready" != "true" ]; then
  exit 1
fi

node /opt/seed-hex-catch/src/server.mjs &
catch_pid=$!

shutdown() {
  kill -TERM "$catch_pid" "$flare_pid" 2>/dev/null || true
  wait "$catch_pid" "$flare_pid" 2>/dev/null || true
}

trap shutdown INT TERM EXIT
while kill -0 "$flare_pid" 2>/dev/null && kill -0 "$catch_pid" 2>/dev/null; do
  sleep 1
done
exit 1
