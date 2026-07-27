#!/usr/bin/env bash
# Bring up a WireGuard tunnel to Private Internet Access.
#
# PIA does not hand out static WireGuard configs. You exchange your account
# credentials for a short-lived token, then register a freshly generated public
# key with one specific server, which returns the peer details. So the config is
# generated at connect time rather than stored.
#
# The gateway sends all BitTorrent traffic through this tunnel: the swarm sees
# PIA's address, never the home connection.
set -euo pipefail

ENV_FILE=${ENV_FILE:-/etc/stream-gateway/pia.env}
REGION=${PIA_REGION:-us_chicago}
WG_IF=${WG_IF:-pia}

[ -f "$ENV_FILE" ] || { echo "missing $ENV_FILE"; exit 1; }
# shellcheck disable=SC1090
. "$ENV_FILE"

: "${PIA_USER:?PIA_USER not set}"
: "${PIA_PASS:?PIA_PASS not set}"

echo "==> fetching PIA server list"
SERVERS=$(curl -sS --max-time 20 "https://serverlist.piaservers.net/vpninfo/servers/v6" | head -1)

read -r WG_HOST WG_SERVER_IP WG_CN <<<"$(
  printf '%s' "$SERVERS" | jq -r --arg r "$REGION" '
    (.regions[] | select(.id == $r) | .servers.wg[0]) as $s
    | "\($s.ip) \($s.ip) \($s.cn)"
  '
)"
[ -n "${WG_CN:-}" ] || { echo "region $REGION has no wireguard server"; exit 1; }
echo "    server $WG_CN ($WG_SERVER_IP)"

echo "==> getting auth token"
TOKEN=$(curl -sS --max-time 20 -u "${PIA_USER}:${PIA_PASS}" \
  "https://privateinternetaccess.com/gtoken/generateToken" | jq -r '.token')
[ "$TOKEN" != "null" ] && [ -n "$TOKEN" ] || { echo "auth failed - check credentials"; exit 1; }

echo "==> registering key"
PRIV=$(wg genkey)
PUB=$(printf '%s' "$PRIV" | wg pubkey)

# PIA presents a certificate for the server's common name, not its IP, so the
# request is pinned to the CN and resolved manually.
RESP=$(curl -sS --max-time 20 -G \
  --connect-to "${WG_CN}::${WG_SERVER_IP}:" \
  --cacert /etc/stream-gateway/ca.rsa.4096.crt \
  --data-urlencode "pt=${TOKEN}" \
  --data-urlencode "pubkey=${PUB}" \
  "https://${WG_CN}:1337/addKey")

STATUS=$(printf '%s' "$RESP" | jq -r '.status')
[ "$STATUS" = "OK" ] || { echo "addKey failed: $RESP"; exit 1; }

PEER_IP=$(printf '%s' "$RESP" | jq -r '.peer_ip')
SERVER_KEY=$(printf '%s' "$RESP" | jq -r '.server_key')
SERVER_PORT=$(printf '%s' "$RESP" | jq -r '.server_port')
DNS=$(printf '%s' "$RESP" | jq -r '.dns_servers[0]')

echo "==> configuring $WG_IF ($PEER_IP)"
umask 077
mkdir -p /etc/wireguard
cat > "/etc/wireguard/${WG_IF}.conf" <<EOF
[Interface]
Address = ${PEER_IP}
PrivateKey = ${PRIV}
# No DNS= here: the container keeps using LAN DNS so it can still reach
# Prowlarr and be reached by the Roku. Only torrent traffic is routed out.

[Peer]
PublicKey = ${SERVER_KEY}
Endpoint = ${WG_SERVER_IP}:${SERVER_PORT}
# Full tunnel, but see the policy routing below -- LAN stays reachable.
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
EOF

wg-quick down "$WG_IF" 2>/dev/null || true
wg-quick up "$WG_IF"

echo "==> verifying egress"
sleep 2
VPN_IP=$(curl -sS --max-time 15 --interface "$WG_IF" https://api.ipify.org || echo FAILED)
echo "    tunnel egress IP: $VPN_IP"
echo "    PIA DNS: $DNS"

if [ "$VPN_IP" = "FAILED" ]; then
  echo "!! tunnel is up but no traffic is flowing"
  exit 1
fi
echo "==> ok"
