#!/bin/sh
# Start Yarr.It inside the unified image.
#
# Keys minted by the `link` service land in /state/engines.env. Anything the
# operator set explicitly in the environment wins over them, so pointing a
# bundled install at an existing service is an env change, not a rebuild.
set -eu

if [ -f /state/engines.env ]; then
  while IFS='=' read -r k v; do
    case "$k" in ''|\#*) continue ;; esac
    eval "cur=\${$k:-}"
    [ -z "$cur" ] && export "$k=$v"
  done < /state/engines.env
fi

mkdir -p /data/search-cache /data/linear

# All three processes are children of this script, which stays in the
# foreground as their supervisor. If any one of them exits -- mw-search refusing
# a broken library.json, say -- the others are stopped and the container exits
# non-zero, so the restart policy brings the whole stack back once the cause is
# fixed, instead of Caddy serving 502s from a dead backend indefinitely.
mw-bridge -addr 127.0.0.1:8801 &
BRIDGE=$!
set -- -addr 127.0.0.1:8802
[ -n "${PROWLARR_URL:-}" ] && set -- "$@" -prowlarr "$PROWLARR_URL"
mw-search "$@" &
SEARCH=$!
caddy run --config /etc/caddy/Caddyfile --adapter caddyfile &
CADDY=$!

stop_all() {
  kill -TERM "$BRIDGE" "$SEARCH" "$CADDY" 2>/dev/null || true
  wait
}
trap 'stop_all; exit 0' TERM INT

# BusyBox ash has no `wait -n`, but it reaps finished background jobs, so a
# failed `kill -0` means that process is gone.
while kill -0 "$BRIDGE" 2>/dev/null && kill -0 "$SEARCH" 2>/dev/null && kill -0 "$CADDY" 2>/dev/null; do
  sleep 2
done
for p in "mw-bridge:$BRIDGE" "mw-search:$SEARCH" "caddy:$CADDY"; do
  kill -0 "${p#*:}" 2>/dev/null || echo "yarrit: ${p%%:*} exited; stopping the container" >&2
done
stop_all
exit 1
