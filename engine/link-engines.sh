#!/bin/sh
# Hand the bundled ROMarr's API key to Yarr.It, once, with nobody copying it.
#
# Run by the `link` service in engine/compose.yaml. ROMarr writes its key into
# its own config on first start; this reads it from the shared volume rather
# than guessing, and writes /state/engines.env for Yarr.It's entrypoint.
# Idempotent: a key already recorded is kept.
set -eu

STATE=${STATE:-/state}
OUT="$STATE/engines.env"
ROMARR_CONF=${ROMARR_CONF:-/romarr/romarr.json}
umask 077
mkdir -p "$STATE"

log() { printf 'link: %s\n' "$*"; }

romarr_key=""
[ -f "$OUT" ] && . "$OUT" && romarr_key=${ROMARR_API_KEY:-}

if [ -n "${ROMARR_URL:-}" ] && [ -z "$romarr_key" ]; then
  i=0
  until curl -fsS -o /dev/null "$ROMARR_URL/api/health"; do
    i=$((i + 1)); [ "$i" -ge 90 ] && { log "ROMarr did not come up"; break; }; sleep 2
  done
  i=0
  while [ -z "$romarr_key" ] && [ "$i" -lt 30 ]; do
    [ -f "$ROMARR_CONF" ] && romarr_key=$(jq -r '.settings._api_key // empty' "$ROMARR_CONF")
    [ -z "$romarr_key" ] && sleep 2
    i=$((i + 1))
  done
fi
[ -n "$romarr_key" ] && log "romarr linked" || log "romarr key not found in $ROMARR_CONF"

printf 'ROMARR_API_KEY=%s\n' "$romarr_key" > "$OUT"
log "wrote $OUT"
