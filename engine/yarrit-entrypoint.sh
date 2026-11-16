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

mw-bridge -addr 127.0.0.1:8801 &
set -- -addr 127.0.0.1:8802
[ -n "${PROWLARR_URL:-}" ] && set -- "$@" -prowlarr "$PROWLARR_URL"
mw-search "$@" &

exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile
