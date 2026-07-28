#!/usr/bin/env bash
# Provision a bare VPS as the inbound mail edge for moveweight.net.
#
# Written after the 2026-07-28 rebuild, which wiped the host. Everything the
# edge needed had only ever been configured by hand: postfix/main.cf.effective
# is a capture of the result, not something that can be applied, and
# wireguard/README.md documents the layout without carrying the config. This
# script is the missing half.
#
# Two secrets cannot be restored, only replaced -- both private keys lived on
# the wiped disk:
#
#   * the WireGuard private key. The public key in wireguard/README.md
#     (6Znsb0AW...) is now stale; a new one is generated here and printed, and
#     LXC 160's peer block MUST be updated to match or the tunnel simply never
#     handshakes. That failure is silent: `wg show` reports 0 B received and
#     mail queues rather than erroring.
#   * /root/.ssh/hestia_sync, used to pull the recipient list. A new keypair is
#     generated and its public half printed for LXC 160's authorized_keys.
#
# Safe to re-run. Existing keys are kept rather than regenerated, so a second
# run does not invalidate a tunnel that is already working.
set -euo pipefail
cd "$(dirname "$0")/.."

JUMP=root@192.168.0.6
KEY=/root/.ssh/vps_edge
VPS=root@104.129.28.137
HESTIA_PEER_PUBKEY=W9dlV1pNfy6GiB6XU3kTYl5Ruj0WScNVzUjyEEBecTs=

on_vps() { ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS 'bash -s'"; }
to_vps() { ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS 'cat > $1'" < "$2"; }

echo "==> checking access"
printf 'true\n' | on_vps || {
  cat <<EOF
cannot reach $VPS with $KEY.

A rebuilt host has no authorized_keys. On the VPS console:
  mkdir -p /root/.ssh && chmod 700 /root/.ssh
  echo '<contents of ${KEY}.pub>' >> /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
EOF
  exit 1
}

echo "==> packages"
printf '%s\n' '
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq postfix postfix-pcre wireguard-tools ufw certbot vnstat
' | on_vps

echo "==> wireguard (listener at 10.10.10.3)"
printf '%s\n' "
set -euo pipefail
umask 077
mkdir -p /etc/wireguard
# Kept if present: regenerating would silently break a tunnel that already works.
[ -f /etc/wireguard/vps_private.key ] || wg genkey > /etc/wireguard/vps_private.key
wg pubkey < /etc/wireguard/vps_private.key > /etc/wireguard/vps_public.key

cat > /etc/wireguard/wg0.conf <<EOF
[Interface]
Address = 10.10.10.3/24
ListenPort = 51820
PostUp = wg set %i private-key /etc/wireguard/vps_private.key

[Peer]
# LXC 160 (HestiaCP mail). Home dials out; no endpoint is pinned here, which
# is what makes this survive a CableOne address change.
PublicKey = ${HESTIA_PEER_PUBKEY}
AllowedIPs = 10.10.10.0/24, 192.168.0.0/24
EOF
chmod 600 /etc/wireguard/wg0.conf
systemctl enable --now wg-quick@wg0 >/dev/null 2>&1 || systemctl restart wg-quick@wg0
" | on_vps

echo "==> recipient-sync key"
printf '%s\n' '
set -euo pipefail
[ -f /root/.ssh/hestia_sync ] || ssh-keygen -t ed25519 -N "" -C mw-vps-hestia-sync -f /root/.ssh/hestia_sync >/dev/null
' | on_vps

echo "==> postfix"
to_vps /etc/postfix/relay_domains postfix/relay_domains
printf '%s\n' '
set -euo pipefail
# Applied with postconf rather than by shipping main.cf, so packaged defaults
# stay intact and only what we actually mean to set is changed.
postconf -e \
  "myhostname = relay.moveweight.net" \
  "mydomain = moveweight.net" \
  "myorigin = \$myhostname" \
  "mydestination =" \
  "inet_protocols = ipv4" \
  "local_recipient_maps =" \
  "local_transport = error:5.1.1 no local delivery on this relay" \
  "relay_domains = /etc/postfix/relay_domains" \
  "relay_recipient_maps = hash:/etc/postfix/relay_recipients" \
  "relay_transport = smtp:[10.10.10.1]:25" \
  "relayhost =" \
  "message_size_limit = 52428800" \
  "maximal_queue_lifetime = 3d" \
  "bounce_queue_lifetime = 1d" \
  "smtpd_helo_required = yes" \
  "smtpd_banner = \$myhostname ESMTP" \
  "smtpd_client_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_rbl_client zen.spamhaus.org" \
  "smtpd_recipient_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination" \
  "smtpd_relay_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination" \
  "smtp_tls_security_level = may" \
  "smtpd_tls_security_level = may" \
  "smtpd_tls_auth_only = yes"

# reject_unauth_destination is what keeps this from being an open relay. If it
# is ever missing from both restriction lists, refuse to continue.
postconf -n | grep -q "smtpd_relay_restrictions.*reject_unauth_destination" || {
  echo "REFUSING: smtpd_relay_restrictions lacks reject_unauth_destination" >&2
  exit 1
}

# An empty map still has to exist and be hashed, or postfix rejects every
# recipient until the first sync runs.
touch /etc/postfix/relay_recipients
postmap /etc/postfix/relay_recipients
systemctl enable --now postfix >/dev/null 2>&1
systemctl restart postfix
' | on_vps

echo "==> recipient sync (script + timer)"
to_vps /usr/local/sbin/sync-recipients.sh          scripts/sync-recipients.sh
to_vps /etc/systemd/system/sync-recipients.service postfix/sync-recipients.service
to_vps /etc/systemd/system/sync-recipients.timer   postfix/sync-recipients.timer
printf '%s\n' '
set -euo pipefail
chmod 0755 /usr/local/sbin/sync-recipients.sh
systemctl daemon-reload
# The timer is enabled, but the unit is not run now: it cannot succeed until
# the LXC 160 steps below are done, and a failed first run would look like a
# provisioning error rather than a pending handover.
systemctl enable --now sync-recipients.timer >/dev/null 2>&1
' | on_vps

echo "==> firewall"
printf '%s\n' '
set -euo pipefail
ufw allow 22/udp >/dev/null 2>&1 || true
ufw allow 22/tcp >/dev/null 2>&1
ufw allow 25/tcp >/dev/null 2>&1
ufw allow 51820/udp >/dev/null 2>&1
ufw --force enable >/dev/null 2>&1 || true
' | on_vps

echo "==> state"
printf '%s\n' '
for u in wg-quick@wg0 postfix; do printf "    %-16s %s\n" "$u" "$(systemctl is-active $u)"; done
echo "    tunnel:"; wg show 2>/dev/null | sed "s/^/      /" | head -8
echo
echo "    ==== NEW WireGuard public key (VPS) ===="
sed "s/^/      /" /etc/wireguard/vps_public.key
echo "    ==== NEW recipient-sync public key ===="
sed "s/^/      /" /root/.ssh/hestia_sync.pub
' | on_vps

cat <<'EOF'

==> Finish on LXC 160 (both are required; neither fails loudly)

  1. Replace the VPS peer's PublicKey in /etc/wireguard/wg0.conf with the key
     printed above, then `systemctl restart wg-quick@wg0`. The old key
     (6Znsb0AW...) died with the rebuild. A mismatch does not error -- the
     handshake just never completes and mail sits in the queue.

  2. Authorise the recipient-sync key, locked to the listing command so the
     edge can obtain the recipient list and nothing else:

       command="/usr/local/sbin/list-recipients.py",no-port-forwarding,\
       no-agent-forwarding,no-pty <the ed25519 key printed above>

Then, back here:
  bash scripts/vps.sh '/usr/local/sbin/sync-recipients.sh'   # populate the map
  bash scripts/soak-check.sh                                 # verify

TLS is not configured by this script. smtpd_tls_cert_file previously pointed at
/etc/letsencrypt/live/relay/, which needs a certbot run for relay.moveweight.net
once DNS points here; postfix runs with tls_security_level=may until then.
EOF
