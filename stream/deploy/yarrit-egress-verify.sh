#!/usr/bin/env bash
# Prove the network namespace confines /api/link/* on its own.
#
# This deliberately uses plain curl. There is no SSRF guard anywhere in the
# path -- curl will cheerfully connect to 192.168.0.115 if the kernel lets it.
# That is the point: if these checks pass, the isolation holds even with every
# line of link_guard.go deleted.
#
#   sudo ./yarrit-egress-verify.sh
#
# Exits non-zero on the first failure, so it is usable as a deploy gate.

set -uo pipefail

NS=${NS:-yarrit-egress}
PROWLARR=${PROWLARR:-http://192.168.0.115:9696/}
fails=0

ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; fails=$((fails + 1)); }

if ! ip netns list | grep -q "^${NS}\b"; then
  echo "namespace $NS does not exist; run yarrit-egress-netns.sh up" >&2
  exit 1
fi

echo "Guard-free reachability from inside $NS (plain curl, no application code):"

# The one that matters. Wade's Prowlarr, over the tunnel the host holds.
out=$(ip netns exec "$NS" curl -s -m 5 -o /dev/null -w '%{http_code}' "$PROWLARR" 2>&1)
rc=$?
if [ "$rc" -ne 0 ] && [ "$out" != "200" ]; then
  ok "Prowlarr ($PROWLARR) is unreachable — curl exit $rc"
else
  bad "Prowlarr ANSWERED from inside the namespace (http $out) — the isolation is not holding"
fi

# Everything else the namespace must not reach.
for target in \
  "http://192.168.0.1/" \
  "http://10.0.0.1/" \
  "http://172.16.0.1/" \
  "http://169.254.169.254/latest/meta-data/" \
  "http://127.0.0.1:8802/api/health"
do
  if ip netns exec "$NS" curl -s -m 4 -o /dev/null "$target" 2>/dev/null; then
    bad "$target answered from inside the namespace"
  else
    ok "$target is unreachable"
  fi
done

# ENETUNREACH specifically, not a timeout. A timeout would mean the packet left
# and nothing answered; unreachable means it was never built.
err=$(ip netns exec "$NS" curl -s -m 5 "$PROWLARR" 2>&1 || true)
case "$err" in
  *"Network is unreachable"*|*"Could not connect"*|*"Failed to connect"*)
    ok "the failure is a routing failure, not a timeout: ${err:0:60}" ;;
  *) bad "unexpected failure mode (wanted ENETUNREACH): ${err:0:90}" ;;
esac

echo
echo "The namespace must still reach the public internet, or it is just broken:"
if ip netns exec "$NS" curl -s -m 15 -o /dev/null "https://www.youtube.com/"; then
  ok "public internet is reachable"
else
  bad "public internet is NOT reachable — yt-dlp would fail for every link"
fi
if ip netns exec "$NS" getent hosts youtube.com >/dev/null 2>&1; then
  ok "DNS resolves inside the namespace"
else
  bad "DNS does not resolve inside the namespace"
fi

echo
if [ "$fails" -eq 0 ]; then
  echo "All checks passed: the namespace confines egress without the guard's help."
  exit 0
fi
echo "$fails check(s) failed." >&2
exit 1
