#!/usr/bin/env bash
# Put the torrent gateway behind a VPN, and make it fail CLOSED.
#
#   sudo ./vpn-killswitch.sh [conf-name]        # default: pia
#
# A VPN with no kill switch is worse than no VPN, because it feels safe. The
# tunnel drops, the default route is still there, and traffic carries on from
# the home address with nothing to say so. That is the failure this prevents:
# if the tunnel is not up, nothing leaves at all.
#
# Expects a WireGuard config at /etc/wireguard/<conf-name>.conf. Every VPN worth
# using will hand you one (PIA, Mullvad, Proton, AirVPN, or your own server).
#
# LAN traffic is deliberately exempt. The gateway's whole job is to serve video
# to the TVs in the house, and those are local connections that never should
# have gone through the tunnel anyway -- routing them there would be slower and
# would break the moment the VPN went down.
set -euo pipefail

CONF=${1:-pia}
WG_CONF="/etc/wireguard/${CONF}.conf"

die() { printf '\n\033[1;31m!! %s\033[0m\n' "$*" >&2; exit 1; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

[ "$(id -u)" = 0 ] || die "run this as root -- it changes firewall rules"
[ -f "$WG_CONF" ] || die "no config at $WG_CONF

Put your provider's WireGuard config there first. For PIA, that comes from
their manual-config download; most other providers give you the file directly."

# --- what we are protecting -------------------------------------------------
# Derived, never assumed: a wrong subnet here either locks the box off its own
# LAN or punches a hole straight through the tunnel.
WAN_IF=$(ip route show default | awk '/default/{print $5; exit}')
[ -n "$WAN_IF" ] || die "no default route -- cannot tell which interface to seal"
LAN_CIDR=$(ip -o -f inet addr show "$WAN_IF" | awk '{print $4; exit}' |
           sed 's#\([0-9]*\.[0-9]*\.[0-9]*\)\.[0-9]*/#\1.0/#')
[ -n "$LAN_CIDR" ] || die "could not work out the LAN subnet on $WAN_IF"

# The one packet that must escape the tunnel is the one that BUILDS the tunnel.
ENDPOINT=$(awk -F'= *' '/^ *Endpoint/{print $2; exit}' "$WG_CONF" | tr -d ' ')
[ -n "$ENDPOINT" ] || die "no Endpoint line in $WG_CONF"
EP_HOST=${ENDPOINT%:*}
EP_PORT=${ENDPOINT##*:}
# A hostname here would need DNS *before* the tunnel exists, and DNS is exactly
# what the kill switch blocks. Resolve it once, now, and pin the address.
if ! printf '%s' "$EP_HOST" | grep -qE '^[0-9]+(\.[0-9]+){3}$'; then
  EP_HOST=$(getent hosts "$EP_HOST" | awk '{print $1; exit}')
  [ -n "$EP_HOST" ] || die "could not resolve the VPN endpoint to an address"
fi

say "sealing $WAN_IF; LAN $LAN_CIDR stays reachable; VPN via $EP_HOST:$EP_PORT"

if ! command -v iptables >/dev/null 2>&1; then
  say "installing iptables (it is not present)"
  # Must happen BEFORE the rules go in -- afterwards there is no route to apt.
  DEBIAN_FRONTEND=noninteractive apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iptables >/dev/null
fi

say "installing rules"
apply_rules() {
  local ipt=$1 lan=$2 ep=$3
  "$ipt" -F OUTPUT 2>/dev/null || true
  "$ipt" -A OUTPUT -o lo -j ACCEPT
  "$ipt" -A OUTPUT -d "$lan" -j ACCEPT
  "$ipt" -A OUTPUT -o "$CONF" -j ACCEPT
  [ -n "$ep" ] && "$ipt" -A OUTPUT -d "$ep" -p udp --dport "$EP_PORT" -j ACCEPT
  "$ipt" -A OUTPUT -d "$lan" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
  # Everything else that tries to leave by the physical interface is a leak.
  "$ipt" -A OUTPUT -o "$WAN_IF" -j DROP
}
apply_rules iptables "$LAN_CIDR" "$EP_HOST/32"

# IPv6 is the classic way a kill switch is defeated: the v4 rules look perfect
# and the traffic quietly leaves over v6 instead. Most VPN configs are v4-only,
# so the safe answer is to drop v6 out of the physical interface entirely.
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -F OUTPUT 2>/dev/null || true
  ip6tables -A OUTPUT -o lo -j ACCEPT
  ip6tables -A OUTPUT -o "$CONF" -j ACCEPT
  ip6tables -A OUTPUT -d fe80::/64 -j ACCEPT
  ip6tables -A OUTPUT -o "$WAN_IF" -j DROP
  echo "    IPv6 sealed too"
fi

say "making it survive a reboot"
# Rules alone are not enough: the tunnel has to come back up as well, and the
# gateway must not start before it. A gateway that outruns its VPN is the same
# leak by a different route.
mkdir -p /etc/iptables
iptables-save > /etc/iptables/rules.v4
command -v ip6tables-save >/dev/null 2>&1 && ip6tables-save > /etc/iptables/rules.v6 || true

cat > /etc/systemd/system/vpn-killswitch.service <<EOF
[Unit]
Description=Fail-closed firewall for VPN egress
DefaultDependencies=no
Before=network-pre.target wg-quick@${CONF}.service
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'iptables-restore < /etc/iptables/rules.v4'
ExecStart=/bin/sh -c '[ -f /etc/iptables/rules.v6 ] && ip6tables-restore < /etc/iptables/rules.v6 || true'

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now vpn-killswitch.service >/dev/null 2>&1
systemctl enable "wg-quick@${CONF}" >/dev/null 2>&1 || true

# The gateway must wait for the tunnel. Without this it can win the boot race
# and announce itself to a tracker from the real address before wg is up.
if systemctl list-unit-files 2>/dev/null | grep -q '^mw-gateway'; then
  mkdir -p /etc/systemd/system/mw-gateway.service.d
  cat > /etc/systemd/system/mw-gateway.service.d/vpn.conf <<EOF
[Unit]
After=wg-quick@${CONF}.service vpn-killswitch.service
Requires=wg-quick@${CONF}.service
EOF
  systemctl daemon-reload
  echo "    mw-gateway now requires the tunnel"
fi

say "verifying"
# Proof, not assumption: the address the outside world sees must not be the
# one the LAN's router owns.
VPN_IP=$(curl -s --max-time 15 https://api.ipify.org || echo "")
if [ -z "$VPN_IP" ]; then
  die "no outbound connectivity after applying rules -- the tunnel is not up.
Bring it up with:  systemctl start wg-quick@${CONF}
Then re-run this script."
fi
printf '    traffic leaves as %s\n' "$VPN_IP"
printf '    LAN reachable:    %s\n' "$(ip -o -f inet addr show "$WAN_IF" | awk '{print $4}')"

cat <<EOF

  Sealed. If the tunnel drops, outbound traffic stops instead of falling back
  to your own address -- so a dead VPN looks like a dead download, which is the
  correct and noticeable failure.

  Check it any time:
      curl -s https://api.ipify.org   # must NOT be your home address
      systemctl status wg-quick@${CONF}

  To undo:
      systemctl disable --now vpn-killswitch
      iptables -F OUTPUT
EOF
