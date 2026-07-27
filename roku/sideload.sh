#!/usr/bin/env bash
# Sideload the Yarr.It channel onto a Roku in developer mode.
#
#   ROKU_PASS=<dev password> ./sideload.sh [roku-ip]
#
# The password is the one set on the TV when Developer Mode was enabled. It is
# not the Roku account password, and it is not recoverable -- the device only
# stores a hash. To reset it, re-run the dev-mode sequence on the remote:
#   Home x3, Up x2, Right, Left, Right, Left, Right
#
# Roku uses HTTP digest auth here, so --digest is required; basic auth fails.
set -euo pipefail

ROKU=${1:-192.168.0.126}
ZIP=${ZIP:-$(dirname "$0")/Yarr.It-roku.zip}
: "${ROKU_PASS:?set ROKU_PASS to the Roku developer password}"

[ -f "$ZIP" ] || { echo "missing $ZIP -- run package.sh first"; exit 1; }

echo "==> uploading $(basename "$ZIP") to $ROKU"
RESP=$(curl -sS --max-time 90 \
  --digest -u "rokudev:${ROKU_PASS}" \
  -F "mysubmit=Install" \
  -F "archive=@${ZIP}" \
  -F "passwd=" \
  "http://${ROKU}/plugin_install")

# The dev server answers with an HTML page; the interesting part is one line.
MSG=$(printf '%s' "$RESP" | grep -oE 'Application Received[^<]*|Identical to previous version[^<]*|Install Failure[^<]*|Compilation Failed[^<]*' | head -1 || true)

if [ -z "$MSG" ]; then
  if printf '%s' "$RESP" | grep -qi "401\|unauthor"; then
    echo "!! authentication failed - wrong developer password"
    exit 1
  fi
  echo "!! unexpected response:"
  printf '%s' "$RESP" | sed -e 's/<[^>]*>//g' | grep -v '^\s*$' | head -12
  exit 1
fi

echo "==> $MSG"
case "$MSG" in
  *Failure*|*Failed*)
    echo
    echo "Compile errors show up in the device log:"
    echo "  telnet ${ROKU} 8085"
    exit 1
    ;;
esac

echo
echo "Launched on the TV. Watch the log with:  telnet ${ROKU} 8085"
