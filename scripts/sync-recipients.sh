#!/usr/bin/env bash
# Rebuild postfix's relay_recipients map from HestiaCP on LXC 160.
#
# Runs on the VPS mail edge. Pulls over the WireGuard tunnel using a key that is
# locked (via authorized_keys command=) to /usr/local/sbin/list-recipients.py,
# so this host can obtain the recipient list and nothing else.
#
# The map is what stops the VPS accepting mail for addresses that do not exist
# and then bouncing it. A truncated map is therefore worse than a stale one:
# if the upstream returns implausibly few entries we keep what we have.
set -euo pipefail

OUT=/etc/postfix/relay_recipients
KEY=/root/.ssh/hestia_sync
PEER=root@10.10.10.1
MIN_ENTRIES=20

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

ssh -i "$KEY" -o BatchMode=yes -o ConnectTimeout=15 "$PEER" > "$TMP" 2>/dev/null || {
  echo "REFUSING: could not reach Hestia; keeping existing map" >&2
  exit 1
}

# Every line must look like "<addr> OK" or "@<domain> OK".
if grep -qvE '^[^ ]+ OK$' "$TMP"; then
  echo "REFUSING: malformed output from Hestia; keeping existing map" >&2
  exit 1
fi

COUNT=$(grep -c ' OK$' "$TMP" || true)
if [ "$COUNT" -lt "$MIN_ENTRIES" ]; then
  echo "REFUSING: only $COUNT recipients returned (min $MIN_ENTRIES); keeping existing map" >&2
  exit 1
fi

if [ -f "$OUT" ] && cmp -s "$TMP" "$OUT"; then
  echo "relay_recipients unchanged: $COUNT entries"
  exit 0
fi

install -m 644 "$TMP" "$OUT"
postmap "$OUT"
echo "relay_recipients updated: $COUNT entries"
