#!/usr/bin/env bash
# Build the network namespace that /api/link/* fetches from.
#
# WHY THIS EXISTS
#
# mw-search holds a WireGuard tunnel to the house: that is how it reaches
# Prowlarr at 192.168.0.115. /api/link/* takes a URL from an anonymous stranger
# and fetches it, so any hole in that endpoint is a hole into the LAN.
#
# link_guard.go already refuses private addresses, re-checks every redirect,
# pins resolved IPs against rebinding, and confines yt-dlp to a proxy that does
# the same. That is good, and it is still application code deciding what to
# dial. This script removes the decision: inside `yarrit-egress` there is no
# route to RFC1918 at all, so the question never reaches any code of ours.
#
# The two layers fail independently, which is the whole point:
#   * break the guard   -> the kernel still says "Network is unreachable"
#   * remove this netns -> the guard still refuses the address
#
# Prove it with yarrit-egress-verify.sh, which uses plain curl -- no guard
# anywhere in the path.
#
#   sudo ./yarrit-egress-netns.sh up
#   sudo ./yarrit-egress-netns.sh down
#   sudo ./yarrit-egress-netns.sh status

set -euo pipefail

NS=${NS:-yarrit-egress}
HOST_IF=${HOST_IF:-veth-yarrit}
NS_IF=${NS_IF:-veth-egress}
HOST_IP=${HOST_IP:-10.201.0.1}
NS_IP=${NS_IP:-10.201.0.2}
PREFIX=30
# Whatever carries traffic to the internet. Detected rather than assumed.
UPLINK=${UPLINK:-$(ip route show default | awk '/default/ {print $5; exit}')}

# Everything the namespace must never be able to reach. RFC1918 is the house;
# the rest are the addresses an SSRF payload reaches for when RFC1918 is shut.
BLOCKED=(
  10.0.0.0/8
  172.16.0.0/12
  192.168.0.0/16
  169.254.0.0/16   # link-local, and AWS/GCP metadata at 169.254.169.254
  100.64.0.0/10    # CGNAT
  127.0.0.0/8      # the host's own loopback, reached via the veth
  192.0.0.0/24
  198.18.0.0/15
)

up() {
  [ -n "$UPLINK" ] || { echo "cannot detect the uplink interface; set UPLINK=" >&2; exit 1; }

  # Start from nothing every time. Reconfiguring a half-built namespace in place
  # meant a restart could fail with "File exists" against a namespace that was
  # confining traffic perfectly well -- and a unit that will not restart is one
  # somebody eventually disables. `down` is already idempotent and silent.
  down >/dev/null 2>&1 || true

  ip netns add "$NS"
  ip link add "$HOST_IF" type veth peer name "$NS_IF"
  ip link set "$NS_IF" netns "$NS"

  ip addr add "$HOST_IP/$PREFIX" dev "$HOST_IF"
  ip link set "$HOST_IF" up
  ip netns exec "$NS" ip addr add "$NS_IP/$PREFIX" dev "$NS_IF"
  ip netns exec "$NS" ip link set "$NS_IF" up
  ip netns exec "$NS" ip link set lo up
  ip netns exec "$NS" ip route add default via "$HOST_IP"

  # The load-bearing lines. A blackhole route is more specific than the default,
  # so it wins, and a socket bound in this namespace gets ENETUNREACH before a
  # single packet is built. This is what makes the failure a kernel fact rather
  # than a policy decision -- and it is what verify.sh asserts.
  for net in "${BLOCKED[@]}"; do
    ip netns exec "$NS" ip route add blackhole "$net"
  done

  # Forwarding and NAT so the namespace can still reach the public internet,
  # which is the entire point of it existing.
  sysctl -qw net.ipv4.ip_forward=1
  iptables -t nat -C POSTROUTING -s "$NS_IP/$PREFIX" -o "$UPLINK" -j MASQUERADE 2>/dev/null ||
    iptables -t nat -A POSTROUTING -s "$NS_IP/$PREFIX" -o "$UPLINK" -j MASQUERADE

  # Belt and braces at the host's forwarding stage. If someone ever deletes a
  # blackhole route, the host still refuses to carry that traffic onward -- and
  # the host is the machine with the tunnel, so this is the rule that actually
  # protects the LAN.
  for net in "${BLOCKED[@]}"; do
    iptables -C FORWARD -s "$NS_IP/$PREFIX" -d "$net" -j REJECT 2>/dev/null ||
      iptables -I FORWARD -s "$NS_IP/$PREFIX" -d "$net" -j REJECT
  done
  # The veth peer address itself has to stay reachable: mw-search talks to the
  # in-namespace proxy over it, and the proxy answers on it.
  iptables -C FORWARD -s "$NS_IP/$PREFIX" -o "$UPLINK" -j ACCEPT 2>/dev/null ||
    iptables -A FORWARD -s "$NS_IP/$PREFIX" -o "$UPLINK" -j ACCEPT

  echo "namespace $NS is up (uplink $UPLINK, proxy address $NS_IP)"
}

down() {
  for net in "${BLOCKED[@]}"; do
    iptables -D FORWARD -s "$NS_IP/$PREFIX" -d "$net" -j REJECT 2>/dev/null || true
  done
  iptables -D FORWARD -s "$NS_IP/$PREFIX" -o "$UPLINK" -j ACCEPT 2>/dev/null || true
  iptables -t nat -D POSTROUTING -s "$NS_IP/$PREFIX" -o "$UPLINK" -j MASQUERADE 2>/dev/null || true
  ip link del "$HOST_IF" 2>/dev/null || true
  ip netns del "$NS" 2>/dev/null || true
  echo "namespace $NS is down"
}

status() {
  echo "== namespaces =="; ip netns list
  echo "== routes inside $NS =="; ip netns exec "$NS" ip route show
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  status) status ;;
  *) echo "usage: $0 {up|down|status}" >&2; exit 2 ;;
esac
